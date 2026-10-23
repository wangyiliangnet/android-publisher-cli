package cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/cli"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type watchFixture struct {
	root, id string
	server   *httptest.Server
	queries  atomic.Int64
	writes   atomic.Int64
	args     []string
}

func newWatchFixture(t *testing.T) *watchFixture {
	t.Helper()
	base := t.TempDir()
	fixture := filepath.Join(base, "fixture")
	invoke(t, 0, "simulate", "init", "--dir", fixture)
	f := &watchFixture{root: filepath.Join(base, "client")}
	f.args = []string{"simulate", "--data-dir", f.root}
	var planned engine.Summary
	if err := json.Unmarshal(invoke(t, 0, f.command("plan", "--manifest", filepath.Join(fixture, "manifest.yaml"))...), &planned); err != nil {
		t.Fatal(err)
	}
	f.id = planned.ReleaseID
	store, err := simulation.New(filepath.Join(base, "remote"), simulation.Options{Review: "approved", Availability: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/observe" {
			f.queries.Add(1)
		}
		if r.URL.Path == "/execute" {
			f.writes.Add(1)
		}
		store.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { f.server.Close(); store.Close() })
	invoke(t, 0, f.command("preflight", "--release", f.id, "--store", f.server.URL)...)
	invoke(t, 0, f.command("submit", "--release", f.id, "--store", f.server.URL)...)
	return f
}

func (f *watchFixture) command(args ...string) []string {
	return append(append([]string{}, f.args...), args...)
}

type watchFrame struct {
	engine.Summary
	Sequence   int    `json:"sequence"`
	Phase      string `json:"phase"`
	Effect     string `json:"effect"`
	NextAction string `json:"next_action"`
}

func decodeFrames(t *testing.T, data []byte) []watchFrame {
	t.Helper()
	frames := []watchFrame{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var frame watchFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

func TestWatchDurableScheduleAndReadOnlyNDJSON(t *testing.T) {
	f := newWatchFixture(t)
	writes := f.writes.Load()
	frames := decodeFrames(t, invoke(t, 0, f.command("watch", "--release", f.id, "--store", f.server.URL, "--interval", "100ms", "--max-polls", "3")...))
	if len(frames) != 3 || f.queries.Load() != 6 || f.writes.Load() != writes {
		t.Fatalf("frames=%d reads=%d publishing writes=%d -> %d", len(frames), f.queries.Load(), writes, f.writes.Load())
	}
	for i, frame := range frames {
		if frame.Sequence != i+1 || frame.SchemaVersion != 1 || frame.Phase != "observation" || frame.Effect != "read_only" || frame.NextAction != "wait_next_check" || !frame.AllApproved || frame.AllLive {
			t.Fatalf("invalid watch frame: %+v", frame)
		}
		for _, target := range frame.Targets {
			if target.Generation != 1 || target.LastSuccessAt == "" || target.NextCheckAt == "" {
				t.Fatalf("watch ignored the persisted next_check_at: %+v", target)
			}
		}
	}
}

func TestWatchOfflinePreservesSuccessAndFailureHealthAcrossSkippedCycles(t *testing.T) {
	f := newWatchFixture(t)
	var good engine.Summary
	if err := json.Unmarshal(invoke(t, 0, f.command("status", "--refresh", "--release", f.id, "--store", f.server.URL)...), &good); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(f.root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE targets SET next_check_at=''"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	f.server.Close()
	frames := decodeFrames(t, invoke(t, 3, f.command("watch", "--release", f.id, "--store", f.server.URL, "--interval", "100ms", "--max-polls", "2")...))
	if len(frames) != 2 {
		t.Fatalf("got %d frames", len(frames))
	}
	for _, frame := range frames {
		if !frame.AllApproved || frame.AllLive || len(frame.Errors) != 6 || frame.NextAction != "retry_read_after_next_check" {
			t.Fatalf("query failure changed accepted state or disappeared while waiting: %+v", frame)
		}
		for i, target := range frame.Targets {
			if target.LastSuccessAt != good.Targets[i].LastSuccessAt || target.Review != "approved" || target.Availability != "scheduled" || target.QueryFailures != 1 || target.Generation != 2 {
				t.Fatalf("lost successful evidence or ignored backoff: %+v", target)
			}
		}
	}
}

type signaledWriter struct {
	once sync.Once
	line chan struct{}
}

func (s *signaledWriter) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.line) })
	return len(p), nil
}

func TestWatchCancellationAndSleepReleaseWriterLock(t *testing.T) {
	f := newWatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &signaledWriter{line: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, f.command("watch", "--release", f.id, "--store", f.server.URL, "--interval", "1h"), out, io.Discard)
	}()
	select {
	case <-out.line:
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not publish its first poll")
	}
	s, err := state.Open(f.root, manifest.Simulation, false)
	if err != nil {
		t.Fatalf("watch retained its writer lock during sleep: %v", err)
	}
	s.Close()
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("interrupted watch returned %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch did not respond to cancellation")
	}
}

func TestObservationFlagsFailBeforeOpeningState(t *testing.T) {
	base := t.TempDir()
	worker := filepath.Join(base, "worker.js")
	if err := os.WriteFile(worker, []byte("// unused"), 0600); err != nil {
		t.Fatal(err)
	}
	wrongNode := filepath.Join(base, "node")
	if err := os.WriteFile(wrongNode, []byte("#!/bin/sh\nprintf 'v20.20.1\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{"watch", "--release", "unused"},
		{"simulate", "plan", "--manifest", "unused", "--observer", "browser"},
		{"simulate", "status", "--release", "unused", "--observer", "browser"},
		{"simulate", "submit", "--release", "unused", "--observer", "browser"},
		{"simulate", "watch", "--release", "unused", "--store", "http://127.0.0.1:1", "--interval", "1ms"},
		{"simulate", "watch", "--release", "unused", "--store", "http://127.0.0.1:1", "--max-polls", "-1"},
		{"simulate", "watch", "--release", "unused", "--store", "http://127.0.0.1:1", "--refresh"},
		{"simulate", "watch", "--release", "unused", "--store", "http://127.0.0.1:1", "--node", wrongNode},
		{"simulate", "status", "--refresh", "--release", "unused", "--observer", "browser", "--store", "https://example.com", "--node", wrongNode, "--browser-worker", worker},
		{"simulate", "status", "--refresh", "--release", "unused", "--observer", "browser", "--store", "http://localhost:80", "--node", wrongNode, "--browser-worker", worker},
		{"simulate", "status", "--refresh", "--release", "unused", "--observer", "browser", "--store", "http://127.0.0.1:1", "--node", "node", "--browser-worker", worker},
		{"simulate", "status", "--refresh", "--release", "unused", "--observer", "browser", "--store", "http://127.0.0.1:1", "--node", wrongNode, "--browser-worker", worker},
	}
	for i, command := range cases {
		root := filepath.Join(base, "state", string(rune('a'+i)))
		args := []string{"--data-dir", root}
		if command[0] == "simulate" {
			args = append([]string{"simulate"}, args...)
			command = command[1:]
		}
		invoke(t, 2, append(args, command...)...)
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatalf("rejected observation flags touched state: %v", command)
		}
	}
}

func TestBrowserCLIUsesFrozenIdentityAndRejectsConflictingReceipt(t *testing.T) {
	f := newWatchFixture(t)
	base := t.TempDir()
	capture := filepath.Join(base, "requests.ndjson")
	if err := os.WriteFile(capture, nil, 0600); err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(base, "worker.js")
	if err := os.WriteFile(worker, []byte("// process bridge fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	// The fake executable exposes the exact frozen bridge input, then returns a
	// fixed login-needed result. It never launches a browser or reaches a store.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	node := filepath.Join(base, "node")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf 'v24.22.0\\n'; exit 0; fi\n/bin/cat >> " + quote(capture) + "\nprintf '\\n' >> " + quote(capture) + "\nprintf '{\"schema_version\":1,\"mode\":\"simulation\",\"status\":\"auth_required\"}\\n'\n"
	if err := os.WriteFile(node, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	command := f.command("status", "--refresh", "--release", f.id, "--store", f.server.URL, "--observer", "browser", "--node", node, "--browser-worker", worker)
	writes := f.writes.Load()
	var login engine.Summary
	if err := json.Unmarshal(invoke(t, 4, command...), &login); err != nil {
		t.Fatal(err)
	}
	if len(login.ObservationStatuses) != 6 {
		t.Fatal("browser login state was not exposed")
	}
	for _, status := range login.ObservationStatuses {
		if status.Status != "auth_required" || status.NextAction != "login_required" {
			t.Fatalf("unsafe or missing actionable browser status: %+v", status)
		}
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(f.root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Load(f.id)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.Targets(f.id)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	want := map[string]state.Target{}
	for _, target := range targets {
		want[target.Scope] = target
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	seen := map[string]bool{}
	for scanner.Scan() {
		var request struct {
			SchemaVersion int    `json:"schema_version"`
			Mode          string `json:"mode"`
			Target        struct {
				Channel        string `json:"channel"`
				Account        string `json:"account"`
				AppID          string `json:"app_id"`
				Scope          string `json:"scope"`
				VersionCode    string `json:"version_code"`
				RemoteRef      string `json:"remote_ref"`
				PackageName    string `json:"package_name"`
				IntentDigest   string `json:"intent_digest"`
				AdapterBinding string `json:"adapter_binding"`
			} `json:"target"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		target := request.Target
		expected, ok := want[target.Scope]
		identity := snapshot.Manifest.Targets[expected.Channel]
		if !ok || seen[target.Scope] || request.SchemaVersion != 1 || request.Mode != manifest.Simulation || target.Channel != expected.Channel || target.Account != identity.AccountRef || target.AppID != identity.AppRef || target.VersionCode != "1" || target.RemoteRef != expected.RemoteRef || target.PackageName != snapshot.Manifest.PackageName || target.IntentDigest != snapshot.Digest || target.AdapterBinding != "" {
			t.Fatalf("worker was not bound to the frozen target: %s", scanner.Bytes())
		}
		seen[target.Scope] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 || f.queries.Load() != 0 || f.writes.Load() != writes {
		t.Fatalf("browser CLI used the wrong observation or publication path: seen=%d API reads=%d writes=%d", len(seen), f.queries.Load(), f.writes.Load())
	}
	// A skipped poll keeps query health failed, without repeating a stale
	// login-required classification as if another page had just been read.
	watchCommand := f.command("watch", "--release", f.id, "--store", f.server.URL, "--observer", "browser", "--node", node, "--browser-worker", worker, "--max-polls", "1")
	stale := decodeFrames(t, invoke(t, 3, watchCommand...))
	if len(stale) != 1 || len(stale[0].ObservationStatuses) != 0 || len(stale[0].Errors) != 6 {
		t.Fatal("a skipped poll invented a fresh browser status or lost failed query health")
	}
	s, err = state.Open(f.root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE targets SET next_check_at='' WHERE channel='honor'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	live := decodeFrames(t, invoke(t, 4, watchCommand...))
	if len(live) != 1 || live[0].ObservationStatuses["honor"].Status != "auth_required" || live[0].NextAction != "login_required" {
		t.Fatal("watch did not return login-required status and exit code 4")
	}
	data, err = os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	s, err = state.Open(f.root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE targets SET remote_ref='conflicting-final-receipt' WHERE channel='honor'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	invoke(t, 2, command...)
	after, err := os.ReadFile(capture)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("conflicting final receipt reached the browser worker")
	}
}
