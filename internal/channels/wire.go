// Package channels implements fixed vendor protocols. It never retries a business
// request or interprets a transport success as acceptance. Production CLI wiring
// stays gated until account capabilities and frozen publishing inputs are verified.
package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

var origins = map[string]string{
	"xiaomi": "https://api.developer.xiaomi.com", "tencent": "https://p.open.qq.com",
	"huawei": "https://connect-api.cloud.huawei.com", "honor": "https://appmarket-openapi-drcn.cloud.honor.com",
	"oppo": "https://oop-openapi-cn.heytapmobi.com", "vivo": "https://developer-api.vivo.com.cn",
}

// Sender is the narrow seam for synthetic protocol servers. Controlled is the
// production implementation; it retains an M2 account lease for the whole call.
type Sender interface {
	Send(context.Context, transport.Request) ([]byte, error)
}
type Controlled struct {
	Lease   *account.Lease
	Scope   account.Scope
	observe func(context.Context, transport.Request, int, []byte, error) error
}

func (s Controlled) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	if q.Body != nil {
		defer q.Body.Close()
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" {
		return nil, fail("invalid_endpoint", "not_sent")
	}
	allowed := u.Scheme+"://"+u.Host == origins[s.Scope.Channel]
	if s.Scope.Channel == "honor" && u.Host == "iam.developer.honor.com" {
		allowed = true
	}
	if q.Grant != nil {
		allowed = allowedUpload(s.Scope.Channel, q.URL)
	}
	if !allowed {
		return nil, fail("endpoint_not_allowed", "not_sent")
	}
	c, err := transport.New(controlledPolicy(s.Scope, u.Scheme+"://"+u.Host, q))
	if err != nil {
		return nil, err
	}
	r, err := c.Do(ctx, s.Lease, q)
	if s.observe != nil {
		b := r.Bytes()
		observeErr := s.observe(ctx, q, r.Status, b, err)
		clear(b)
		if observeErr != nil {
			return nil, observeErr
		}
	}
	if err != nil {
		if s.Scope.Channel == "honor" && q.Class == transport.Lifecycle && q.Method == "POST" && u.Host == "iam.developer.honor.com" && u.Path == "/auth/token" && u.RawQuery == "" && q.Grant == nil {
			if network, ok := err.(*transport.Error); ok && network.Code == "http_status" {
				b := r.Bytes()
				defer clear(b)
				if rejected := honorOAuthRejection(r.Status, b, network); rejected != nil {
					return nil, rejected
				}
			}
		}
		return nil, err
	}
	if q.Grant != nil && r.Status != http.StatusOK {
		return nil, unknown()
	}
	return r.Bytes(), nil
}

func controlledPolicy(scope account.Scope, origin string, q transport.Request) transport.Policy {
	p := transport.Policy{ID: "vendor-" + scope.Channel, Origin: origin, Scope: scope, Upload: q.Grant != nil, Methods: []string{"GET", "POST", "PUT"}, Headers: []string{"Content-Type", "Authorization", "Client_id", "Accept", "X-Amz-Content-Sha256", "X-Amz-Date", "User-Agent"}}
	if scope.Channel == "vivo" && (q.Class == transport.Query || q.Class == transport.Write) && q.Grant == nil {
		// Closely spaced real details queries returned subCode 11010. Use a
		// conservative account limit for both queries and writes, not a
		// claimed vendor quota. Pre-write inspection must not consume the
		// same minute as the following business request. The budget
		// includes the durable account wait; never replay a refused query.
		p.MinInterval = time.Minute
		p.Timeout = 90 * time.Second
	}
	// APK parsing and uploads may take over a minute; the complete request and
	// response-header wait both retain the bounded, single-attempt M2 budget.
	if q.Class == transport.Write {
		p.Timeout = 90 * time.Second
		p.ResponseHeaderTimeout = 90 * time.Second
	}
	return p
}

// Auth is resolved inside credentials.Manager.WithAccount. Never serialize it.
type Auth struct {
	ID            string
	Secret, Token *credentials.Secret
	PublicKey     *rsa.PublicKey
}

func (Auth) String() string                 { return "[REDACTED channel auth]" }
func (a Auth) GoString() string             { return a.String() }
func (a Auth) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED channel auth]"`), nil }

type Client struct {
	Channel string
	Wire    Sender
	Auth    Auth
	Now     func() time.Time
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func (c *Client) String() string               { return "channel client" }
func (c *Client) GoString() string             { return c.String() }
func (c *Client) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED channel client]"`), nil }

type Error struct{ Code, Effect string }

func (e *Error) Error() string       { return "channel protocol: " + e.Code + " (" + e.Effect + ")" }
func (e *Error) WriteEffect() string { return e.Effect }
func fail(code, effect string) error { return &Error{code, effect} }
func unknown() error                 { return fail("missing_or_unrecognized_evidence", "unknown") }

// Object intentionally hides raw response data from logs. IDs are parsed without
// float64; required fields and gateway/business codes must be checked explicitly.
type object map[string]json.RawMessage

func (object) String() string                 { return "[REDACTED vendor response]" }
func (o object) GoString() string             { return o.String() }
func (o object) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED vendor response]"`), nil }
func decode(b []byte) (object, error) {
	var o map[string]json.RawMessage
	if len(b) == 0 || len(b) > 4<<20 || !utf8.Valid(b) || !uniqueJSON(b) || json.Unmarshal(b, &o) != nil || o == nil {
		return nil, unknown()
	}
	return object(o), nil
}

func uniqueJSON(b []byte) bool {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var walk func(int) bool
	walk = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				s, ok := key.(string)
				if err != nil || !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !walk(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !walk(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
func (o object) str(k string) string { var s string; _ = json.Unmarshal(o[k], &s); return s }
func (o object) num(k string) (int64, bool) {
	var n int64
	err := json.Unmarshal(o[k], &n)
	return n, err == nil && string(o[k]) != "null"
}
func (o object) id(k string) string {
	if s := o.str(k); s != "" {
		return s
	}
	var n json.Number
	if json.Unmarshal(o[k], &n) != nil {
		return ""
	}
	s := n.String()
	if s == "" {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}
func (o object) obj(k string) object { v, _ := decode(o[k]); return v }
func (o object) list(k string) []object {
	var rows []map[string]json.RawMessage
	if json.Unmarshal(o[k], &rows) != nil {
		return nil
	}
	out := make([]object, len(rows))
	for i, v := range rows {
		out[i] = object(v)
	}
	return out
}
func check(o object, key string) error {
	n, ok := o.num(key)
	if !ok {
		return unknown()
	}
	if n != 0 {
		return fail("vendor_business_error", "unknown")
	}
	return nil
}
func raw(v any) []byte       { b, _ := json.Marshal(v); return b }
func decimal(n int64) string { return strconv.FormatInt(n, 10) }
func hmacSign(fields map[string]string, secret []byte, omit ...string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		skip := false
		for _, s := range omit {
			if k == s {
				skip = true
			}
		}
		if !skip {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fields[k])
	}
	h := hmac.New(sha256.New, secret)
	_, _ = h.Write([]byte(strings.Join(parts, "&")))
	return hex.EncodeToString(h.Sum(nil))
}
func copyFields(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (c *Client) send(ctx context.Context, endpoint, method string, class transport.Class, op string, header http.Header, body io.ReadCloser, size int64, grant *transport.Grant) (object, error) {
	if body != nil {
		defer body.Close()
	}
	if c.Wire == nil || origins[c.Channel] == "" {
		if body != nil {
			body.Close()
		}
		return nil, fail("invalid_client", "not_sent")
	}
	b, err := c.Wire.Send(ctx, transport.Request{URL: endpoint, Method: method, Class: class, OperationID: op, Headers: header, Body: body, ContentLength: size, Grant: grant})
	if err != nil {
		return nil, err
	}
	return decode(b)
}
func (c *Client) form(ctx context.Context, path string, class transport.Class, op string, fields map[string]string) (object, error) {
	values := url.Values{}
	for k, v := range fields {
		values.Set(k, v)
	}
	b := []byte(values.Encode())
	return c.send(ctx, origins[c.Channel]+path, "POST", class, op, http.Header{"Content-Type": []string{"application/x-www-form-urlencoded; charset=utf-8"}}, io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil)
}
func (c *Client) bearer() http.Header {
	h := http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}
	if c.Auth.Token != nil {
		h.Set("Authorization", "Bearer "+string(c.Auth.Token.Bytes()))
	}
	return h
}
func (c *Client) json(ctx context.Context, path, method string, class transport.Class, op string, v any) (object, error) {
	var b []byte
	var body io.ReadCloser
	if v != nil {
		b = raw(v)
		body = io.NopCloser(bytes.NewReader(b))
	}
	return c.send(ctx, origins[c.Channel]+path, method, class, op, c.bearer(), body, int64(len(b)), nil)
}

// File refers to an already frozen artifact. Every send rechecks length and both
// digests. A caller cannot substitute an unverified path through exported fields.
type File struct {
	path, name, sha256, md5 string
	size                    int64
}

func NewFile(path string) (File, error) {
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return File{}, fail("artifact_unavailable", "not_sent")
	}
	f, n, err := transport.OpenUpload(path)
	if err != nil {
		return File{}, fail("artifact_unavailable", "not_sent")
	}
	defer f.Close()
	if n <= 0 {
		return File{}, fail("empty_artifact", "not_sent")
	}
	s, m := sha256.New(), md5.New()
	if _, err = io.Copy(io.MultiWriter(s, m), f); err != nil {
		return File{}, fail("artifact_unavailable", "not_sent")
	}
	name := filepath.Base(path)
	if name == "." || strings.ContainsAny(name, "\r\n") {
		return File{}, fail("invalid_filename", "not_sent")
	}
	return File{path, name, hex.EncodeToString(s.Sum(nil)), hex.EncodeToString(m.Sum(nil)), n}, nil
}

// NewNamedFile preserves the wire filename when a frozen content-addressed
// artifact is stored as "blob". The name is public intent, not a filesystem path.
func NewNamedFile(path, name string) (File, error) {
	if name == "" || name == "." || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\r\n") || !utf8.ValidString(name) {
		return File{}, fail("invalid_filename", "not_sent")
	}
	f, err := NewFile(path)
	if err != nil {
		return File{}, err
	}
	f.name = name
	return f, nil
}
func (f File) SHA256() string { return f.sha256 }
func (f File) MD5() string    { return f.md5 }
func (f File) Name() string   { return f.name }
func (f File) Size() int64    { return f.size }
func (f File) open() (io.ReadCloser, error) {
	r, n, err := transport.OpenUpload(f.path)
	if err != nil {
		return nil, fail("artifact_unavailable", "not_sent")
	}
	defer r.Close()
	if n != f.size {
		return nil, fail("artifact_changed", "not_sent")
	}
	// Verify an anonymous private copy: an open source inode can still change
	// before HTTP consumes it. Immediate unlink also prevents crash leftovers.
	snapshot, err := os.CreateTemp("", "publisher-upload-*")
	if err != nil {
		return nil, fail("artifact_snapshot_unavailable", "not_sent")
	}
	if err = os.Remove(snapshot.Name()); err != nil {
		snapshot.Close()
		return nil, fail("artifact_snapshot_unavailable", "not_sent")
	}
	s, m := sha256.New(), md5.New()
	copied, err := io.Copy(io.MultiWriter(snapshot, s, m), io.LimitReader(r, f.size+1))
	if err != nil || copied != f.size || hex.EncodeToString(s.Sum(nil)) != f.sha256 || hex.EncodeToString(m.Sum(nil)) != f.md5 {
		snapshot.Close()
		return nil, fail("artifact_changed", "not_sent")
	}
	if _, err = snapshot.Seek(0, io.SeekStart); err != nil {
		snapshot.Close()
		return nil, fail("artifact_unavailable", "not_sent")
	}
	return &readSnapshot{file: snapshot}, nil
}

type readSnapshot struct{ file *os.File }

func (r *readSnapshot) Read(p []byte) (int, error) { return r.file.Read(p) }
func (r *readSnapshot) Close() error               { return r.file.Close() }
func (*readSnapshot) String() string               { return "[REDACTED upload snapshot]" }

type multiBody struct {
	io.Reader
	closers []io.Closer
}

func (b *multiBody) Close() error {
	for _, c := range b.closers {
		_ = c.Close()
	}
	return nil
}
func multipartBody(fields map[string]string, files map[string]File) (io.ReadCloser, int64, string, error) {
	w := multipart.NewWriter(io.Discard)
	boundary := w.Boundary()
	var readers []io.Reader
	var closers []io.Closer
	var size int64
	add := func(b []byte) { readers = append(readers, bytes.NewReader(b)); size += int64(len(b)) }
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		add([]byte("--" + boundary + "\r\nContent-Disposition: " + mime.FormatMediaType("form-data", map[string]string{"name": k}) + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + fields[k] + "\r\n"))
	}
	keys = keys[:0]
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := files[k]
		r, err := f.open()
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, 0, "", err
		}
		closers = append(closers, r)
		add([]byte("--" + boundary + "\r\nContent-Disposition: " + mime.FormatMediaType("form-data", map[string]string{"name": k, "filename": f.name}) + "\r\nContent-Type: application/octet-stream\r\n\r\n"))
		readers = append(readers, r)
		size += f.size
		add([]byte("\r\n"))
	}
	add([]byte("--" + boundary + "--\r\n"))
	return &multiBody{io.MultiReader(readers...), closers}, size, "multipart/form-data; boundary=" + boundary, nil
}
func (c *Client) multipart(ctx context.Context, path string, class transport.Class, op string, fields map[string]string, files map[string]File, headers http.Header) (object, error) {
	body, size, contentType, err := multipartBody(fields, files)
	if err != nil {
		return nil, err
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", contentType)
	return c.send(ctx, origins[c.Channel]+path, "POST", class, op, headers, body, size, nil)
}

// UploadGrant is secret material: store in Keychain, never in journal receipts.
type UploadGrant struct {
	URL, Ref           string
	ExpiresAt, ReadyAt time.Time
	Method             string
	Headers            http.Header
}

func (UploadGrant) String() string                 { return "[REDACTED upload grant]" }
func (g UploadGrant) GoString() string             { return g.String() }
func (g UploadGrant) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED upload grant]"`), nil }
func allowedUpload(channel, endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch channel {
	case "tencent":
		return strings.HasSuffix(host, ".myqcloud.com") && strings.Contains(host, ".cos.")
	case "huawei":
		return host == "connect-api.cloud.huawei.com" || host == "nsp-appgallery-agcfs-drcn.obs.cn-north-2.myhuaweicloud.cn" || strings.HasSuffix(host, ".dbankcdn.com")
	case "oppo":
		return host == "api.open.oppomobile.com" || host == "oop-openapi-cn.heytapmobi.com" || strings.HasSuffix(host, ".heytapmobi.com")
	case "honor":
		return host == "appmarket-openapi-drcn.cloud.honor.com"
	}
	return false
}

func (c *Client) uploadRaw(ctx context.Context, op string, g UploadGrant, file File) error {
	if c.Wire == nil {
		return fail("invalid_client", "not_sent")
	}
	if !allowedUpload(c.Channel, g.URL) || g.Ref == "" || g.Method != "PUT" || !g.ExpiresAt.After(c.now()) {
		return fail("upload_grant_invalid", "not_sent")
	}
	body, err := file.open()
	if err != nil {
		return err
	}
	defer body.Close()
	headers := g.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Del("Host")
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/octet-stream")
	}
	_, err = c.Wire.Send(ctx, transport.Request{URL: g.URL, Method: g.Method, Class: transport.Write, OperationID: op, Headers: headers, Body: body, ContentLength: file.size, Grant: &transport.Grant{URL: g.URL, Method: g.Method, ExpiresAt: g.ExpiresAt, Headers: g.Headers.Clone()}})
	return err
}

// Acceptance is deliberately distinct from review and availability. RefKind
// identifies an actual vendor reference or a local response acknowledgement.
type Acceptance struct{ Ref, RefKind string }
type Details struct {
	PackageName, AppID, VersionName string
	VersionCode                     int64
	Metadata                        map[string]string
	UpdateAllowed                   bool // Xiaomi's explicit updateVersion permission
}
type Status struct {
	VersionCode               int64
	Ref, Review, Availability string
	Context                   *protocol.ChannelContext
}

func (c *Client) channel(want string) error {
	if c.Channel != want {
		return fail("wrong_channel", "not_sent")
	}
	return nil
}
func required(values ...string) error {
	for _, v := range values {
		if v == "" {
			return fail("missing_required_input", "not_sent")
		}
	}
	return nil
}
func ack(op string) Acceptance { return Acceptance{"ack-" + op, "local_acknowledgement"} }
func numberID(s string) (json.Number, error) {
	if s == "" {
		return "", fail("invalid_id", "not_sent")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", fail("invalid_id", "not_sent")
		}
	}
	return json.Number(s), nil
}
func (a Acceptance) String() string { return fmt.Sprintf("acceptance (%s)", a.RefKind) }
