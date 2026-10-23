package engine_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

// This fixture tests the production kernel boundary without vendor HTTP or
// Keychain. Its remote state deliberately changes at bind, before metadata.
type fiveKernelRemote struct {
	metadata    map[string]string
	calls       []string
	proofs      map[string]protocol.Receipt
	inspections int
}

type fiveKernelAdapter struct {
	t                   *testing.T
	store               *state.Store
	remote              *fiveKernelRemote
	scope, intent       string
	writable            bool
	steps               []string
	baselineDigest      string
	allowReconciliation bool
	advanced            []string
}

func (a *fiveKernelAdapter) ProductionScope() (string, string, bool) {
	return a.scope, a.intent, a.writable
}
func (a *fiveKernelAdapter) Inspect(_ context.Context, scope string) (protocol.RemoteSnapshot, error) {
	a.remote.inspections++
	return protocol.RemoteSnapshot{Scope: scope, AdapterBinding: protocol.Digest("synthetic five kernel binding"), Metadata: copyFiveKernelMetadata(a.remote.metadata)}, nil
}
func (a *fiveKernelAdapter) BuildPlan(protocol.Request) ([]string, error) {
	return append([]string(nil), a.steps...), nil
}
func (a *fiveKernelAdapter) ExecuteOne(_ context.Context, q protocol.Request) (protocol.Receipt, error) {
	op, err := a.store.Operation(q.TargetID, q.Kind)
	if err != nil || op == nil || op.Effect != "intent" || protocol.Digest(op.Request) != protocol.Digest(q) {
		a.t.Fatal("synthetic write preceded its exact durable intent")
	}
	if q.SnapshotDigest != a.baselineDigest || q.Metadata["generation"] != "original" || q.Metadata["bound"] != "" || q.Metadata["notes_updated"] != "" || q.Metadata["release_notes"] != "Frozen synthetic notes\n- Preserve this line" {
		a.t.Fatal("operation was rebuilt from a mutable or advanced preflight")
	}
	a.remote.calls = append(a.remote.calls, q.Kind)
	switch q.Kind {
	case "bind":
		a.remote.metadata["bound"] = q.OperationID
	case "metadata":
		a.remote.metadata["notes_updated"] = q.OperationID
	}
	r := protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: "synthetic-ref-" + q.Kind}
	a.remote.proofs[q.OperationID] = r
	return r, nil
}
func (a *fiveKernelAdapter) ExpectedSnapshot(q protocol.Request, before protocol.RemoteSnapshot) (protocol.RemoteSnapshot, error) {
	if before.Scope != q.Scope || before.AdapterBinding != protocol.Digest("synthetic five kernel binding") || q.SnapshotDigest != a.baselineDigest {
		return protocol.RemoteSnapshot{}, errors.New("synthetic snapshot binding mismatch")
	}
	a.advanced = append(a.advanced, q.Kind)
	after := before
	after.Metadata = copyFiveKernelMetadata(before.Metadata)
	switch q.Kind {
	case "bind":
		after.Metadata["bound"] = q.OperationID
	case "metadata":
		after.Metadata["notes_updated"] = q.OperationID
	}
	return after, nil
}
func (a *fiveKernelAdapter) Reconcile(_ context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	r := protocol.Reconciliation{OperationID: q.OperationID, RequestDigest: protocol.Digest(q), Result: "inconclusive"}
	if proof, ok := a.remote.proofs[q.OperationID]; a.allowReconciliation && ok {
		r.Result, r.Receipt = "confirmed_applied", &proof
	}
	return r, nil
}
func (*fiveKernelAdapter) Observe(context.Context, string, int64, string) (protocol.Observation, error) {
	return protocol.Observation{}, errors.New("synthetic observation unavailable")
}

func copyFiveKernelMetadata(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type fiveKernelFixture struct {
	engine   *engine.Engine
	adapter  *fiveKernelAdapter
	root, id string
}

func newFiveKernelFixture(t *testing.T, channels ...string) *fiveKernelFixture {
	t.Helper()
	root := t.TempDir()
	f, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "synthetic.apk"), artifact.Info{PackageName: "com.example.fivekernel", VersionCode: 248, VersionName: "3.2.7", ABI: []string{"arm64-v8a"}})
	must(t, err)
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "five-kernel-release", Mode: manifest.Production, PackageName: "com.example.fivekernel", VersionCode: 248, VersionName: "3.2.7", Artifacts: map[string]manifest.Artifact{"apk": f}, NotesFile: "notes", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{}}
	for _, channel := range channels {
		m.Targets[channel] = manifest.Target{AccountRef: "synthetic-account", AppRef: m.PackageName, Artifact: "apk"}
	}
	snapshot := manifest.Snapshot{Manifest: m, Notes: "Frozen synthetic notes\n- Preserve this line"}
	snapshot.Digest = snapshot.CalculateDigest()
	must(t, artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}))
	s, err := state.Open(root, manifest.Production, false)
	must(t, err)
	_, err = s.Save(snapshot)
	must(t, err)
	targets, err := s.Targets(m.ReleaseID)
	must(t, err)
	metadata := map[string]string{"generation": "original"}
	a := &fiveKernelAdapter{t: t, store: s, remote: &fiveKernelRemote{metadata: metadata, proofs: map[string]protocol.Receipt{}}, scope: targets[0].Scope, intent: snapshot.Digest, writable: true, steps: []string{"submit"}, baselineDigest: protocol.Digest(metadata)}
	if len(channels) == 1 && channels[0] == "xiaomi" {
		a.steps = []string{"push"}
	}
	e := &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: a}
	v := &fiveKernelFixture{engine: e, adapter: a, root: root, id: m.ReleaseID}
	t.Cleanup(func() { _ = v.engine.Store.Close() })
	return v
}

func (v *fiveKernelFixture) preflight(t *testing.T) {
	t.Helper()
	_, err := v.engine.Preflight(context.Background(), v.id, nil)
	must(t, err)
}
func (v *fiveKernelFixture) reopen(t *testing.T) {
	t.Helper()
	must(t, v.engine.Store.Close())
	s, err := state.Open(v.root, manifest.Production, false)
	must(t, err)
	// Reconstruct the adapter and engine, preserving only durable state and the
	// separate synthetic remote. No in-memory expected snapshot survives.
	previous := v.adapter
	v.adapter = &fiveKernelAdapter{t: t, store: s, remote: previous.remote, scope: previous.scope, intent: previous.intent, writable: previous.writable, steps: append([]string(nil), previous.steps...), baselineDigest: previous.baselineDigest, allowReconciliation: previous.allowReconciliation}
	v.engine = &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: v.adapter}
}

func TestFiveProductionKernelAcceptsOnlyExactWritableScope(t *testing.T) {
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			v := newFiveKernelFixture(t, channel)
			v.preflight(t)
			result, err := v.engine.Submit(context.Background(), v.id, nil)
			must(t, err)
			if !result.AllAccepted || result.AllApproved || result.AllLive || len(v.adapter.remote.calls) != 1 {
				t.Fatalf("wrong production acceptance state: %+v", result)
			}
			v.reopen(t)
			_, err = v.engine.Submit(context.Background(), v.id, nil)
			must(t, err)
			if len(v.adapter.remote.calls) != 1 {
				t.Fatal("completed production operation was repeated")
			}
		})
	}
}

// Embedding only the Adapter contract deliberately omits ProductionScope.
type fiveUnscopedAdapter struct{ protocol.Adapter }

func TestFiveProductionKernelRejectsInvalidScopeBeforeInspectionOrIntent(t *testing.T) {
	for _, variant := range []string{"missing-provider", "read-only", "scope", "intent", "unsupported-channel", "multiple-targets"} {
		t.Run(variant, func(t *testing.T) {
			channels := []string{"honor"}
			if variant == "multiple-targets" {
				channels = append(channels, "huawei")
			}
			v := newFiveKernelFixture(t, channels...)
			if variant != "multiple-targets" {
				v.preflight(t)
			}
			switch variant {
			case "missing-provider":
				v.engine.Adapter = fiveUnscopedAdapter{v.adapter}
			case "read-only":
				v.adapter.writable = false
			case "scope":
				v.adapter.scope = "synthetic-wrong-scope"
			case "intent":
				v.adapter.intent = protocol.Digest("synthetic-other-intent")
			case "unsupported-channel":
				_, err := v.engine.Store.DB.Exec("UPDATE targets SET channel='unsupported'")
				must(t, err)
			}
			beforeInspections := v.adapter.remote.inspections
			if _, err := v.engine.Submit(context.Background(), v.id, nil); err == nil {
				t.Fatal("invalid production scope was accepted")
			}
			ops, err := v.engine.Store.OperationStatuses(v.id)
			must(t, err)
			if len(ops) != 0 || len(v.adapter.remote.calls) != 0 || v.adapter.remote.inspections != beforeInspections {
				t.Fatal("production gate failure inspected remote data, persisted or sent a business intent")
			}
		})
	}
}

func TestFiveProductionKernelAdvancesConfirmedBindAcrossJournalResume(t *testing.T) {
	for _, variant := range []string{"uninterrupted", "saved-receipt", "reconciled-receipt", "unknown-bind"} {
		t.Run(variant, func(t *testing.T) {
			v := newFiveKernelFixture(t, "honor")
			v.adapter.steps = []string{"authorize", "upload", "bind", "metadata", "submit"}
			v.preflight(t)
			initial, err := v.engine.Store.Target(v.id + "/honor")
			must(t, err)
			if variant != "uninterrupted" {
				v.engine.Hook = func(point string, q protocol.Request) error {
					want := "after_receipt"
					if variant != "saved-receipt" {
						want = "after_execute"
					}
					if point == want && q.Kind == "bind" {
						return errors.New("synthetic interruption after binding")
					}
					return nil
				}
				if _, err = v.engine.Submit(context.Background(), v.id, nil); err == nil {
					t.Fatal("fault did not interrupt the binding")
				}
				bind, err := v.engine.Store.Operation(v.id+"/honor", "bind")
				must(t, err)
				originalDigest := protocol.Digest(bind.Request)
				v.adapter.allowReconciliation = variant == "reconciled-receipt"
				v.reopen(t)
				result, resumeErr := v.engine.Submit(context.Background(), v.id, nil)
				if variant == "unknown-bind" {
					if resumeErr == nil || result.AllAccepted || !reflect.DeepEqual(v.adapter.remote.calls, []string{"authorize", "upload", "bind"}) || !reflect.DeepEqual(v.adapter.advanced, []string{"authorize", "upload"}) {
						t.Fatal("unknown bind advanced the snapshot or replayed a write")
					}
					for i := 0; i < 2; i++ {
						if _, err = v.engine.Reconcile(context.Background(), v.id, nil); err == nil {
							t.Fatal("inconclusive binding proof became definitive")
						}
						if _, err = v.engine.Submit(context.Background(), v.id, nil); err == nil {
							t.Fatal("unknown binding was retried")
						}
					}
					if !reflect.DeepEqual(v.adapter.remote.calls, []string{"authorize", "upload", "bind"}) {
						t.Fatal("repeated reconciliation sent another business write")
					}
				} else {
					must(t, resumeErr)
					if !result.AllAccepted || !reflect.DeepEqual(v.adapter.remote.calls, v.adapter.steps) || !reflect.DeepEqual(v.adapter.advanced, v.adapter.steps) {
						t.Fatal("confirmed bind was not advanced exactly once during reconstruction")
					}
				}
				bind, err = v.engine.Store.Operation(v.id+"/honor", "bind")
				must(t, err)
				if protocol.Digest(bind.Request) != originalDigest || bind.Attempt != 1 {
					t.Fatal("resume modified or retried the immutable binding request")
				}
			} else {
				result, err := v.engine.Submit(context.Background(), v.id, nil)
				must(t, err)
				if !result.AllAccepted || !reflect.DeepEqual(v.adapter.advanced, v.adapter.steps) || !reflect.DeepEqual(v.adapter.remote.calls, v.adapter.steps) {
					t.Fatal("fresh bind receipt did not advance before metadata inspection")
				}
			}
			current, err := v.engine.Store.Target(v.id + "/honor")
			must(t, err)
			if current.Preflight != initial.Preflight {
				t.Fatal("expected snapshot advancement rewrote frozen preflight")
			}
		})
	}
}
