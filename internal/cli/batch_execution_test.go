package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

type batchTestEnvironment struct {
	root, remote, approval string
	batch                  batchFile
	plan                   batchApproval
	server                 *httptest.Server
	fake                   *simulation.Server
	client                 *simulation.Client
}

func batchTestSetup(t *testing.T, channels []string, options simulation.Options) *batchTestEnvironment {
	t.Helper()
	v := &batchTestEnvironment{root: filepath.Join(t.TempDir(), "client"), remote: filepath.Join(t.TempDir(), "remote"), approval: filepath.Join(t.TempDir(), "review.json")}
	var err error
	v.fake, err = simulation.New(v.remote, options)
	if err != nil {
		t.Fatal(err)
	}
	v.server = httptest.NewServer(v.fake)
	v.client, err = simulation.NewClient(v.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.server.Close(); v.fake.Close() })
	fixture, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "fixture.apk"), artifact.Info{PackageName: "com.example.batch", VersionCode: 4, VersionName: "4.0", ABI: []string{"arm64-v8a"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(v.root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	v.batch = batchFile{SchemaVersion: 1, ID: "team-batch", Entries: []batchEntry{}}
	for _, channel := range channels {
		id := "team-" + channel
		m := manifest.Manifest{SchemaVersion: 1, ReleaseID: id, Mode: manifest.Simulation, PackageName: "com.example.batch", VersionCode: 4, VersionName: "4.0", Artifacts: map[string]manifest.Artifact{"fixture": fixture}, ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{channel: {AccountRef: "team", AppRef: "example-app", Artifact: "fixture"}}}
		snap := manifest.Snapshot{Manifest: m, Notes: "本次优化\n- 保留说明原文。"}
		snap.Digest = snap.CalculateDigest()
		if err = artifact.Freeze(context.Background(), v.root, m, artifact.SimulationInspector{}); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Save(snap); err != nil {
			t.Fatal(err)
		}
		if _, err = (&engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: v.client}).Preflight(context.Background(), id, nil); err != nil {
			t.Fatal(err)
		}
		v.batch.Entries = append(v.batch.Entries, batchEntry{ReleaseID: id, Channel: channel})
	}
	s.Close()
	data, _ := json.Marshal(v.batch)
	input := filepath.Join(t.TempDir(), "batch.json")
	if err = os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	v.batch, err = createBatch(v.root, manifest.Simulation, input)
	if err != nil {
		t.Fatal(err)
	}
	v.plan, err = reviewBatch(context.Background(), v.root, manifest.Simulation, v.batch, nil, v.approval, artifact.SimulationInspector{})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (v *batchTestEnvironment) runner(hook func(string, protocol.Request) error) batchExecutionRunner {
	return func(ctx context.Context, command string, entry batchEntry, intent, preflight string, out io.Writer) int {
		s, err := state.Open(v.root, manifest.Simulation, false)
		if err != nil {
			return 5
		}
		defer s.Close()
		e := &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: v.client, Hook: hook}
		var result engine.Summary
		if command == "reconcile" {
			result, err = e.Reconcile(ctx, entry.ReleaseID, nil)
		} else {
			result, err = e.Submit(ctx, entry.ReleaseID, nil)
		}
		encode(out, result)
		if err != nil {
			return 3
		}
		return 0
	}
}

func TestBatchApprovalIsOfflinePrivateAndImmutable(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
	data, err := os.ReadFile(v.approval)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "baseline_ref") || strings.Contains(string(data), "remote_ref") || strings.Contains(string(data), "credential_name") || !bytes.Contains(data, []byte("本次优化")) {
		t.Fatal("approval omitted public meaning or leaked private data", string(data))
	}
	info, _ := os.Stat(v.approval)
	if info.Mode().Perm() != 0600 {
		t.Fatal("approval is not private", info.Mode())
	}
	stats, err := v.client.Stats(context.Background())
	if err != nil || stats.Total != 0 {
		t.Fatal("review performed business writes", stats, err)
	}
	if _, err = reviewBatch(context.Background(), v.root, manifest.Simulation, v.batch, []string{"xiaomi"}, v.approval, artifact.SimulationInspector{}); err == nil {
		t.Fatal("review replaced an existing approval")
	}
	var preview bytes.Buffer
	if err = writeBatchReview(&preview, v.plan); err != nil || !strings.Contains(preview.String(), "审核通过后立即发布") || !strings.Contains(preview.String(), v.plan.PlanDigest) {
		t.Fatal("missing review preview", err)
	}
}

func TestBatchInvalidApprovalAndSubsetDispatchNothing(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
	calls := 0
	runner := func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; return 0 }
	for _, test := range []struct {
		digest string
		subset []string
	}{
		{"", nil}, {strings.Repeat("a", 64), nil}, {v.plan.PlanDigest, []string{"xiaomi"}}, {v.plan.PlanDigest, []string{"xiaomi", "xiaomi"}}, {v.plan.PlanDigest, []string{"honor"}},
	} {
		_, code := executeBatch(context.Background(), "submit", v.root, manifest.Simulation, v.batch, v.approval, test.digest, test.subset, artifact.SimulationInspector{}, runner)
		if code != 2 || calls != 0 {
			t.Fatal("unapproved batch dispatched", code, calls)
		}
	}
	for _, bad := range []string{"xiaomi,", ",vivo", "xiaomi, xiaomi", "xiaomi,xiaomi", "unknown"} {
		if _, err := parseBatchChannels(bad); err == nil {
			t.Fatal("invalid channels accepted", bad)
		}
	}
	if got, err := batchSelected(v.batch, []string{"vivo", "xiaomi"}); err != nil || !slices.Equal(got, []string{"xiaomi", "vivo"}) {
		t.Fatal("selection was not canonical", got, err)
	}
}

func TestBatchAllStaticInputsValidatedBeforeFirstDispatch(t *testing.T) {
	for _, change := range []string{"artifact", "preflight", "batch", "approval"} {
		t.Run(change, func(t *testing.T) {
			v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
			s, err := state.Open(v.root, manifest.Simulation, false)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "artifact":
				snap, _ := s.Load(v.batch.Entries[1].ReleaseID)
				os.WriteFile(artifact.Path(v.root, snap.Manifest.Artifacts["fixture"].SHA256), []byte("tampered"), 0600)
			case "preflight":
				s.DB.Exec("UPDATE targets SET preflight=replace(preflight,'metadata','other') WHERE release_id=?", v.batch.Entries[1].ReleaseID)
			case "batch":
				os.WriteFile(batchPath(v.root, v.batch.ID), []byte("{}"), 0600)
			case "approval":
				data, _ := os.ReadFile(v.approval)
				os.WriteFile(v.approval, bytes.ReplaceAll(data, []byte("本次优化"), []byte("未经审阅说明")), 0600)
			}
			s.Close()
			calls := 0
			_, code := executeBatch(context.Background(), "submit", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; return 0 })
			if code != 2 || calls != 0 {
				t.Fatal("stale later target allowed an earlier dispatch", code, calls)
			}
		})
	}
}

func TestBatchProductionChecksEveryConfigurationAndPreflightBeforeDispatch(t *testing.T) {
	for _, mutation := range []string{"config", "binding"} {
		t.Run(mutation, func(t *testing.T) {
			root, source, _ := oppoCLIPlan(t)
			notes := filepath.Join(t.TempDir(), "notes")
			if err := os.WriteFile(notes, []byte("本次更新说明"), 0600); err != nil {
				t.Fatal(err)
			}
			apk := source.Manifest.Artifacts["oppo"]
			input := prepareInput{SchemaVersion: 1, BatchID: "production-batch", PackageName: source.Manifest.PackageName, VersionCode: source.Manifest.VersionCode, VersionName: source.Manifest.VersionName, SignerSHA256: apk.SignerSHA256, NotesFile: notes, Channels: []prepareChannel{{Channel: "oppo", Account: "fixture", APK: artifact.Path(root, apk.SHA256), CredentialName: "fixture-secret"}, {Channel: "honor", Account: "fixture", APK: artifact.Path(root, apk.SHA256), CredentialName: "fixture-secret", ClientID: "fixture-client", LanguageID: "zh-CN"}}}
			data, _ := json.Marshal(input)
			file := filepath.Join(t.TempDir(), "prepare.json")
			os.WriteFile(file, data, 0600)
			b, err := prepareRelease(context.Background(), manifest.Production, root, file, artifact.SimulationInspector{})
			if err != nil {
				t.Fatal(err)
			}
			s, err := state.Open(root, manifest.Production, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range b.Entries {
				targets, _ := s.Targets(entry.ReleaseID)
				target := targets[0]
				binding := strings.Repeat("b", 64)
				if err = s.Preflight(target, protocol.RemoteSnapshot{Scope: target.Scope, AdapterBinding: binding, Metadata: map[string]string{"release_notes": "上次更新说明", "private_fixture": "must-not-export"}}); err != nil {
					t.Fatal(err)
				}
				var encoded []byte
				if entry.Channel == "oppo" {
					encoded, _ = json.Marshal(oppoBinding{SchemaVersion: 1, IntentDigest: entry.IntentDigest, Scope: target.Scope, CredentialName: entry.CredentialName, BaselineRef: "material-" + strings.Repeat("a", 64), AdapterBinding: binding})
				} else {
					encoded, _ = json.Marshal(fiveBinding{SchemaVersion: 1, IntentDigest: entry.IntentDigest, Scope: target.Scope, BaselineRef: "material-" + strings.Repeat("a", 64), AdapterBinding: binding})
				}
				if err = privatefs.Write(filepath.Join(root, "production", entry.Channel, entry.ReleaseID+".json"), encoded); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()
			approvalFile := filepath.Join(t.TempDir(), "approval.json")
			approval, err := reviewBatch(context.Background(), root, manifest.Production, b, nil, approvalFile, artifact.SimulationInspector{})
			if err != nil {
				t.Fatal(err)
			}
			data, _ = os.ReadFile(approvalFile)
			if bytes.Contains(data, []byte("must-not-export")) || bytes.Contains(data, []byte("private_fixture")) || !bytes.Contains(data, []byte("上次更新说明")) {
				t.Fatal("review did not whitelist its public diff")
			}
			if mutation == "config" {
				os.WriteFile(b.Entries[1].AccountConfig, []byte("{}"), 0600)
			} else {
				os.WriteFile(filepath.Join(root, "production", b.Entries[1].Channel, b.Entries[1].ReleaseID+".json"), []byte("{}"), 0600)
			}
			calls := 0
			_, code := executeBatch(context.Background(), "submit", root, manifest.Production, b, approvalFile, approval.PlanDigest, nil, artifact.SimulationInspector{}, func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; return 0 })
			if code != 2 || calls != 0 {
				t.Fatal("later changed production input allowed earlier dispatch", code, calls)
			}
		})
	}
}

type batchCancellingInspector struct{ cancel context.CancelFunc }

func (i batchCancellingInspector) Inspect(ctx context.Context, path string) (artifact.Info, error) {
	i.cancel()
	return (artifact.SimulationInspector{}).Inspect(ctx, path)
}

func TestBatchCancellationDuringApprovalValidationIsInterrupted(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	r, code := executeBatch(ctx, "submit", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, batchCancellingInspector{cancel}, func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; return 0 })
	if code != 130 || calls != 0 || r.SchemaVersion != 1 || r.ID != v.batch.ID {
		t.Fatal("interrupted verification was reported as invalid approval", r, code, calls)
	}
}

func TestBatchLostResponseRequiresExplicitReconcileThenSkipsAccepted(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{Faults: map[string]string{"vivo": "drop_submit_once"}})
	r, code := executeBatch(context.Background(), "submit", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, v.runner(nil))
	if code != 3 || r.Total != 2 || r.Accepted != 1 || r.AllAccepted {
		t.Fatal("lost response was accepted", r, code)
	}
	stats, _ := v.client.Stats(context.Background())
	before := stats.Total
	calls := 0
	r, code = executeBatch(context.Background(), "resume", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; return 0 })
	if code != 3 || calls != 0 || r.Results[0].Action != "already_accepted" || r.Results[1].NextAction != "reconcile_before_resume" {
		t.Fatal("unknown write was implicitly retried", r, code, calls)
	}
	r, code = executeBatch(context.Background(), "reconcile", v.root, manifest.Simulation, v.batch, "", "", []string{"vivo"}, artifact.SimulationInspector{}, v.runner(nil))
	if code != 0 || !r.AllAccepted || r.Total != 2 || r.AllApproved || r.AllLive {
		t.Fatal("read-only reconciliation lost global state", r, code)
	}
	r, code = executeBatch(context.Background(), "resume", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, v.runner(nil))
	stats, _ = v.client.Stats(context.Background())
	if code != 0 || !r.AllAccepted || stats.Total != before {
		t.Fatal("resume repeated accepted submission", r, code, stats.Total, before)
	}
}

func TestBatchIndependentFailureAndCancellationKeepGlobalSummary(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
	calls := 0
	r, code := executeBatch(context.Background(), "submit", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, func(ctx context.Context, command string, e batchEntry, intent, preflight string, out io.Writer) int {
		calls++
		if e.Channel == "xiaomi" {
			io.WriteString(out, "sensitive raw response")
			return 4
		}
		return v.runner(nil)(ctx, command, e, intent, preflight, out)
	})
	if calls != 2 || code != 4 || r.Total != 2 || r.Accepted != 1 {
		t.Fatal("failure stopped an independent target", r, code, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	r, code = executeBatch(ctx, "submit", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, func(context.Context, string, batchEntry, string, string, io.Writer) int { calls++; cancel(); return 0 })
	if code != 130 || calls != 1 || r.Total != 2 || r.Accepted != 1 {
		t.Fatal("cancellation discarded durable state or started another channel", r, code, calls)
	}
}

func TestBatchProcessWorker(t *testing.T) {
	if os.Getenv("PUBLISHER_BATCH_TEST_WORKER") != "1" {
		return
	}
	root, endpoint := os.Getenv("PUBLISHER_BATCH_TEST_ROOT"), os.Getenv("PUBLISHER_BATCH_TEST_ENDPOINT")
	b, err := readBatch(batchPath(root, "team-batch"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := simulation.NewClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	v := &batchTestEnvironment{root: root, client: client}
	ready, point := os.Getenv("PUBLISHER_BATCH_TEST_READY"), os.Getenv("PUBLISHER_BATCH_TEST_POINT")
	hook := func(current string, q protocol.Request) error {
		if current == point && q.Kind == "push" {
			if err := privatefs.Write(ready, []byte("ready")); err != nil {
				return err
			}
			select {}
		}
		return nil
	}
	_, code := executeBatch(context.Background(), "submit", root, manifest.Simulation, b, os.Getenv("PUBLISHER_BATCH_TEST_APPROVAL"), os.Getenv("PUBLISHER_BATCH_TEST_DIGEST"), nil, artifact.SimulationInspector{}, v.runner(hook))
	if code != 0 {
		t.Fatal(code)
	}
}

func TestBatchActualSIGKILLRecoveryAtIntentAndReceipt(t *testing.T) {
	for _, point := range []string{"after_intent", "after_receipt"} {
		t.Run(point, func(t *testing.T) {
			v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
			ready := filepath.Join(t.TempDir(), "checkpoint")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBatchProcessWorker$", "-test.timeout=14s")
			cmd.Env = append(os.Environ(), "PUBLISHER_BATCH_TEST_WORKER=1", "PUBLISHER_BATCH_TEST_ROOT="+v.root, "PUBLISHER_BATCH_TEST_ENDPOINT="+v.server.URL, "PUBLISHER_BATCH_TEST_APPROVAL="+v.approval, "PUBLISHER_BATCH_TEST_DIGEST="+v.plan.PlanDigest, "PUBLISHER_BATCH_TEST_POINT="+point, "PUBLISHER_BATCH_TEST_READY="+ready)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := false
			defer func() {
				if !done {
					cmd.Process.Kill()
					cmd.Wait()
				}
			}()
			deadline := time.Now().Add(8 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker missed durable checkpoint", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			cmd.Process.Kill()
			cmd.Wait()
			done = true
			if point == "after_intent" {
				_, code := executeBatch(context.Background(), "resume", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, v.runner(nil))
				if code != 3 {
					t.Fatal("unresolved intent did not block", code)
				}
				_, code = executeBatch(context.Background(), "reconcile", v.root, manifest.Simulation, v.batch, "", "", []string{"xiaomi"}, artifact.SimulationInspector{}, v.runner(nil))
				if code != 0 {
					t.Fatal("explicit reconciliation failed", code)
				}
			}
			r, code := executeBatch(context.Background(), "resume", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, v.runner(nil))
			stats, err := v.client.Stats(context.Background())
			if code != 0 || !r.AllAccepted || r.Total != 2 || err != nil || stats.Total != 4 || stats.Applied != 4 {
				t.Fatal("restart duplicated or lost a write", r, code, stats, err)
			}
			_, code = executeBatch(context.Background(), "resume", v.root, manifest.Simulation, v.batch, v.approval, v.plan.PlanDigest, nil, artifact.SimulationInspector{}, v.runner(nil))
			stats, _ = v.client.Stats(context.Background())
			if code != 0 || stats.Total != 4 || stats.Applied != 4 {
				t.Fatal("repeat resume duplicated a write", code, stats)
			}
		})
	}
}
