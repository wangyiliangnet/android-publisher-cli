package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "fake-runner-parent" {
		var input struct {
			Config Config
			Target workerTarget
		}
		if json.NewDecoder(os.Stdin).Decode(&input) != nil {
			os.Exit(4)
		}
		input.Target.Target.VersionCode, _ = strconv.ParseInt(input.Target.VersionCode, 10, 64)
		r, err := New(input.Config)
		if err != nil {
			os.Exit(5)
		}
		_, _ = r.Collect(context.Background(), input.Target.Target)
		os.Exit(0)
	}
	if len(os.Args) == 2 && strings.HasPrefix(filepath.Base(os.Args[1]), "fake-worker-") {
		fakeWorker(strings.TrimPrefix(filepath.Base(os.Args[1]), "fake-worker-"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeWorker(mode string) {
	var req workerRequest
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(3)
	}
	if mode == "hold" {
		_ = os.WriteFile(filepath.Join(filepath.Dir(req.ProfileDir), "worker-started"), []byte("1"), 0600)
		life := os.NewFile(3, "liveness")
		_, _ = io.Copy(io.Discard, life)
		_ = os.WriteFile(filepath.Join(filepath.Dir(req.ProfileDir), "worker-closed"), []byte("1"), 0600)
		os.Exit(0)
	}
	if mode == "hang" {
		time.Sleep(time.Hour)
		return
	}
	if mode == "overflow" {
		_, _ = io.WriteString(os.Stdout, strings.Repeat("sensitive_value", maxOutput))
		return
	}
	if mode == "stderr" {
		_, _ = io.WriteString(os.Stderr, "credential=NEVER_PUBLIC token=NEVER_PUBLIC\n")
		os.Exit(2)
	}
	if mode == "auth_required" {
		_, _ = io.WriteString(os.Stdout, `{"schema_version":1,"mode":"simulation","status":"auth_required"}`)
		return
	}
	if mode == "duplicate" {
		_, _ = io.WriteString(os.Stdout, `{"schema_version":1,"mode":"simulation","status":"auth_required","status":"profile_busy"}`)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	cleanURL := strings.Split(req.URL, "?")[0]
	o := workerObservation{Scope: req.Target.Scope, AdapterBinding: req.Target.AdapterBinding, VersionCode: req.Target.VersionCode, RemoteRef: req.Target.RemoteRef, Review: "approved", Availability: "scheduled", Source: "browser_fixture", CollectedAt: now, SubmittedAt: now, URL: cleanURL}
	p := proof{SchemaVersion: 1, Mode: "simulation", Kind: "sanitized_browser_evidence", Target: req.Target, Observed: proofObserved{Channel: req.Target.Channel, Account: req.Target.Account, AppID: req.Target.AppID, Scope: req.Target.Scope, PackageName: req.Target.PackageName, VersionCode: req.Target.VersionCode, RemoteRef: req.Target.RemoteRef, Review: o.Review, Availability: o.Availability, ReviewText: "审核通过", AvailabilityText: "定时发布", SubmittedAt: now}, CollectedAt: now, URL: cleanURL}
	if mode == "proof-account" {
		p.Observed.Account = "another-account"
	}
	if mode == "proof-intent" {
		p.Target.IntentDigest = strings.Repeat("f", 64)
	}
	if mode == "proof-status" {
		p.Observed.Availability = "live"
	}
	if mode == "proof-ref" {
		p.Observed.RemoteRef = "other-submission"
	}
	if mode == "proof-url" {
		p.URL += "?secret=NEVER_PUBLIC"
	}
	if mode == "proof-label" {
		p.Observed.ReviewText = "credential=NEVER_PUBLIC"
	}
	if mode == "future-submit" {
		o.SubmittedAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
		p.Observed.SubmittedAt = o.SubmittedAt
	}
	if mode == "old-collection" {
		o.CollectedAt = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
		o.SubmittedAt = time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339Nano)
		p.CollectedAt = o.CollectedAt
		p.Observed.SubmittedAt = o.SubmittedAt
	}
	if mode == "future-collection" {
		o.CollectedAt = time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
		p.CollectedAt = o.CollectedAt
	}
	if mode == "precision" {
		o.CollectedAt = time.Now().UTC().Truncate(time.Millisecond).Add(123456 * time.Nanosecond).Format(time.RFC3339Nano)
		o.SubmittedAt = time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
		p.CollectedAt = o.CollectedAt
		p.Observed.SubmittedAt = o.SubmittedAt
	}
	text, _ := json.Marshal(p)
	var pngBuffer bytes.Buffer
	_ = png.Encode(&pngBuffer, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	png := pngBuffer.Bytes()
	textPath := filepath.Join(req.EvidenceDir, "evidence.json")
	pngPath := filepath.Join(req.EvidenceDir, "evidence.png")
	_ = os.WriteFile(textPath, text, 0600)
	_ = os.WriteFile(pngPath, png, 0600)
	if mode == "png-symlink" {
		outside := filepath.Join(filepath.Dir(req.EvidenceDir), "outside.png")
		_ = os.WriteFile(outside, png, 0600)
		_ = os.Remove(pngPath)
		_ = os.Symlink(outside, pngPath)
	}
	if mode == "png-fifo" {
		_ = os.Remove(pngPath)
		_ = syscall.Mkfifo(pngPath, 0600)
	}
	if mode == "dir-symlink" {
		outside := req.EvidenceDir + "-outside"
		_ = os.Rename(req.EvidenceDir, outside)
		_ = os.Symlink(outside, req.EvidenceDir)
	}
	if mode == "file-mode" {
		_ = os.Chmod(textPath, 0644)
	}
	if mode == "dir-mode" {
		_ = os.Chmod(req.EvidenceDir, 0755)
	}
	if mode == "version" {
		o.VersionCode = "42"
	}
	if mode == "binding" {
		o.AdapterBinding = strings.Repeat("b", 64)
	}
	if mode == "unknown-state" {
		o.Review = "approved-ish"
	}
	if mode == "live-without-review" {
		o.Review = "unknown"
		o.Availability = "live"
	}
	if mode == "url" {
		o.URL = "https://vendor.invalid/portal?secret=NEVER_PUBLIC"
	}
	if mode == "timestamp" {
		o.CollectedAt = "bad-clock"
	}
	ts, ps := sha256.Sum256(text), sha256.Sum256(png)
	e := workerEvidence{TextSHA256: hex.EncodeToString(ts[:]), PNGSHA256: hex.EncodeToString(ps[:]), TextBytes: int64(len(text)), PNGBytes: int64(len(png))}
	if mode == "hash" {
		e.TextSHA256 = strings.Repeat("a", 64)
	}
	if mode == "size" {
		e.PNGBytes++
	}
	res := workerResult{SchemaVersion: 1, Mode: "simulation", Status: "observed", Observation: &o, Evidence: &e}
	_ = json.NewEncoder(os.Stdout).Encode(res)
}

func runnerFixture(t *testing.T, mode string) (*Runner, Target) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(base, "fake-worker-"+mode)
	if err = os.WriteFile(worker, []byte("synthetic helper"), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	coordinator := account.Coordinator{Root: filepath.Join(base, "coordination")}
	r, err := New(Config{Root: filepath.Join(base, "state"), NodePath: executable, WorkerPath: worker, Origin: "http://127.0.0.1:12345", Simulation: true, Coordinator: &coordinator, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Channel: "honor", Account: "synthetic-account", AppID: "9007199254740993", PackageName: "com.example.synthetic", VersionCode: 9223372036854775807, RemoteRef: "9007199254740999", AdapterBinding: strings.Repeat("a", 64), IntentDigest: strings.Repeat("c", 64)}
	target.Scope = protocol.Digest([]string{target.Channel, target.Account, target.AppID, target.PackageName})
	return r, target
}

func statusCode(t *testing.T, err error, want string) {
	t.Helper()
	var status *StatusError
	if !errors.As(err, &status) || status.Code != want {
		t.Fatalf("want %s, got %v", want, err)
	}
	if strings.Contains(err.Error(), "NEVER_PUBLIC") {
		t.Fatal("sensitive worker detail escaped")
	}
}

func TestRunnerVerifiedEvidenceAndReadOnlyAdapter(t *testing.T) {
	r, target := runnerFixture(t, "good")
	before := time.Now().UTC()
	o, err := r.Collect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if o.VersionCode != target.VersionCode || o.Review != "approved" || o.Availability != "scheduled" || o.Source != "browser_fixture" || o.CollectedAt.Before(before) {
		t.Fatalf("incorrect observation: %+v", o)
	}
	if len(o.Evidence) != 2 {
		t.Fatal("missing sanitized evidence")
	}
	for _, e := range o.Evidence {
		if filepath.IsAbs(e.Path) || !strings.HasPrefix(e.Path, "evidence/") {
			t.Fatal("non-relative evidence")
		}
		p := filepath.Join(r.config.Root, filepath.FromSlash(e.Path))
		hash, size, err := privatefs.Hash(p)
		if err != nil || hash != e.SHA256 || size != e.Size {
			t.Fatal("evidence reference invalid")
		}
	}
	a := &Adapter{Runner: r, Targets: map[string]Target{target.Scope: target}}
	if _, err = a.Inspect(context.Background(), target.Scope); err == nil {
		t.Fatal("browser inspect enabled")
	}
	if _, err = a.ExecuteOne(context.Background(), protocol.Request{}); err == nil {
		t.Fatal("browser write enabled")
	}
	rec, err := a.Reconcile(context.Background(), protocol.Request{OperationID: "unknown-write"})
	if err != nil || rec.Result != "inconclusive" || rec.Receipt != nil {
		t.Fatal("browser reconciled unknown business write")
	}
	_, err = a.Observe(context.Background(), target.Scope, target.VersionCode, "another-ref")
	statusCode(t, err, "evidence_insufficient")
}

func TestRunnerRejectsUnverifiedOutput(t *testing.T) {
	for _, mode := range []string{"proof-account", "proof-intent", "proof-status", "proof-ref", "proof-url", "proof-label", "future-submit", "old-collection", "future-collection", "png-symlink", "png-fifo", "dir-symlink", "file-mode", "dir-mode", "version", "binding", "unknown-state", "live-without-review", "url", "timestamp", "hash", "size", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			r, target := runnerFixture(t, mode)
			_, err := r.Collect(context.Background(), target)
			statusCode(t, err, "evidence_insufficient")
		})
	}
	for _, mode := range []string{"stderr", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			r, target := runnerFixture(t, mode)
			_, err := r.Collect(context.Background(), target)
			statusCode(t, err, "browser_unavailable")
		})
	}
	r, target := runnerFixture(t, "auth_required")
	_, err := r.Collect(context.Background(), target)
	statusCode(t, err, "auth_required")
}

func TestRunnerRetainsVerifiedWorkerCollectionTime(t *testing.T) {
	r, target := runnerFixture(t, "precision")
	o, err := r.Collect(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	text, err := privatefs.Read(filepath.Join(r.config.Root, filepath.FromSlash(o.Evidence[0].Path)), maxText)
	if err != nil {
		t.Fatal(err)
	}
	var p proof
	if err = strictJSON(text, &p); err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse(time.RFC3339Nano, p.CollectedAt)
	if err != nil || !o.CollectedAt.Equal(stamp) || o.CollectedAt.Nanosecond()%int(time.Millisecond) != 123456 || o.CollectedAt.Location() != time.UTC {
		t.Fatalf("worker collection time or precision changed: proof=%s observation=%s", p.CollectedAt, o.CollectedAt.Format(time.RFC3339Nano))
	}
	// A submission may predate this collection by a day. Only the collection
	// clock must describe the current browser invocation.
	submitted, err := time.Parse(time.RFC3339Nano, p.Observed.SubmittedAt)
	if err != nil || o.CollectedAt.Sub(submitted) < 23*time.Hour {
		t.Fatal("old submission was not retained")
	}
}

func TestRunnerCancellationAndSharedAccountLease(t *testing.T) {
	r, target := runnerFixture(t, "hold")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := r.Collect(ctx, target); done <- err }()
	marker := filepath.Join(r.config.Root, "browser", target.Channel, account.Key(target.Account), "worker-started")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("worker failed to start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitCtx, stopWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopWait()
	err := r.coordinator.With(waitCtx, account.Scope{Channel: target.Channel, Account: target.Account}, func(*account.Lease) error { t.Error("account lease escaped browser ownership"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("account lock did not wait: %v", err)
	}
	// A competing browser gets an actionable profile-busy result without
	// disturbing the current worker or deleting Chromium profile lock files.
	_, err = r.Collect(context.Background(), target)
	statusCode(t, err, "profile_busy")
	cancel()
	select {
	case err := <-done:
		statusCode(t, err, "canceled")
	case <-time.After(3 * time.Second):
		t.Fatal("worker cancellation exceeded bound")
	}
	if err = r.coordinator.With(context.Background(), account.Scope{Channel: target.Channel, Account: target.Account}, func(*account.Lease) error { return nil }); err != nil {
		t.Fatal("account lease not released")
	}
}

func TestRunnerAccountWaitAndTimeoutAreBounded(t *testing.T) {
	r, target := runnerFixture(t, "good")
	locked := make(chan struct{})
	unlock := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- r.coordinator.With(context.Background(), account.Scope{Channel: target.Channel, Account: target.Account}, func(*account.Lease) error { close(locked); <-unlock; return nil })
	}()
	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err := r.Collect(ctx, target)
	cancel()
	statusCode(t, err, "timed_out")
	close(unlock)
	if err = <-released; err != nil {
		t.Fatal(err)
	}
	r, target = runnerFixture(t, "hang")
	r.config.Timeout = 30 * time.Millisecond
	before := time.Now()
	_, err = r.Collect(context.Background(), target)
	statusCode(t, err, "timed_out")
	if time.Since(before) > 3*time.Second {
		t.Fatal("hanging worker exceeded bounded termination")
	}
}

func TestRunnerProductionAndManagedPathGates(t *testing.T) {
	r, target := runnerFixture(t, "good")
	config := r.config
	config.Simulation = false
	_, err := New(config)
	statusCode(t, err, "production_disabled")
	for _, origin := range []string{"https://127.0.0.1:123", "http://localhost:123", "http://127.0.0.2:123", "http://127.0.0.1", "http://127.0.0.1:123/path", "http://127.0.0.1:123?secret=x", "http://user:pass@127.0.0.1:123"} {
		config = r.config
		config.Origin = origin
		_, err = New(config)
		statusCode(t, err, "invalid_input")
	}
	linkRoot := r.config.Root + "-link"
	if err = os.Symlink(r.config.Root, linkRoot); err != nil {
		t.Fatal(err)
	}
	config = r.config
	config.Root = linkRoot
	_, err = New(config)
	statusCode(t, err, "storage_unavailable")
	outside := filepath.Join(filepath.Dir(r.config.Root), "outside")
	if err = os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(r.config.Root, "browser")); err != nil {
		t.Fatal(err)
	}
	_, err = r.Collect(context.Background(), target)
	statusCode(t, err, "storage_unavailable")
	if files, _ := os.ReadDir(outside); len(files) != 0 {
		t.Fatal("managed path followed symlink")
	}
}

func TestRunnerDoesNotChangeAncestorPermissions(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	r, target := runnerFixture(t, "good")
	config := r.config
	config.Root = filepath.Join(base, "new-parent", "state")
	if _, err = New(config); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(base); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("changed preexisting ancestor permissions")
	}
	target.Scope = strings.Repeat("f", 64)
	_, err = r.Collect(context.Background(), target)
	statusCode(t, err, "invalid_input")
}

func TestRunnerParentDeathClosesLivenessPipe(t *testing.T) {
	r, target := runnerFixture(t, "hold")
	r.config.Timeout = 30 * time.Second
	config := r.config
	config.Coordinator = &r.coordinator
	input, _ := json.Marshal(struct {
		Config Config
		Target workerTarget
	}{config, workerTarget{Target: target, VersionCode: strconv.FormatInt(target.VersionCode, 10)}})
	executable, _ := os.Executable()
	cmd := exec.Command(executable, "fake-runner-parent")
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	profileRoot := filepath.Join(r.config.Root, "browser", target.Channel, account.Key(target.Account))
	waitForFile := func(name string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(profileRoot, name)); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing child marker %s", name)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitForFile("worker-started")
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitForFile("worker-closed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.coordinator.With(ctx, account.Scope{Channel: target.Channel, Account: target.Account}, func(*account.Lease) error { return nil }); err != nil {
		t.Fatal("dead parent retained account lock")
	}
}
