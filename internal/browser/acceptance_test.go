package browser_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/browser"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func browserRuntime(t *testing.T) (string, string) {
	t.Helper()
	node := os.Getenv("PUBLISHER_NODE")
	if node == "" {
		node, _ = exec.LookPath("node")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(repo, "browser", "dist", "worker.js")
	version, versionErr := exec.Command(node, "--version").Output()
	wanted, readErr := os.ReadFile(filepath.Join(repo, ".node-version"))
	_, workerErr := os.Stat(worker)
	if node == "" || versionErr != nil || readErr != nil || strings.TrimSpace(string(version)) != "v"+strings.TrimSpace(string(wanted)) || workerErr != nil {
		if os.Getenv("PUBLISHER_REQUIRE_BROWSER_TESTS") == "1" {
			t.Fatalf("required browser runtime unavailable: pinned Node and built worker needed (node=%q version=%q)", node, strings.TrimSpace(string(version)))
		}
		t.Skip("pinned Node and built Playwright worker required; set PUBLISHER_REQUIRE_BROWSER_TESTS=1 to make this mandatory")
	}
	node, err = filepath.Abs(node)
	if err != nil {
		t.Fatal(err)
	}
	return node, worker
}

type collectingAdapter struct {
	protocol.Adapter
	runner  *browser.Runner
	targets map[string]browser.Target
	mu      sync.Mutex
	seen    []protocol.Observation
}

func (a *collectingAdapter) Observe(ctx context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	target, ok := a.targets[scope]
	if !ok || target.VersionCode != version || target.RemoteRef != ref {
		return protocol.Observation{}, fmt.Errorf("unknown local target")
	}
	o, err := a.runner.Collect(ctx, target)
	if err == nil {
		a.mu.Lock()
		a.seen = append(a.seen, o)
		a.mu.Unlock()
	}
	return o, err
}

type acceptanceEnv struct {
	engine  *engine.Engine
	store   *state.Store
	fake    *simulation.Server
	server  *httptest.Server
	client  *simulation.Client
	id      string
	adapter *collectingAdapter
	mode    atomic.Int32
	config  browser.Config
}

func newAcceptance(t *testing.T) *acceptanceEnv {
	t.Helper()
	node, worker := browserRuntime(t)
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(base, "publisher"), manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	fake, err := simulation.New(filepath.Join(base, "fake"), simulation.Options{Review: "approved", Availability: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fake.Close() })
	v := &acceptanceEnv{store: s, fake: fake}
	v.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/browser/") || v.mode.Load() == 0 {
			fake.ServeHTTP(w, r)
			return
		}
		if v.mode.Load() == 5 {
			http.Error(w, "synthetic query unavailable", http.StatusServiceUnavailable)
			return
		}
		response := httptest.NewRecorder()
		fake.ServeHTTP(response, r)
		body := response.Body.String()
		switch v.mode.Load() {
		case 1:
			body = strings.ReplaceAll(body, `data-field="version-code">1</span>`, `data-field="version-code">0</span>`)
		case 2:
			body = strings.ReplaceAll(body, `data-field="reference">`, `data-field="reference">old-`)
		case 3:
			body = strings.ReplaceAll(body, "审核通过", "fixture-unknown-status")
		case 4:
			body = strings.ReplaceAll(body, `data-testid="release-row"`, `data-testid="not-a-release"`)
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		io.WriteString(w, body)
	}))
	t.Cleanup(v.server.Close)
	client, err := simulation.NewClient(v.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	v.client = client
	v.engine = &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: client}
	result, err := v.engine.Plan(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	v.id = result.ReleaseID
	if _, err = v.engine.Preflight(context.Background(), v.id, nil); err != nil {
		t.Fatal(err)
	}
	if result, err = v.engine.Submit(context.Background(), v.id, nil); err != nil || !result.AllAccepted {
		t.Fatalf("simulation submit: accepted=%d/%d errors=%v err=%v", result.Accepted, result.Total, result.Errors, err)
	}
	v.config = browser.Config{Root: s.Root, NodePath: node, WorkerPath: worker, Origin: v.server.URL, Simulation: true, Coordinator: &account.Coordinator{Root: filepath.Join(base, "accounts")}, Timeout: 20 * time.Second}
	runner, err := browser.New(v.config)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := s.Targets(v.id)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Load(v.id)
	if err != nil {
		t.Fatal(err)
	}
	a := &collectingAdapter{Adapter: client, runner: runner, targets: map[string]browser.Target{}}
	for _, target := range targets {
		identity := snapshot.Manifest.Targets[target.Channel]
		a.targets[target.Scope] = browser.Target{Channel: target.Channel, Account: identity.AccountRef, AppID: identity.AppRef, Scope: target.Scope, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, RemoteRef: target.RemoteRef, IntentDigest: snapshot.Digest}
	}
	v.adapter, v.engine.Adapter = a, a
	return v
}

func TestBrowserRealSixChannelSimulationAcceptance(t *testing.T) {
	v := newAcceptance(t)
	before, err := v.client.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := v.engine.Refresh(context.Background(), v.id, nil)
	if err != nil || !result.AllAccepted || !result.AllApproved || result.AllLive || result.Total != 6 {
		t.Fatalf("browser refresh: accepted=%d approved=%d live=%d total=%d errors=%v err=%v", result.Accepted, result.Approved, result.Live, result.Total, result.Errors, err)
	}
	if len(v.adapter.seen) != 6 {
		t.Fatalf("only %d channels observed", len(v.adapter.seen))
	}
	for _, o := range v.adapter.seen {
		if len(o.Evidence) != 2 {
			t.Fatalf("expected sanitized text and image evidence, got %+v", o.Evidence)
		}
		for _, evidence := range o.Evidence {
			path := evidence.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(v.store.Root, path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 || info.Size() != evidence.Size {
				t.Fatalf("unsafe evidence file: %+v %v", evidence, err)
			}
			hash := sha256.Sum256(data)
			if hex.EncodeToString(hash[:]) != evidence.SHA256 {
				t.Fatal("evidence bytes differ from recorded SHA256")
			}
			if bytes.Contains(data, []byte("fixture-secret-must-not-enter-evidence")) || bytes.Contains(data, []byte("scope=")) {
				t.Fatalf("raw page secret or query URL entered %s evidence", evidence.Kind)
			}
			if evidence.Kind == "png" {
				image, err := png.DecodeConfig(bytes.NewReader(data))
				if err != nil || image.Width <= 0 || image.Height <= 0 || image.Width > 4000 || image.Height > 4000 {
					t.Fatalf("invalid sanitized screenshot: %+v %v", image, err)
				}
			}
		}
	}
	for _, target := range result.Targets {
		if target.Review != "approved" || target.Availability != "scheduled" || target.LastSuccessAt == "" || target.LastError != "" {
			t.Fatalf("invalid independent projections: %+v", target)
		}
	}
	after, err := v.client.Stats(context.Background())
	if err != nil || before.Total != after.Total || before.Applied != after.Applied || before.Total != 16 {
		t.Fatalf("browser caused business requests: before=%+v after=%+v err=%v", before, after, err)
	}
	// Each subsequent real browser query must preserve the last proven state
	// when the new page lacks target/version/reference/status evidence.
	var target state.Target
	for _, candidate := range result.Targets {
		if candidate.Channel == "honor" {
			target = candidate
		}
	}
	for mode := int32(1); mode <= 5; mode++ {
		v.mode.Store(mode)
		if _, err = v.store.DB.Exec("UPDATE targets SET next_check_at='' WHERE id=?", target.ID); err != nil {
			t.Fatal(err)
		}
		_, queryErr := v.engine.Refresh(context.Background(), v.id, []string{"honor"})
		updated, err := v.store.Target(target.ID)
		if err != nil || queryErr == nil || updated.Review != "approved" || updated.Availability != "scheduled" || updated.LastSuccessAt != target.LastSuccessAt || updated.LastError == "" || updated.QueryFailures != int(mode) {
			t.Fatalf("mode %d lost last good evidence: %+v err=%v query=%v", mode, updated, err, queryErr)
		}
	}
	after, _ = v.client.Stats(context.Background())
	if before.Total != after.Total || before.Applied != after.Applied {
		t.Fatal("failed browser queries caused a business write")
	}
}

func TestBrowserAcceptanceSubprocessHelper(t *testing.T) {
	if os.Getenv("PUBLISHER_BROWSER_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	var config browser.Config
	var target browser.Target
	if json.Unmarshal([]byte(os.Getenv("PUBLISHER_BROWSER_HELPER_CONFIG")), &config) != nil || json.Unmarshal([]byte(os.Getenv("PUBLISHER_BROWSER_HELPER_TARGET")), &target) != nil {
		t.Fatal("invalid synthetic helper fixture")
	}
	var err error
	target.VersionCode, err = strconv.ParseInt(os.Getenv("PUBLISHER_BROWSER_HELPER_VERSION"), 10, 64)
	if err != nil {
		t.Fatal("invalid synthetic helper version")
	}
	runner, err := browser.New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Collect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBrowserRealSIGKILLReleasesWorkerAndProfile(t *testing.T) {
	v := newAcceptance(t)
	assertBrowserParentDeath(t, v)
}

func browserChildPID(t *testing.T, parent int) int {
	t.Helper()
	output, err := exec.Command("/bin/ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		t.Fatal("could not inspect the synthetic helper's child process")
	}
	var child int
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != strconv.Itoa(parent) {
			continue
		}
		pid, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil || child != 0 {
			t.Fatal("synthetic helper must own exactly one Node worker")
		}
		child = pid
	}
	if child == 0 {
		t.Fatal("synthetic helper has no live Node worker")
	}
	return child
}

func browserProcessExited(pid int) bool {
	output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	return err != nil || strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
}

func waitBrowserProcessExit(pid int, deadline time.Time) bool {
	for time.Now().Before(deadline) {
		if browserProcessExited(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return browserProcessExited(pid)
}

func assertBrowserParentDeath(t *testing.T, v *acceptanceEnv) {
	t.Helper()
	var target browser.Target
	for _, candidate := range v.adapter.targets {
		if candidate.Channel == "honor" {
			target = candidate
		}
	}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	cleanup := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/browser/") && requests.Add(1) == 1 {
			close(entered)
			// Never release the original request to let the old worker finish.
			// Only worker cancellation or test cleanup can end this handler.
			select {
			case <-r.Context().Done():
				close(canceled)
			case <-cleanup:
			}
			return
		}
		v.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { close(cleanup); server.Close() })
	config := v.config
	config.Origin, config.Timeout = server.URL, 20*time.Second
	configJSON, _ := json.Marshal(config)
	targetJSON, _ := json.Marshal(target)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestBrowserAcceptanceSubprocessHelper$")
	cmd.Env = append(os.Environ(), "PUBLISHER_BROWSER_HELPER=1", "PUBLISHER_BROWSER_HELPER_CONFIG="+string(configJSON), "PUBLISHER_BROWSER_HELPER_TARGET="+string(targetJSON), "PUBLISHER_BROWSER_HELPER_VERSION="+strconv.FormatInt(target.VersionCode, 10))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	select {
	case <-entered:
	case err = <-done:
		t.Fatalf("helper exited before real browser navigation: %v %s", err, output.String())
	case <-time.After(20 * time.Second):
		cmd.Process.Kill()
		<-done
		t.Fatalf("helper did not navigate: %s", output.String())
	}
	workerPID := browserChildPID(t, cmd.Process.Pid)
	t.Cleanup(func() {
		// A failed assertion must still clean up its synthetic orphaned worker.
		if !browserProcessExited(workerPID) {
			syscall.Kill(workerPID, syscall.SIGTERM)
			if !waitBrowserProcessExit(workerPID, time.Now().Add(4*time.Second)) {
				syscall.Kill(workerPID, syscall.SIGKILL)
			}
		}
	})
	killedAt := time.Now()
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	deadline := killedAt.Add(4 * time.Second)
	select {
	case <-canceled:
	case <-time.After(time.Until(deadline)):
		t.Fatal("original HTTP request survived Go SIGKILL; fd3 did not cancel it within 4s")
	}
	if !waitBrowserProcessExit(workerPID, deadline) {
		t.Fatal("Node worker survived Go SIGKILL beyond the 4s parent-death bound")
	}
	// SIGKILL bypasses all Go defers. The Node worker must notice the private
	// fd3 pipe EOF and close Chromium; otherwise the same profile stays busy.
	runner, err := browser.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if _, err = runner.Collect(ctx, target); err != nil {
		t.Fatalf("profile was not reusable within 4s of Go SIGKILL: %v", err)
	}
	stats, err := v.client.Stats(context.Background())
	if err != nil || stats.Total != 16 {
		t.Fatalf("kill/recovery mutated remote business state: %+v %v", stats, err)
	}
}

func TestBrowserRealCancellationAndProfileMutex(t *testing.T) {
	v := newAcceptance(t)
	var target browser.Target
	for _, candidate := range v.adapter.targets {
		if candidate.Channel == "vivo" {
			target = candidate
		}
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var block atomic.Bool
	block.Store(true)
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() && strings.HasPrefix(r.URL.Path, "/browser/") {
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		v.fake.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	config := v.config
	config.Origin = server.URL
	runner, err := browser.New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runner.Collect(ctx, target)
		done <- err
	}()
	select {
	case <-entered:
	case err = <-done:
		close(release)
		t.Fatalf("browser exited before navigation: %v", err)
	case <-time.After(20 * time.Second):
		close(release)
		t.Fatal("browser did not navigate")
	}
	started := time.Now()
	_, err = runner.Collect(context.Background(), target)
	var status *browser.StatusError
	if !errors.As(err, &status) || status.Code != "profile_busy" || time.Since(started) > time.Second {
		cancel()
		close(release)
		t.Fatalf("concurrent observer did not fail promptly at profile mutex: %v", err)
	}
	cancel()
	select {
	case err = <-done:
		if !errors.As(err, &status) || status.Code != "canceled" {
			close(release)
			t.Fatalf("cancellation not explicit: %v", err)
		}
	case <-time.After(8 * time.Second):
		close(release)
		t.Fatal("canceled browser did not stop within its cleanup bound")
	}
	block.Store(false)
	close(release)
	if _, err = runner.Collect(context.Background(), target); err != nil {
		t.Fatalf("profile was not reusable after cancellation: %v", err)
	}
	stats, err := v.client.Stats(context.Background())
	if err != nil || stats.Total != 16 {
		t.Fatalf("profile mutex/cancellation mutated remote business state: %+v %v", stats, err)
	}
}

func TestBrowserRealRedirectCannotReachAnotherOrigin(t *testing.T) {
	v := newAcceptance(t)
	var target browser.Target
	for _, candidate := range v.adapter.targets {
		if candidate.Channel == "xiaomi" {
			target = candidate
		}
	}
	var foreignCalls atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreignCalls.Add(1)
		io.WriteString(w, "synthetic foreign origin")
	}))
	defer foreign.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/forbidden?token=fixture-secret", http.StatusFound)
	}))
	defer redirect.Close()
	config := v.config
	config.Origin = redirect.URL
	runner, err := browser.New(config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Collect(context.Background(), target)
	if err == nil || foreignCalls.Load() != 0 || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("cross-origin navigation escaped read-only boundary: foreign=%d err=%v", foreignCalls.Load(), err)
	}
}
