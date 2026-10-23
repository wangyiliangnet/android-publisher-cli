// Package browser owns a bounded, read-only browser worker. Its first delivery is
// deliberately restricted to local simulation fixtures, never a vendor portal.
package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

const (
	maxOutput = 64 << 10
	maxText   = 64 << 10
	maxPNG    = 4 << 20
)

// StatusError carries an actionable, fixed status only. Worker stderr, page
// contents, file paths and subprocess errors never enter its public text.
type StatusError struct{ Code string }

func (e *StatusError) Error() string             { return "browser observation: " + e.Code }
func (e *StatusError) ObservationStatus() string { return e.Code }
func fail(code string) error                     { return &StatusError{Code: code} }

type Config struct {
	Root        string
	NodePath    string
	WorkerPath  string
	Origin      string
	Simulation  bool
	Coordinator *account.Coordinator
	Timeout     time.Duration
}

type Runner struct {
	config      Config
	origin      string
	coordinator account.Coordinator
}

// Target is constructed from the frozen manifest, preflight and submission
// receipt. A page cannot change any of these expected identifiers.
type Target struct {
	Channel        string `json:"channel"`
	Account        string `json:"account"`
	AppID          string `json:"app_id"`
	Scope          string `json:"scope"`
	PackageName    string `json:"package_name"`
	VersionCode    int64  `json:"-"`
	RemoteRef      string `json:"remote_ref"`
	AdapterBinding string `json:"adapter_binding"`
	IntentDigest   string `json:"intent_digest"`
}

type workerTarget struct {
	Target
	VersionCode string `json:"version_code"`
}
type workerRequest struct {
	SchemaVersion int          `json:"schema_version"`
	Mode          string       `json:"mode"`
	Target        workerTarget `json:"target"`
	URL           string       `json:"url"`
	ProfileDir    string       `json:"profile_dir"`
	EvidenceDir   string       `json:"evidence_dir"`
	TimeoutMS     int64        `json:"timeout_ms"`
}
type workerObservation struct {
	Scope          string `json:"scope"`
	AdapterBinding string `json:"adapter_binding"`
	VersionCode    string `json:"version_code"`
	RemoteRef      string `json:"remote_ref"`
	Review         string `json:"review_state"`
	Availability   string `json:"availability_state"`
	Source         string `json:"source"`
	CollectedAt    string `json:"collected_at"`
	SubmittedAt    string `json:"submitted_at"`
	URL            string `json:"url"`
}
type workerEvidence struct {
	TextSHA256 string `json:"text_sha256"`
	PNGSHA256  string `json:"png_sha256"`
	TextBytes  int64  `json:"text_bytes"`
	PNGBytes   int64  `json:"png_bytes"`
}
type workerResult struct {
	SchemaVersion int                `json:"schema_version"`
	Mode          string             `json:"mode"`
	Status        string             `json:"status"`
	Observation   *workerObservation `json:"observation,omitempty"`
	Evidence      *workerEvidence    `json:"evidence,omitempty"`
}
type proofObserved struct {
	Channel          string `json:"channel"`
	Account          string `json:"account"`
	AppID            string `json:"app_id"`
	Scope            string `json:"scope"`
	PackageName      string `json:"package_name"`
	VersionCode      string `json:"version_code"`
	RemoteRef        string `json:"remote_ref"`
	Review           string `json:"review_state"`
	Availability     string `json:"availability_state"`
	ReviewText       string `json:"review_text"`
	AvailabilityText string `json:"availability_text"`
	SubmittedAt      string `json:"submitted_at"`
}
type proof struct {
	SchemaVersion int           `json:"schema_version"`
	Mode          string        `json:"mode"`
	Kind          string        `json:"kind"`
	Target        workerTarget  `json:"target"`
	Observed      proofObserved `json:"observed"`
	CollectedAt   string        `json:"collected_at"`
	URL           string        `json:"url"`
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var utcTimePattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$`)
var reviewLabels = map[string]string{"待审核": "pending", "审核中": "in_review", "审核通过": "approved", "审核未通过": "rejected", "已撤回": "withdrawn"}
var availabilityLabels = map[string]string{"待上架": "not_observed_live", "定时发布": "scheduled", "已上线": "live", "已被新版本替代": "superseded"}

func New(c Config) (*Runner, error) {
	if !c.Simulation {
		return nil, fail("production_disabled")
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Timeout < time.Millisecond || c.Timeout > 30*time.Second || !filepath.IsAbs(c.Root) || filepath.Clean(c.Root) != c.Root || c.Root == string(filepath.Separator) {
		return nil, fail("invalid_input")
	}
	for _, path := range []string{c.NodePath, c.WorkerPath} {
		if !filepath.IsAbs(path) {
			return nil, fail("browser_unavailable")
		}
		f, err := privatefs.Open(path, os.O_RDONLY)
		if err != nil {
			return nil, fail("browser_unavailable")
		}
		f.Close()
	}
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Opaque != "" {
		return nil, fail("invalid_input")
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || n < 1 || n > 65535 || (host != "127.0.0.1" && host != "::1") {
		return nil, fail("invalid_input")
	}
	c.Root, err = canonicalRoot(c.Root)
	if err != nil || secureDir(c.Root) != nil {
		return nil, fail("storage_unavailable")
	}
	coord := account.Default()
	if c.Coordinator != nil {
		coord = *c.Coordinator
	}
	// The account coordinator itself predates this worker. Reject symlinked
	// ancestors before it creates or opens its shared lock files.
	coord.Root, err = canonicalRoot(coord.Root)
	if err != nil || secureDir(coord.Root) != nil {
		return nil, fail("storage_unavailable")
	}
	return &Runner{config: c, origin: "http://" + u.Host, coordinator: coord}, nil
}

func (t Target) valid() bool {
	if !(account.Scope{Channel: t.Channel, Account: t.Account}).Valid() || !safeText(t.AppID, 256) || !packagePattern.MatchString(t.PackageName) || t.VersionCode <= 0 || !safeText(t.RemoteRef, 2048) || !hashPattern.MatchString(t.IntentDigest) || (t.AdapterBinding != "" && !hashPattern.MatchString(t.AdapterBinding)) {
		return false
	}
	return t.Scope == protocol.Digest([]string{t.Channel, t.Account, t.AppID, t.PackageName})
}

func safeText(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// Collect holds both the profile lock and the installation-wide account lease
// through browser closure and evidence verification. It never modifies a store.
func (r *Runner) Collect(ctx context.Context, t Target) (protocol.Observation, error) {
	if r == nil || !t.valid() {
		return protocol.Observation{}, fail("invalid_input")
	}
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return protocol.Observation{}, contextStatus(err)
	}
	profileRoot := filepath.Join(r.config.Root, "browser", t.Channel, account.Key(t.Account))
	profileDir := filepath.Join(profileRoot, "profile")
	if ensureManagedDir(r.config.Root, profileDir) != nil {
		return protocol.Observation{}, fail("storage_unavailable")
	}
	lock, err := privatefs.Open(filepath.Join(profileRoot, "worker.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return protocol.Observation{}, fail("storage_unavailable")
	}
	defer lock.Close()
	if lock.Chmod(0600) != nil {
		return protocol.Observation{}, fail("storage_unavailable")
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) {
			return protocol.Observation{}, fail("profile_busy")
		}
		return protocol.Observation{}, fail("storage_unavailable")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var observation protocol.Observation
	err = r.coordinator.With(ctx, account.Scope{Channel: t.Channel, Account: t.Account}, func(lease *account.Lease) error {
		var inner error
		observation, inner = r.collectLocked(lease.Context(), t, profileDir)
		return inner
	})
	if err != nil {
		var status *StatusError
		if errors.As(err, &status) {
			return protocol.Observation{}, status
		}
		if ctx.Err() != nil {
			return protocol.Observation{}, contextStatus(ctx.Err())
		}
		return protocol.Observation{}, fail("storage_unavailable")
	}
	return observation, nil
}

func (r *Runner) collectLocked(ctx context.Context, t Target, profileDir string) (protocol.Observation, error) {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return protocol.Observation{}, fail("storage_unavailable")
	}
	dir := filepath.Join(r.config.Root, "evidence", hex.EncodeToString(nonce[:]))
	if ensureManagedDir(r.config.Root, dir) != nil {
		return protocol.Observation{}, fail("storage_unavailable")
	}
	urlValue := r.origin + "/browser/" + t.Channel + "?scope=" + t.Scope
	remaining := r.config.Timeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if remaining < time.Millisecond {
		return protocol.Observation{}, fail("timed_out")
	}
	request := workerRequest{SchemaVersion: 1, Mode: "simulation", Target: workerTarget{Target: t, VersionCode: strconv.FormatInt(t.VersionCode, 10)}, URL: urlValue, ProfileDir: profileDir, EvidenceDir: dir, TimeoutMS: remaining.Milliseconds()}
	data, err := json.Marshal(request)
	if err != nil {
		return protocol.Observation{}, fail("invalid_input")
	}
	// Both processes use this Mac's wall clock. The worker's collection time
	// must fall within this invocation, with one second of clock tolerance;
	// replacing an old page proof's clock with Now would make stale evidence
	// look fresh in the persistent observation projection.
	collectionStart := time.Now().UTC()
	output, err := r.run(ctx, data)
	collectionEnd := time.Now().UTC()
	if err != nil {
		return protocol.Observation{}, err
	}
	var result workerResult
	if strictJSON(output, &result) != nil || result.SchemaVersion != 1 || result.Mode != "simulation" {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	if result.Status != "observed" {
		if result.Observation != nil || result.Evidence != nil {
			return protocol.Observation{}, fail("evidence_insufficient")
		}
		switch result.Status {
		case "auth_required", "profile_busy", "evidence_insufficient", "blocked_navigation", "timed_out", "canceled", "invalid_input", "browser_unavailable":
			return protocol.Observation{}, fail(result.Status)
		default:
			return protocol.Observation{}, fail("evidence_insufficient")
		}
	}
	o := result.Observation
	if o == nil || result.Evidence == nil || o.Scope != t.Scope || o.AdapterBinding != t.AdapterBinding || o.VersionCode != strconv.FormatInt(t.VersionCode, 10) || o.RemoteRef != t.RemoteRef || o.Source != "browser_fixture" || o.URL != r.origin+"/browser/"+t.Channel {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	submittedAt, err := time.Parse(time.RFC3339Nano, o.SubmittedAt)
	if err != nil || !utcTimePattern.MatchString(o.SubmittedAt) || submittedAt.IsZero() {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	collectedAt, err := time.Parse(time.RFC3339Nano, o.CollectedAt)
	if err != nil || !utcTimePattern.MatchString(o.CollectedAt) || collectedAt.IsZero() || collectedAt.Before(collectionStart.Add(-time.Second)) || collectedAt.After(collectionEnd.Add(time.Second)) || submittedAt.After(collectedAt.Add(30*time.Second)) {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	observation := protocol.Observation{Scope: t.Scope, AdapterBinding: t.AdapterBinding, VersionCode: t.VersionCode, RemoteRef: t.RemoteRef, Review: o.Review, Availability: o.Availability, Source: "browser_fixture", CollectedAt: collectedAt.UTC()}
	if observation.Validate(t.Scope, t.VersionCode, t.RemoteRef) != nil {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	evidence, err := r.evidence(dir, *result.Evidence, t, *o)
	if err != nil {
		return protocol.Observation{}, err
	}
	observation.Evidence = evidence
	return observation, nil
}

func (r *Runner) evidence(dir string, e workerEvidence, t Target, o workerObservation) ([]protocol.Evidence, error) {
	files := []struct {
		kind, name, hash string
		size, limit      int64
	}{{"text", "evidence.json", e.TextSHA256, e.TextBytes, maxText}, {"png", "evidence.png", e.PNGSHA256, e.PNGBytes, maxPNG}}
	refs := make([]protocol.Evidence, 0, 2)
	for _, file := range files {
		if !hashPattern.MatchString(file.hash) || file.size <= 0 || file.size > file.limit || managedDirSafe(r.config.Root, dir) != nil {
			return nil, fail("evidence_insufficient")
		}
		path := filepath.Join(dir, file.name)
		f, err := privatefs.Open(path, os.O_RDONLY)
		if err != nil {
			return nil, fail("evidence_insufficient")
		}
		info, err := f.Stat()
		if err != nil || info.Size() != file.size || info.Mode().Perm() != 0600 {
			f.Close()
			return nil, fail("evidence_insufficient")
		}
		data, err := io.ReadAll(io.LimitReader(f, file.limit+1))
		f.Close()
		if err != nil || int64(len(data)) != file.size {
			return nil, fail("evidence_insufficient")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != file.hash {
			return nil, fail("evidence_insufficient")
		}
		if file.kind == "text" && !validProof(data, t, o) {
			return nil, fail("evidence_insufficient")
		}
		if file.kind == "png" && (len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n") {
			return nil, fail("evidence_insufficient")
		}
		if file.kind == "png" {
			config, err := png.DecodeConfig(bytes.NewReader(data))
			if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 8<<20 {
				return nil, fail("evidence_insufficient")
			}
			if _, err := png.Decode(bytes.NewReader(data)); err != nil {
				return nil, fail("evidence_insufficient")
			}
		}
		rel, err := filepath.Rel(r.config.Root, path)
		if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return nil, fail("evidence_insufficient")
		}
		refs = append(refs, protocol.Evidence{Kind: file.kind, Path: filepath.ToSlash(rel), SHA256: file.hash, Size: file.size})
	}
	return refs, nil
}

func validProof(data []byte, t Target, o workerObservation) bool {
	var p proof
	expected := t
	expected.VersionCode = 0
	if strictJSON(data, &p) != nil || p.SchemaVersion != 1 || p.Mode != "simulation" || p.Kind != "sanitized_browser_evidence" || p.Target.Target != expected || p.Target.VersionCode != o.VersionCode || p.CollectedAt != o.CollectedAt || p.URL != o.URL {
		return false
	}
	v := p.Observed
	return v.Channel == t.Channel && v.Account == t.Account && v.AppID == t.AppID && v.Scope == t.Scope && v.PackageName == t.PackageName && v.VersionCode == o.VersionCode && v.RemoteRef == t.RemoteRef && v.Review == o.Review && v.Availability == o.Availability && v.SubmittedAt == o.SubmittedAt && reviewLabels[v.ReviewText] == v.Review && availabilityLabels[v.AvailabilityText] == v.Availability
}

func contextStatus(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fail("timed_out")
	}
	return fail("canceled")
}

// boundedOutput keeps draining until the child closes its pipe, without ever
// allocating an unbounded page-controlled response or retaining stderr.
type boundedOutput struct {
	data     []byte
	overflow bool
	mu       sync.Mutex
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := maxOutput - len(b.data)
	if len(p) > left {
		b.overflow = true
		p = p[:left]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (r *Runner) run(ctx context.Context, data []byte) ([]byte, error) {
	readLife, writeLife, err := os.Pipe()
	if err != nil {
		return nil, fail("browser_unavailable")
	}
	defer readLife.Close()
	defer writeLife.Close()
	cmd := exec.Command(r.config.NodePath, r.config.WorkerPath)
	cmd.Stdin = strings.NewReader(string(data))
	cmd.ExtraFiles = []*os.File{readLife}
	cmd.Env = []string{"PATH=" + filepath.Dir(r.config.NodePath) + ":/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "TMPDIR=" + os.TempDir(), "LANG=C.UTF-8", "NO_COLOR=1"}
	if cache := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); filepath.IsAbs(cache) && filepath.Clean(cache) == cache {
		cmd.Env = append(cmd.Env, "PLAYWRIGHT_BROWSERS_PATH="+cache)
	}
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	// If a defective child leaves inherited stdout open, Wait still ends. Normal
	// shutdown relies on the worker to close only the browser it created.
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fail("browser_unavailable")
	}
	readLife.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return nil, contextStatus(ctx.Err())
		}
		if output.overflow || err != nil {
			return nil, fail("browser_unavailable")
		}
		return output.data, nil
	case <-ctx.Done():
		writeLife.Close()
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
		}
		return nil, contextStatus(ctx.Err())
	}
}

func strictJSON(data []byte, v any) error {
	if !utf8.Valid(data) {
		return errors.New("invalid encoding")
	}
	tokens := json.NewDecoder(strings.NewReader(string(data)))
	if err := jsonValue(tokens, 0); err != nil {
		return err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	return nil
}
func jsonValue(d *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("nested response")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("duplicate field")
			}
			keys[name] = true
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := jsonValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = d.Token()
	return err
}

// secureAncestors rejects every symlink component, rather than accepting a
// private leaf beneath an attacker-selected profile or evidence parent.
func secureAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid path")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe directory")
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	return nil
}
func secureDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid path")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe directory")
		}
		if err := secureAncestors(path); err != nil {
			return err
		}
		return os.Chmod(path, 0700)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return errors.New("invalid parent")
	}
	if err := createParents(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return secureDir(path)
}
func ensureManagedDir(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("outside root")
	}
	if err := secureDir(root); err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		if err := secureDir(current); err != nil {
			return err
		}
	}
	return nil
}
func managedDirSafe(root, path string) error {
	if err := secureAncestors(path); err != nil {
		return err
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			return errors.New("unsafe directory mode")
		}
		if p == root {
			return nil
		}
		if parent := filepath.Dir(p); parent == p {
			return errors.New("outside root")
		}
	}
}
func createParents(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return secureAncestors(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return errors.New("invalid parent")
	}
	if err := createParents(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return secureAncestors(path)
}

// macOS exposes system aliases such as /var -> /private/var. Resolve the data
// root once; all later managed descendants are checked without following links.
func canonicalRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return "", errors.New("invalid root")
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("symlink root")
	}
	existing := path
	for {
		if _, err := os.Lstat(existing); err == nil {
			real, err := filepath.EvalSymlinks(existing)
			if err != nil {
				return "", err
			}
			rel, err := filepath.Rel(existing, path)
			if err != nil {
				return "", err
			}
			return filepath.Join(real, rel), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return "", errors.New("missing root")
		}
		existing = parent
	}
}

// Adapter exposes observation only. It cannot be used as a fallback publisher.
type Adapter struct {
	Runner  *Runner
	Targets map[string]Target
}

func (a *Adapter) Observe(ctx context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	if a == nil || a.Runner == nil {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	t, ok := a.Targets[scope]
	if !ok || t.VersionCode != version || t.RemoteRef != ref {
		return protocol.Observation{}, fail("evidence_insufficient")
	}
	return a.Runner.Collect(ctx, t)
}
func (a *Adapter) Inspect(context.Context, string) (protocol.RemoteSnapshot, error) {
	return protocol.RemoteSnapshot{}, fail("read_only")
}
func (a *Adapter) ExecuteOne(context.Context, protocol.Request) (protocol.Receipt, error) {
	return protocol.Receipt{}, fail("read_only")
}
func (a *Adapter) Reconcile(_ context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	return protocol.Reconciliation{OperationID: q.OperationID, RequestDigest: protocol.Digest(q), Result: "inconclusive"}, nil
}
