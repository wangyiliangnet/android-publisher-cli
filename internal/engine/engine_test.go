package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type env struct {
	e              *engine.Engine
	root, path, id string
	client         *simulation.Client
}

func setup(t *testing.T, options simulation.Options) *env {
	t.Helper()
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	store, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	fake, err := simulation.New(filepath.Join(base, "remote"), options)
	must(t, err)
	server := httptest.NewServer(fake)
	client, err := simulation.NewClient(server.URL)
	must(t, err)
	e := &engine.Engine{Store: store, Inspector: artifact.SimulationInspector{}, Adapter: client}
	result, err := e.Plan(context.Background(), path)
	must(t, err)
	t.Cleanup(func() { server.Close(); fake.Close(); store.Close() })
	return &env{e, store.Root, path, result.ReleaseID, client}
}
func (v *env) preflight(t *testing.T) {
	t.Helper()
	_, err := v.e.Preflight(context.Background(), v.id, nil)
	must(t, err)
}
func (v *env) stats(t *testing.T) simulation.Stats {
	t.Helper()
	result, err := v.client.Stats(context.Background())
	must(t, err)
	return result
}

func TestLostResponseInvalidReceiptAndSubsetRecovery(t *testing.T) {
	v := setup(t, simulation.Options{Faults: map[string]string{"vivo": "drop_submit_once", "xiaomi": "invalid_push_once"}})
	v.preflight(t)
	result, err := v.e.Submit(context.Background(), v.id, nil)
	if err == nil || result.Accepted != 4 || result.Total != 6 || result.AllAccepted {
		t.Fatalf("unexpected partial result: %+v %v", result, err)
	}
	result, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
	must(t, err)
	if result.Accepted != 5 || result.AllAccepted || result.Total != 6 {
		t.Fatalf("subset erased global scope: %+v", result)
	}
	result, err = v.e.Submit(context.Background(), v.id, []string{"vivo"})
	must(t, err)
	if !result.AllAccepted || result.AllApproved || result.AllLive {
		t.Fatalf("acceptance conflated with review: %+v", result)
	}
	_, err = v.e.Submit(context.Background(), v.id, nil)
	must(t, err)
	stats := v.stats(t)
	if stats.Total != 16 || stats.Calls["xiaomi"]["push"] != 1 || stats.Calls["vivo"]["submit"] != 1 {
		t.Fatalf("duplicate remote operation: %+v", stats)
	}
}

func TestOfflinePlanImmutabilityAndSourceCleanup(t *testing.T) {
	v := setup(t, simulation.Options{})
	before := v.stats(t)
	v.e.Adapter = nil // Local plan must not require or call a transport.
	result, err := v.e.Plan(context.Background(), v.path)
	must(t, err)
	if result.ReleaseID != v.id {
		t.Fatal("same intent not reused")
	}
	m, notes, err := manifest.Load(v.path, manifest.Simulation)
	must(t, err)
	m.ReleaseID = "other-label"
	s := manifest.Snapshot{Manifest: m, Notes: notes}
	s.Digest = s.CalculateDigest()
	id, err := v.e.Store.Save(s)
	must(t, err)
	if id != v.id {
		t.Fatal("second label created a duplicate release")
	}
	m.ReleaseID = v.id
	s = manifest.Snapshot{Manifest: m, Notes: notes + " different"}
	s.Digest = s.CalculateDigest()
	if _, err = v.e.Store.Save(s); err == nil {
		t.Fatal("changed immutable ID accepted")
	}
	v.e.Adapter = v.client
	must(t, os.RemoveAll(filepath.Dir(v.path)))
	v.preflight(t)
	_, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
	must(t, err)
	if before.Total != 0 || v.stats(t).Total != 1 {
		t.Fatal("plan sent network writes or original artifact was required")
	}
}

func TestChangedFrozenArtifactOrSnapshotPreventsWrites(t *testing.T) {
	for _, corruption := range []string{"artifact", "snapshot"} {
		t.Run(corruption, func(t *testing.T) {
			v := setup(t, simulation.Options{})
			v.preflight(t)
			snapshot, err := v.e.Store.Load(v.id)
			must(t, err)
			if corruption == "artifact" {
				for _, a := range snapshot.Manifest.Artifacts {
					must(t, os.WriteFile(artifact.Path(v.root, a.SHA256), []byte("replaced"), 0600))
				}
			} else {
				snapshot.Notes = "tampered"
				b, _ := json.Marshal(snapshot)
				_, err = v.e.Store.DB.Exec("UPDATE releases SET snapshot=? WHERE id=?", string(b), v.id)
				must(t, err)
			}
			if _, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
				t.Fatal("corrupt input accepted")
			}
			if v.stats(t).Total != 0 {
				t.Fatal("business write preceded verification")
			}
		})
	}
}

func TestPersistenceFailureNeverProceedsToNextWrite(t *testing.T) {
	for _, point := range []string{"before_intent", "after_execute"} {
		t.Run(point, func(t *testing.T) {
			v := setup(t, simulation.Options{})
			v.preflight(t)
			v.e.Hook = func(got string, _ protocol.Request) error {
				if got == point {
					_, err := v.e.Store.DB.Exec("PRAGMA query_only=ON")
					return err
				}
				return nil
			}
			if _, err := v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
				t.Fatal("database failure was ignored")
			}
			want := 0
			if point == "after_execute" {
				want = 1
			}
			if v.stats(t).Total != want {
				t.Fatal("unexpected remote write count")
			}
			_, err := v.e.Store.DB.Exec("PRAGMA query_only=OFF")
			must(t, err)
			v.e.Hook = nil
			_, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
			must(t, err)
			if v.stats(t).Total != 1 {
				t.Fatal("receipt persistence failure caused duplicate request")
			}
		})
	}
}

func TestObservationsDoNotConflateReviewAndAvailability(t *testing.T) {
	v := setup(t, simulation.Options{Review: "approved", Availability: "scheduled"})
	v.preflight(t)
	_, err := v.e.Submit(context.Background(), v.id, nil)
	must(t, err)
	r, err := v.e.Refresh(context.Background(), v.id, nil)
	must(t, err)
	if !r.AllApproved || r.AllLive || !r.AllAccepted {
		t.Fatalf("wrong independent indicators: %+v", r)
	}
	target := r.Targets[0]
	g1, err := v.e.Store.BeginObservation(target.ID)
	must(t, err)
	g2, err := v.e.Store.BeginObservation(target.ID)
	must(t, err)
	o := protocol.Observation{Scope: target.Scope, VersionCode: 1, RemoteRef: target.RemoteRef, Review: "approved", Availability: "live", Source: "simulation", CollectedAt: time.Now().UTC()}
	must(t, v.e.Store.Observe(target, g2, 1, o, nil))
	o.Review = "in_review"
	o.Availability = "not_observed_live"
	must(t, v.e.Store.Observe(target, g1, 1, o, nil))
	got, err := v.e.Store.Target(target.ID)
	must(t, err)
	if got.Review != "approved" || got.Availability != "live" || got.ProjectionGeneration != g2 {
		t.Fatal("late old observation regressed current projection")
	}
	g3, err := v.e.Store.BeginObservation(target.ID)
	must(t, err)
	must(t, v.e.Store.Observe(got, g3, 1, protocol.Observation{}, fmt.Errorf("offline")))
	got, err = v.e.Store.Target(target.ID)
	must(t, err)
	if got.Review != "approved" || got.Availability != "live" || got.QueryFailures != 1 || got.LastError == "" {
		t.Fatal("query failure overwrote established evidence")
	}
	g4, err := v.e.Store.BeginObservation(target.ID)
	must(t, err)
	o.VersionCode = 0
	must(t, v.e.Store.Observe(got, g4, 1, o, nil))
	got, err = v.e.Store.Target(target.ID)
	must(t, err)
	if got.Review != "approved" {
		t.Fatal("old version accepted as target evidence")
	}
}

type observationFailure struct{ code string }

func (e observationFailure) Error() string             { return "raw-page-secret-token" }
func (e observationFailure) ObservationStatus() string { return e.code }

type failedObserver struct {
	protocol.Adapter
	code string
}

func (f failedObserver) Observe(context.Context, string, int64, string) (protocol.Observation, error) {
	return protocol.Observation{}, fmt.Errorf("outer raw-page-secret-token: %w", observationFailure{f.code})
}

func TestRefreshExposesOnlyKnownPublicStatusesAndRecoversHealth(t *testing.T) {
	v := setup(t, simulation.Options{Review: "approved", Availability: "scheduled"})
	v.preflight(t)
	_, err := v.e.Submit(context.Background(), v.id, nil)
	must(t, err)
	_, err = v.e.Refresh(context.Background(), v.id, []string{"honor"})
	must(t, err)
	for _, test := range []struct{ code, action string }{{"profile_busy", "close_other_observer"}, {"auth_required", "login_required"}, {"raw-page-secret-token", ""}, {"observed", ""}} {
		_, err = v.e.Store.DB.Exec("UPDATE targets SET next_check_at='' WHERE channel='honor'")
		must(t, err)
		v.e.Adapter = failedObserver{Adapter: v.client, code: test.code}
		summary, err := v.e.Refresh(context.Background(), v.id, []string{"honor"})
		if err == nil || summary.Errors["honor"] == "" {
			t.Fatal("query error was reported as success")
		}
		status, ok := summary.ObservationStatuses["honor"]
		if test.action == "" && ok || test.action != "" && (!ok || status.Status != test.code || status.NextAction != test.action) {
			t.Fatalf("incorrect public observation status: %+v", summary.ObservationStatuses)
		}
		encoded, err := json.Marshal(summary)
		must(t, err)
		if strings.Contains(string(encoded), "raw-page-secret-token") {
			t.Fatal("raw observer error leaked to the public summary")
		}
	}
	_, err = v.e.Store.DB.Exec("UPDATE targets SET next_check_at='' WHERE channel='honor'")
	must(t, err)
	v.e.Adapter = v.client
	summary, err := v.e.Refresh(context.Background(), v.id, []string{"honor"})
	must(t, err)
	if len(summary.Errors) != 0 || summary.ObservationStatuses["honor"].Status != "observed" {
		t.Fatal("successful refresh did not clear the previous query-health error")
	}
	for _, target := range summary.Targets {
		if target.Channel == "honor" && (target.QueryFailures != 0 || target.LastError != "" || target.Review != "approved" || target.Availability != "scheduled") {
			t.Fatalf("invalid recovered projection: %+v", target)
		}
	}
}

type fixtureEvidenceObserver struct {
	protocol.Adapter
	evidence []protocol.Evidence
}

func (f fixtureEvidenceObserver) Observe(_ context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	return protocol.Observation{Scope: scope, VersionCode: version, RemoteRef: ref, Source: "browser_fixture", Review: "approved", Availability: "live", CollectedAt: time.Now().UTC(), Evidence: f.evidence}, nil
}

// R1: Store.Observe can durably record a rejected evidence set while returning
// nil for its transaction. Refresh must use the saved health result, rather
// than describing an unverified browser response as successfully observed.
func TestRefreshDoesNotReportObservedWhenStateRejectsEvidence(t *testing.T) {
	for _, test := range []string{"missing_browser_fixture_evidence", "invalid_sha256"} {
		t.Run(test, func(t *testing.T) {
			v := setup(t, simulation.Options{Review: "approved", Availability: "scheduled"})
			v.preflight(t)
			_, err := v.e.Submit(context.Background(), v.id, nil)
			must(t, err)
			good, err := v.e.Refresh(context.Background(), v.id, []string{"honor"})
			must(t, err)
			previous := good.Targets[0]
			if previous.Channel != "honor" {
				t.Fatal("test target order changed")
			}
			var refs []protocol.Evidence
			if test == "invalid_sha256" {
				dir := filepath.Join(v.root, "evidence", strings.Repeat("a", 64))
				must(t, os.MkdirAll(dir, 0700))
				must(t, os.WriteFile(filepath.Join(dir, "evidence.json"), []byte("{}"), 0600))
				must(t, os.WriteFile(filepath.Join(dir, "evidence.png"), []byte("\x89PNG\r\n\x1a\n"), 0600))
				refs = []protocol.Evidence{
					{Kind: "text", Path: "evidence/" + strings.Repeat("a", 64) + "/evidence.json", SHA256: strings.Repeat("1", 64), Size: 2},
					{Kind: "png", Path: "evidence/" + strings.Repeat("a", 64) + "/evidence.png", SHA256: strings.Repeat("2", 64), Size: 8},
				}
			}
			_, err = v.e.Store.DB.Exec("UPDATE targets SET next_check_at='' WHERE channel='honor'")
			must(t, err)
			v.e.Adapter = fixtureEvidenceObserver{Adapter: v.client, evidence: refs}
			summary, err := v.e.Refresh(context.Background(), v.id, []string{"honor"})
			if err == nil || summary.Errors["honor"] == "" || summary.ObservationStatuses["honor"].Status != "evidence_insufficient" || summary.ObservationStatuses["honor"].NextAction != "verify_target_evidence" {
				t.Fatalf("rejected evidence was reported as observed: %+v, %v", summary, err)
			}
			target := summary.Targets[0]
			if target.LastError == "" || target.QueryFailures != 1 || target.LastSuccessAt != previous.LastSuccessAt || target.Review != previous.Review || target.Availability != previous.Availability {
				t.Fatalf("rejected browser evidence changed the last known good state: %+v", target)
			}
		})
	}
}

func TestBackupRestoreIsolatesOperationsMissingFromBackup(t *testing.T) {
	v := setup(t, simulation.Options{})
	backup := filepath.Join(t.TempDir(), "backup")
	must(t, v.e.Store.Backup(backup))
	v.preflight(t)
	_, err := v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
	must(t, err)
	must(t, v.e.Store.Close())
	must(t, state.Restore(v.root, backup, manifest.Simulation))
	s, err := state.Open(v.root, manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	v.e.Store = s
	r, err := v.e.Status(v.id)
	must(t, err)
	if !r.RecoveryRequired || r.Accepted != 0 {
		t.Fatal("restore failed to isolate a missing operation")
	}
	if _, err = v.e.Preflight(context.Background(), v.id, nil); err == nil {
		t.Fatal("restored state allowed business preflight")
	}
	if _, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
		t.Fatal("restore resubmitted an operation absent from backup")
	}
	if v.stats(t).Total != 1 {
		t.Fatal("restore duplicated a remote submission")
	}
	newSnapshot, err := s.Load(v.id)
	must(t, err)
	newSnapshot.Manifest.ReleaseID = "new-release"
	newSnapshot.Notes += " changed"
	newSnapshot.Digest = newSnapshot.CalculateDigest()
	_, err = s.Save(newSnapshot)
	must(t, err)
	if _, err = v.e.Preflight(context.Background(), "new-release", nil); err == nil {
		t.Fatal("new ID bypassed recovery isolation")
	}
}

func TestSimulationClientRejectsNonLoopbackAndRedirect(t *testing.T) {
	for _, url := range []string{"https://127.0.0.1:80", "http://localhost:80", "http://example.com:80", "http://127.0.0.1:80/path", "http://user@127.0.0.1:80", "http://127.0.0.1:80/?secret=x"} {
		if _, err := simulation.NewClient(url); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", url)
		}
	}
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 307) }))
	defer source.Close()
	c, err := simulation.NewClient(source.URL)
	must(t, err)
	if _, err = c.Inspect(context.Background(), "scope"); err == nil || redirected {
		t.Fatal("simulation followed redirect")
	}
}

// TestWorkerProcess runs only inside explicitly spawned test subprocesses.
func TestWorkerProcess(t *testing.T) {
	role := os.Getenv("PUBLISHER_TEST_ROLE")
	if role == "" {
		return
	}
	if role == "server" {
		var options simulation.Options
		must(t, json.Unmarshal([]byte(os.Getenv("PUBLISHER_TEST_OPTIONS")), &options))
		s, err := simulation.New(os.Getenv("PUBLISHER_TEST_REMOTE"), options)
		must(t, err)
		defer s.Close()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		must(t, privatefs.Write(os.Getenv("PUBLISHER_TEST_READY"), []byte("http://"+listener.Addr().String())))
		must(t, http.Serve(listener, s))
		return
	}
	s, err := state.Open(os.Getenv("PUBLISHER_TEST_ROOT"), manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	c, err := simulation.NewClient(os.Getenv("PUBLISHER_TEST_ENDPOINT"))
	must(t, err)
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: c, Hook: func(point string, q protocol.Request) error {
		if point == os.Getenv("PUBLISHER_TEST_POINT") && q.Kind == os.Getenv("PUBLISHER_TEST_KIND") {
			if err := privatefs.Write(os.Getenv("PUBLISHER_TEST_READY"), []byte("ready")); err != nil {
				return err
			}
			for {
				time.Sleep(time.Hour)
			}
		}
		return nil
	}}
	_, err = e.Submit(context.Background(), "simulation-1", []string{os.Getenv("PUBLISHER_TEST_CHANNEL")})
	must(t, err)
}

type child struct {
	command *exec.Cmd
	log     *os.File
	done    bool
}

func startChild(t *testing.T, role, ready string, vars map[string]string) *child {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerProcess$", "-test.timeout=18s")
	cmd.Env = append(os.Environ(), "PUBLISHER_TEST_ROLE="+role, "PUBLISHER_TEST_READY="+ready)
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	log, err := os.CreateTemp(t.TempDir(), "child-")
	must(t, err)
	cmd.Stdout = log
	cmd.Stderr = log
	c := &child{command: cmd, log: log}
	must(t, cmd.Start())
	t.Cleanup(func() {
		if !c.done {
			cmd.Process.Kill()
			cmd.Wait()
		}
		log.Close()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return c
		}
		if time.Now().After(deadline) {
			c.stop()
			b, _ := os.ReadFile(log.Name())
			t.Fatalf("child did not reach checkpoint: %s", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func (c *child) stop() {
	if !c.done {
		c.command.Process.Kill()
		c.command.Wait()
		c.done = true
	}
}

func TestActualProcessDeathAndPersistentRemoteRecovery(t *testing.T) {
	for _, scenario := range []struct {
		point, kind, channel string
		before, after        int
	}{
		{"after_intent", "push", "xiaomi", 0, 1},
		{"after_execute", "push", "xiaomi", 1, 1},
		{"after_receipt", "bind", "huawei", 2, 3},
	} {
		t.Run(scenario.point+"_"+scenario.kind, func(t *testing.T) {
			base := t.TempDir()
			remoteDir := filepath.Join(base, "remote")
			serverReady := filepath.Join(base, "server-ready")
			server := startChild(t, "server", serverReady, map[string]string{"PUBLISHER_TEST_REMOTE": remoteDir, "PUBLISHER_TEST_OPTIONS": "{}"})
			endpointBytes, err := os.ReadFile(serverReady)
			must(t, err)
			endpoint := string(endpointBytes)
			client, err := simulation.NewClient(endpoint)
			must(t, err)
			path, err := simulation.Init(filepath.Join(base, "fixture"))
			must(t, err)
			root := filepath.Join(base, "client")
			s, err := state.Open(root, manifest.Simulation, false)
			must(t, err)
			e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: client}
			r, err := e.Plan(context.Background(), path)
			must(t, err)
			_, err = e.Preflight(context.Background(), r.ReleaseID, nil)
			must(t, err)
			must(t, s.Close())
			ready := filepath.Join(base, "worker-ready")
			worker := startChild(t, "engine", ready, map[string]string{"PUBLISHER_TEST_ROOT": root, "PUBLISHER_TEST_ENDPOINT": endpoint, "PUBLISHER_TEST_POINT": scenario.point, "PUBLISHER_TEST_KIND": scenario.kind, "PUBLISHER_TEST_CHANNEL": scenario.channel})
			if _, err = state.Open(root, manifest.Simulation, false); !errors.Is(err, state.ErrBusy) {
				t.Fatalf("second writer not excluded: %v", err)
			}
			reader, err := state.Open(root, manifest.Simulation, true)
			must(t, err)
			_, err = reader.Load(r.ReleaseID)
			must(t, err)
			must(t, reader.Close())
			stats, err := client.Stats(context.Background())
			must(t, err)
			if stats.Total != scenario.before {
				t.Fatalf("wrong checkpoint count: %+v", stats)
			}
			worker.stop()
			server.stop() // Restart both processes; fake receipts must survive independently of the client journal.
			must(t, os.Remove(serverReady))
			startChild(t, "server", serverReady, map[string]string{"PUBLISHER_TEST_REMOTE": remoteDir, "PUBLISHER_TEST_OPTIONS": "{}"})
			endpointBytes, err = os.ReadFile(serverReady)
			must(t, err)
			client, err = simulation.NewClient(string(endpointBytes))
			must(t, err)
			s, err = state.Open(root, manifest.Simulation, false)
			must(t, err)
			defer s.Close()
			e.Store = s
			e.Adapter = client
			_, err = e.Submit(context.Background(), r.ReleaseID, []string{scenario.channel})
			must(t, err)
			stats, err = client.Stats(context.Background())
			must(t, err)
			if stats.Total != scenario.after {
				t.Fatalf("recovery repeated a completed operation: %+v", stats)
			}
		})
	}
}

func TestUnknownOldIntentBlocksNewRelease(t *testing.T) {
	v := setup(t, simulation.Options{Faults: map[string]string{"xiaomi": "drop_push_once"}, Inconclusive: true})
	v.preflight(t)
	if _, err := v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
		t.Fatal("missing unknown result")
	}
	snapshot, err := v.e.Store.Load(v.id)
	must(t, err)
	snapshot.Manifest.ReleaseID = "new-release"
	snapshot.Manifest.VersionCode = 2
	snapshot.Manifest.VersionName = "2.0"
	nextArtifact, err := artifact.CreateSimulation(filepath.Join(filepath.Dir(v.path), "next.sim"), artifact.Info{PackageName: snapshot.Manifest.PackageName, VersionCode: 2, VersionName: "2.0", ABI: []string{"arm64-v8a"}})
	must(t, err)
	snapshot.Manifest.Artifacts = map[string]manifest.Artifact{"fixture": nextArtifact}
	must(t, artifact.Freeze(context.Background(), v.root, snapshot.Manifest, artifact.SimulationInspector{}))
	snapshot.Notes += " revision"
	snapshot.Digest = snapshot.CalculateDigest()
	_, err = v.e.Store.Save(snapshot)
	must(t, err)
	if _, err = v.e.Preflight(context.Background(), "new-release", []string{"xiaomi"}); err == nil {
		t.Fatal("new intent bypassed the unresolved old operation")
	}
	if _, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
		t.Fatal("inconclusive query retried submission")
	}
	if v.stats(t).Total != 1 {
		t.Fatal("unknown request duplicated")
	}
}

func TestBackupPredatingEntireReleaseStillBlocksNewLabel(t *testing.T) {
	v := setup(t, simulation.Options{})
	base := t.TempDir()
	empty, err := state.Open(filepath.Join(base, "empty"), manifest.Simulation, false)
	must(t, err)
	backup := filepath.Join(base, "backup")
	must(t, empty.Backup(backup))
	must(t, empty.Close())
	v.preflight(t)
	_, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
	must(t, err)
	must(t, v.e.Store.Close())
	must(t, state.Restore(v.root, backup, manifest.Simulation))
	restored, err := state.Open(v.root, manifest.Simulation, false)
	must(t, err)
	defer restored.Close()
	v.e.Store = restored
	if _, err = restored.Load(v.id); err == nil {
		t.Fatal("test backup unexpectedly contained the release")
	}
	data, err := os.ReadFile(v.path)
	must(t, err)
	data = bytes.Replace(data, []byte("release_id: simulation-1"), []byte("release_id: new-label"), 1)
	must(t, os.WriteFile(v.path, data, 0600))
	r, err := v.e.Plan(context.Background(), v.path)
	must(t, err)
	if r.ReleaseID != "new-label" || !r.RecoveryRequired {
		t.Fatal("restore guard was tied to missing release row")
	}
	if _, err = v.e.Preflight(context.Background(), r.ReleaseID, nil); err == nil {
		t.Fatal("new label bypassed old backup recovery")
	}
	if v.stats(t).Total != 1 {
		t.Fatal("old backup permitted a new submission")
	}
}

func TestFakeCountsRejectedDuplicateRequests(t *testing.T) {
	v := setup(t, simulation.Options{})
	v.preflight(t)
	_, err := v.e.Submit(context.Background(), v.id, []string{"xiaomi"})
	must(t, err)
	op, err := v.e.Store.Operation(v.id+"/xiaomi", "push")
	must(t, err)
	_, err = v.client.ExecuteOne(context.Background(), op.Request)
	must(t, err)
	stats := v.stats(t)
	if stats.Total != 2 || stats.Applied != 1 {
		t.Fatal("fake hid a duplicate request behind metadata rejection")
	}
	reconciled, err := v.client.Reconcile(context.Background(), op.Request)
	must(t, err)
	if reconciled.Result != "confirmed_applied" || reconciled.Receipt == nil || reconciled.Receipt.RemoteRef != op.RemoteRef {
		t.Fatal("duplicate rejection erased durable success evidence")
	}
}

func TestTwoEnginesCannotReconcileAnInFlightOperation(t *testing.T) {
	v := setup(t, simulation.Options{})
	v.preflight(t)
	second := *v.e
	entered, resume := make(chan struct{}), make(chan struct{})
	v.e.Hook = func(point string, q protocol.Request) error {
		if point == "after_intent" && q.Channel == "xiaomi" {
			close(entered)
			<-resume
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first engine missed checkpoint")
	}
	_, secondErr := second.Submit(context.Background(), v.id, []string{"xiaomi"})
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("first engine failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first engine hung")
	}
	if !errors.Is(secondErr, state.ErrBusy) {
		t.Fatalf("concurrent command was not excluded: %v; stats=%+v", secondErr, v.stats(t))
	}
	if v.stats(t).Total != 1 {
		t.Fatal("in-flight intent was executed twice")
	}
	op, err := v.e.Store.Operation(v.id+"/xiaomi", "push")
	must(t, err)
	if err = v.e.Store.Complete(op.Request, "confirmed_rejected", "", "late rejection"); err == nil {
		t.Fatal("late rejection overwrote a terminal receipt")
	}
	target, err := v.e.Store.Target(v.id + "/xiaomi")
	must(t, err)
	if target.Phase != "submitted" {
		t.Fatal("terminal target regressed")
	}
}

func TestNotAppliedSubmitPreservesBoundApplicationOwner(t *testing.T) {
	v := setup(t, simulation.Options{})
	v.preflight(t)
	v.e.Hook = func(point string, q protocol.Request) error {
		if point == "after_intent" && q.Kind == "submit" {
			return errors.New("stop before request")
		}
		return nil
	}
	if _, err := v.e.Submit(context.Background(), v.id, []string{"huawei"}); err == nil {
		t.Fatal("fault did not interrupt")
	}
	v.e.Hook = nil
	_, err := v.e.Reconcile(context.Background(), v.id, []string{"huawei"})
	must(t, err)
	old, err := v.e.Store.Target(v.id + "/huawei")
	must(t, err)
	if old.Phase != "bound" {
		t.Fatalf("confirmed checkpoint lost: %s", old.Phase)
	}
	snapshot, err := v.e.Store.Load(v.id)
	must(t, err)
	snapshot.Manifest.ReleaseID = "second-release"
	snapshot.Notes += " revision"
	snapshot.Digest = snapshot.CalculateDigest()
	_, err = v.e.Store.Save(snapshot)
	must(t, err)
	if err = v.e.Store.CheckWritable(old.Scope, "second-release/huawei"); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("bound owner not retained: %v", err)
	}
	if _, err = v.e.Preflight(context.Background(), "second-release", []string{"huawei"}); err == nil {
		t.Fatal("second release escaped bound ownership")
	}
	_, err = v.e.Submit(context.Background(), v.id, []string{"huawei"})
	must(t, err)
	if stats := v.stats(t); stats.Total != 3 || stats.Calls["huawei"]["bind"] != 1 {
		t.Fatal("recovery repeated a binding")
	}
}

func TestRemoteMetadataChangeRequiresNewPreflight(t *testing.T) {
	v := setup(t, simulation.Options{})
	v.preflight(t)
	// A stale preflight is represented by a different previously read field, not by changing the immutable patch.
	_, err := v.e.Store.DB.Exec("UPDATE targets SET preflight=replace(preflight, 'Existing title', 'Stale title') WHERE channel='xiaomi'")
	must(t, err)
	if _, err = v.e.Submit(context.Background(), v.id, []string{"xiaomi"}); err == nil {
		t.Fatal("stale preflight overwrote remote metadata")
	}
	if v.stats(t).Total != 0 {
		t.Fatal("write preceded remote diff check")
	}
}

func TestSimulationSignatureTamper(t *testing.T) {
	dir := t.TempDir()
	path, err := simulation.Init(filepath.Join(dir, "fixture"))
	must(t, err)
	m, _, err := manifest.Load(path, manifest.Simulation)
	must(t, err)
	for _, a := range m.Artifacts {
		data, err := os.ReadFile(a.Path)
		must(t, err)
		changed := bytes.Replace(data, []byte(`"version_code":1`), []byte(`"version_code":2`), 1)
		if bytes.Equal(data, changed) {
			t.Fatal("test did not alter signed content")
		}
		must(t, os.WriteFile(a.Path, changed, 0600))
		if _, err = (artifact.SimulationInspector{}).Inspect(context.Background(), a.Path); err == nil {
			t.Fatal("tampered signature accepted")
		}
	}
}

func TestUnrecognizedChannelSelectionFailsBeforeRequests(t *testing.T) {
	v := setup(t, simulation.Options{})
	_, err := v.e.Preflight(context.Background(), v.id, []string{"tencentMyApp"})
	if err == nil {
		t.Fatal("implicit alias accepted")
	}
	if v.stats(t).Total != 0 {
		t.Fatal("unexpected write")
	}
}
