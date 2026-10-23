// Package simulation is a durable, deliberately non-idempotent fake store. It has no vendor credentials or endpoints.
package simulation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	_ "modernc.org/sqlite"
)

type Options struct {
	Faults       map[string]string
	Review       string
	Availability string
	Inconclusive bool
}
type Server struct {
	db      *sql.DB
	mu      sync.Mutex
	options Options
	owner   *os.File
}
type query struct {
	Scope   string            `json:"scope"`
	Version int64             `json:"version_code"`
	Ref     string            `json:"remote_ref"`
	Request *protocol.Request `json:"request,omitempty"`
}

func New(dir string, options Options) (*Server, error) {
	var absErr error
	dir, absErr = filepath.Abs(dir)
	if absErr != nil {
		return nil, absErr
	}
	if _, err := os.Stat(filepath.Join(dir, "state.sqlite")); err == nil {
		return nil, fmt.Errorf("fake store directory must be separate from publisher state")
	}
	if err := privatefs.Dir(dir); err != nil {
		return nil, err
	}
	owner, err := privatefs.Open(filepath.Join(dir, ".fake.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		owner.Close()
		return nil, fmt.Errorf("fake store is already owned by another process")
	}
	keepOwner := false
	defer func() {
		if !keepOwner {
			owner.Close()
		}
	}()
	f, err := privatefs.Open(filepath.Join(dir, "fake.sqlite"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", filepath.Join(dir, "fake.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=1000;
CREATE TABLE IF NOT EXISTS calls(id INTEGER PRIMARY KEY, operation_id TEXT NOT NULL, channel TEXT NOT NULL, kind TEXT NOT NULL, scope TEXT NOT NULL, request TEXT NOT NULL, ref TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS receipts(operation_id TEXT PRIMARY KEY, receipt TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS apps(scope TEXT PRIMARY KEY, metadata TEXT NOT NULL, active_intent TEXT NOT NULL, version_code INTEGER NOT NULL, remote_ref TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS faults(name TEXT PRIMARY KEY, used INTEGER NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	if options.Review == "" {
		options.Review = "in_review"
	}
	if options.Availability == "" {
		options.Availability = "not_observed_live"
	}
	if _, err = db.Exec("CREATE TABLE IF NOT EXISTS attempts(id INTEGER PRIMARY KEY,channel TEXT NOT NULL,kind TEXT NOT NULL)"); err != nil {
		db.Close()
		return nil, err
	}
	var timestampColumn int
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('calls') WHERE name='created_at'").Scan(&timestampColumn); err != nil {
		db.Close()
		return nil, err
	}
	if timestampColumn == 0 {
		if _, err = db.Exec("ALTER TABLE calls ADD COLUMN created_at TEXT NOT NULL DEFAULT ''"); err != nil {
			db.Close()
			return nil, err
		}
	}
	keepOwner = true
	return &Server{db: db, options: options, owner: owner}, nil
}
func (s *Server) Close() error {
	err := s.db.Close()
	if s.owner != nil {
		s.owner.Close()
		s.owner = nil
	}
	return err
}
func defaultMetadata() map[string]string {
	return map[string]string{"title": "Existing title", "privacy_policy": "https://example.invalid/privacy"}
}
func (s *Server) inspect(scope string) (protocol.RemoteSnapshot, error) {
	r := protocol.RemoteSnapshot{Scope: scope, Metadata: defaultMetadata()}
	var data string
	err := s.db.QueryRow("SELECT metadata,active_intent FROM apps WHERE scope=?", scope).Scan(&data, &r.ActiveIntent)
	if err == sql.ErrNoRows {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal([]byte(data), &r.Metadata)
	return r, err
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("extra JSON data")
	}
	return nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/browser/") {
		s.browser(w, r)
		return
	}
	w.Header().Set("X-Publisher-Simulation", "1")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result any
	var err error
	switch r.URL.Path {
	case "/inspect":
		var q query
		if err = decode(r, &q); err == nil {
			result, err = s.inspect(q.Scope)
		}
	case "/execute":
		var q protocol.Request
		if err = decode(r, &q); err == nil {
			var drop bool
			result, drop, err = s.execute(q)
			if drop && err == nil {
				if h, ok := w.(http.Hijacker); ok {
					c, _, e := h.Hijack()
					if e == nil {
						c.Close()
						return
					}
				}
				http.Error(w, "simulated lost response", 503)
				return
			}
		}
	case "/reconcile":
		var q query
		if err = decode(r, &q); err == nil && q.Request != nil {
			req := *q.Request
			reconciliation := protocol.Reconciliation{OperationID: req.OperationID, RequestDigest: protocol.Digest(req), Result: "inconclusive"}
			if !s.options.Inconclusive {
				var data string
				e := s.db.QueryRow("SELECT receipt FROM receipts WHERE operation_id=?", req.OperationID).Scan(&data)
				if e == sql.ErrNoRows {
					reconciliation.Result = "confirmed_not_applied"
				} else if e != nil {
					err = e
				} else {
					var receipt protocol.Receipt
					if err = json.Unmarshal([]byte(data), &receipt); err == nil && receipt.Validate(req) == nil {
						if receipt.Effect == "confirmed_applied" {
							reconciliation.Result = "confirmed_applied"
							reconciliation.Receipt = &receipt
						} else {
							reconciliation.Result = "confirmed_not_applied"
						}
					}
				}
			}
			result = reconciliation
		} else if err == nil {
			err = fmt.Errorf("request required")
		}
	case "/observe":
		var q query
		if err = decode(r, &q); err == nil {
			var version int64
			var ref string
			err = s.db.QueryRow("SELECT version_code,remote_ref FROM apps WHERE scope=?", q.Scope).Scan(&version, &ref)
			if err == nil {
				result = protocol.Observation{Scope: q.Scope, VersionCode: version, RemoteRef: ref, Review: s.options.Review, Availability: s.options.Availability, Source: "simulation", CollectedAt: time.Now().UTC()}
			}
		}
	case "/stats":
		result, err = s.stats()
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "invalid simulation request", 400)
		return
	}
	json.NewEncoder(w).Encode(result)
}
func (s *Server) useFault(channel, point string) (bool, error) {
	if s.options.Faults[channel] != point {
		return false, nil
	}
	r, err := s.db.Exec("INSERT INTO faults VALUES(?,1) ON CONFLICT(name) DO NOTHING", channel+":"+point)
	if err != nil {
		return false, err
	}
	n, _ := r.RowsAffected()
	return n == 1, nil
}
func (s *Server) execute(q protocol.Request) (protocol.Receipt, bool, error) {
	r := protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied"}
	if _, err := s.db.Exec("INSERT INTO attempts(channel,kind) VALUES(?,?)", q.Channel, q.Kind); err != nil {
		return r, false, err
	}
	if !slices.Contains(manifest.Channels, q.Channel) || !manifest.ValidHash(q.IntentDigest) || !manifest.ValidHash(q.OperationID) || !manifest.ValidHash(q.Scope) || !manifest.ValidHash(q.ArtifactSHA256) || !slices.Contains([]string{"upload", "bind", "submit", "push"}, q.Kind) || q.VersionCode <= 0 || q.Metadata == nil {
		return r, false, fmt.Errorf("invalid operation")
	}
	remote, err := s.inspect(q.Scope)
	if err != nil {
		return r, false, err
	}
	if protocol.Digest(remote.Metadata) != q.SnapshotDigest || (remote.ActiveIntent != "" && remote.ActiveIntent != q.IntentDigest) {
		r.Effect = "confirmed_rejected"
	}
	if q.Kind == "bind" || q.Kind == "submit" {
		expected := "upload"
		if q.Kind == "submit" {
			expected = "bind"
		}
		var data string
		e := s.db.QueryRow("SELECT request FROM calls WHERE ref=? AND scope=? AND kind=? ORDER BY id DESC LIMIT 1", q.PreviousRef, q.Scope, expected).Scan(&data)
		var prev protocol.Request
		if e != nil || json.Unmarshal([]byte(data), &prev) != nil || prev.IntentDigest != q.IntentDigest {
			r.Effect = "confirmed_rejected"
		}
	}
	if r.Effect == "confirmed_rejected" {
		data, _ := json.Marshal(r)
		_, err = s.db.Exec("INSERT INTO receipts VALUES(?,?) ON CONFLICT(operation_id) DO UPDATE SET receipt=excluded.receipt WHERE json_extract(receipts.receipt,'$.effect')<>'confirmed_applied'", q.OperationID, string(data))
		return r, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return r, false, err
	}
	defer tx.Rollback()
	var seq int64
	if err = tx.QueryRow("SELECT COALESCE(MAX(id),0)+1 FROM calls").Scan(&seq); err != nil {
		return r, false, err
	}
	r.RemoteRef = fmt.Sprintf("9007199254740993-%d-%s", seq, q.Kind)
	data, _ := json.Marshal(q)
	if _, err = tx.Exec("INSERT INTO calls(operation_id,channel,kind,scope,request,ref,created_at) VALUES(?,?,?,?,?,?,?)", q.OperationID, q.Channel, q.Kind, q.Scope, string(data), r.RemoteRef, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return r, false, err
	}
	if q.Kind == "submit" || q.Kind == "push" {
		metadata, _ := json.Marshal(q.Metadata)
		if _, err = tx.Exec("INSERT INTO apps VALUES(?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET metadata=excluded.metadata,active_intent=excluded.active_intent,version_code=excluded.version_code,remote_ref=excluded.remote_ref", q.Scope, string(metadata), q.IntentDigest, q.VersionCode, r.RemoteRef); err != nil {
			return r, false, err
		}
	}
	data, _ = json.Marshal(r)
	if _, err = tx.Exec("INSERT INTO receipts VALUES(?,?) ON CONFLICT(operation_id) DO UPDATE SET receipt=excluded.receipt WHERE json_extract(receipts.receipt,'$.effect')<>'confirmed_applied'", q.OperationID, string(data)); err != nil {
		return r, false, err
	}
	if err = tx.Commit(); err != nil {
		return r, false, err
	}
	drop, err := s.useFault(q.Channel, "drop_"+q.Kind+"_once")
	if invalid, e := s.useFault(q.Channel, "invalid_"+q.Kind+"_once"); e != nil {
		return r, false, e
	} else if invalid {
		r.Effect = ""
	}
	return r, drop, err
}

type Stats struct {
	Calls   map[string]map[string]int `json:"calls"`
	Total   int                       `json:"total"`
	Applied int                       `json:"applied"`
}

func (s *Server) stats() (Stats, error) {
	r := Stats{Calls: map[string]map[string]int{}}
	rows, err := s.db.Query("SELECT channel,kind,COUNT(*) FROM attempts GROUP BY channel,kind")
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var channel, kind string
		var count int
		if err = rows.Scan(&channel, &kind, &count); err != nil {
			return r, err
		}
		if r.Calls[channel] == nil {
			r.Calls[channel] = map[string]int{}
		}
		r.Calls[channel][kind] = count
		r.Total += count
	}
	if err = rows.Err(); err != nil {
		return r, err
	}
	rows.Close()
	err = s.db.QueryRow("SELECT COUNT(*) FROM calls").Scan(&r.Applied)
	return r, err
}

type Client struct {
	base string
	http *http.Client
}

func NewClient(rawURL string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("simulation requires a plain HTTP loopback URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return nil, fmt.Errorf("simulation endpoint must use a literal loopback IP and port")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &Client{base: strings.TrimRight(rawURL, "/"), http: &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) call(ctx context.Context, path string, body, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("simulation request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("X-Publisher-Simulation") != "1" {
		return fmt.Errorf("not a valid simulation response")
	}
	d := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(result); err != nil {
		return fmt.Errorf("invalid simulation response schema")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("extra simulation response data")
	}
	return nil
}
func (c *Client) Inspect(ctx context.Context, scope string) (r protocol.RemoteSnapshot, err error) {
	err = c.call(ctx, "/inspect", query{Scope: scope}, &r)
	return
}
func (c *Client) ExecuteOne(ctx context.Context, q protocol.Request) (r protocol.Receipt, err error) {
	err = c.call(ctx, "/execute", q, &r)
	return
}
func (c *Client) Reconcile(ctx context.Context, q protocol.Request) (r protocol.Reconciliation, err error) {
	err = c.call(ctx, "/reconcile", query{Request: &q}, &r)
	return
}
func (c *Client) Observe(ctx context.Context, scope string, version int64, ref string) (r protocol.Observation, err error) {
	err = c.call(ctx, "/observe", query{Scope: scope, Version: version, Ref: ref}, &r)
	return
}
func (c *Client) Stats(ctx context.Context) (r Stats, err error) {
	err = c.call(ctx, "/stats", query{}, &r)
	return
}
