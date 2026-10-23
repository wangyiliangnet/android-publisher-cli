package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func oppoCLIPlan(t *testing.T) (string, manifest.Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	a, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "fixture.apk"), artifact.Info{PackageName: "com.example.oppo", VersionCode: 2, VersionName: "2.0", ABI: []string{"arm64-v8a"}})
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "oppo-cli-test", Mode: manifest.Production, PackageName: "com.example.oppo", VersionCode: 2, VersionName: "2.0", Artifacts: map[string]manifest.Artifact{"oppo": a}, NotesFile: "notes", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{"oppo": {AccountRef: "synthetic-client", AppRef: "123", Artifact: "oppo"}}}
	snapshot := manifest.Snapshot{Manifest: m, Notes: "合成更新说明\n- 保留用户输入"}
	snapshot.Digest = snapshot.CalculateDigest()
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.Targets(m.ReleaseID)
	return root, snapshot, targets[0].Scope
}

type cliOPPOPrepared struct {
	protocol.Adapter
	scope, intent, binding string
	writable               bool
}

func (p cliOPPOPrepared) ProductionScope() (string, string, bool) {
	return p.scope, p.intent, p.writable
}
func (p cliOPPOPrepared) BaselineRef() string                   { return "material-" + strings.Repeat("a", 64) }
func (p cliOPPOPrepared) CopyrightSource() string               { return "electronic_cert_url" }
func (p cliOPPOPrepared) AdapterBinding(string) (string, error) { return p.binding, nil }
func (p cliOPPOPrepared) BuildPlan(protocol.Request) ([]string, error) {
	return []string{"authorize", "upload", "submit"}, nil
}
func (p cliOPPOPrepared) Inspect(context.Context, string) (protocol.RemoteSnapshot, error) {
	return protocol.RemoteSnapshot{Scope: p.scope, AdapterBinding: p.binding, Metadata: map[string]string{"oppo_baseline_ref": p.BaselineRef()}}, nil
}

func TestOPPOProductionCLIPreflightAndApprovalBeforeCredentials(t *testing.T) {
	root, snapshot, scope := oppoCLIPlan(t)
	var out bytes.Buffer
	calls := 0
	factory := func(_ context.Context, in channels.OPPOProductionInput, ref string, writable bool) (oppoPrepared, error) {
		calls++
		if in.Snapshot.Digest != snapshot.Digest || in.Scope != scope || in.CredentialName != "selected-secret" || ref != "" || writable {
			t.Fatal("preflight did not bind frozen identity/credential")
		}
		return cliOPPOPrepared{scope: scope, intent: snapshot.Digest, binding: strings.Repeat("b", 64)}, nil
	}
	code := oppoProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, "selected-secret", "", "", nil, artifact.SimulationInspector{}, factory, &out)
	if code != 0 || calls != 1 || !strings.Contains(out.String(), `"execution_phase":"ready"`) || !strings.Contains(out.String(), `"publishing_permission":"not_verified"`) || !strings.Contains(out.String(), `"copyright_proof":{"request_field":"copyright_url","source_field":"electronic_cert_url"}`) {
		t.Fatalf("preflight: %d %s", code, out.String())
	}
	var binding oppoBinding
	path := filepath.Join(root, "production/oppo", snapshot.Manifest.ReleaseID+".json")
	b, err := privatefs.Read(path, 8192)
	if err != nil || json.Unmarshal(b, &binding) != nil || binding.CredentialName != "selected-secret" {
		t.Fatal("binding not persisted")
	}
	for _, pair := range [][2]string{{"", ""}, {snapshot.Digest, ""}, {"changed-intent", binding.AdapterBinding}, {snapshot.Digest, strings.Repeat("c", 64)}} {
		for _, command := range []string{"submit", "resume"} {
			out.Reset()
			code = oppoProductionCommand(context.Background(), command, root, snapshot.Manifest.ReleaseID, "", pair[0], pair[1], nil, artifact.SimulationInspector{}, func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error) {
				t.Fatal("unapproved command accessed credentials or network")
				return nil, nil
			}, &out)
			if code != 2 || !strings.Contains(out.String(), `"effect":"not_sent"`) {
				t.Fatalf("missing approval not local rejection: %d %s", code, out.String())
			}
		}
	}
	// The exact two approvals reach the factory with the persisted baseline;
	// no arbitrary credential alias or new preflight is selected implicitly.
	out.Reset()
	code = oppoProductionCommand(context.Background(), "submit", root, snapshot.Manifest.ReleaseID, "", snapshot.Digest, binding.AdapterBinding, nil, artifact.SimulationInspector{}, func(_ context.Context, in channels.OPPOProductionInput, ref string, writable bool) (oppoPrepared, error) {
		if !writable || ref != binding.BaselineRef || in.CredentialName != "selected-secret" {
			t.Fatal("approved submission lost frozen binding")
		}
		return nil, errors.New("synthetic stop before credentials; sensitive=https://fixture.invalid/secret")
	}, &out)
	if code != 3 || strings.Contains(out.String(), "fixture.invalid") || strings.Contains(out.String(), "sensitive") {
		t.Fatal("unexpected factory error leaked")
	}
}

func TestOPPOProductionCLIFrozenArtifactAndMissingMetadata(t *testing.T) {
	root, snapshot, _ := oppoCLIPlan(t)
	var out bytes.Buffer
	code := oppoProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, "client-secret", "", "", nil, artifact.SimulationInspector{}, func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error) {
		return nil, &channels.OPPOMetadataError{Missing: []string{"test_desc", "copyright_url"}}
	}, &out)
	if code != 2 || !strings.Contains(out.String(), `"missing_fields":["test_desc","copyright_url"]`) {
		t.Fatalf("missing fields not actionable: %d %s", code, out.String())
	}
	for _, a := range snapshot.Manifest.Artifacts {
		if err := os.WriteFile(artifact.Path(root, a.SHA256), []byte("changed frozen artifact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	code = oppoProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, "client-secret", "", "", nil, artifact.SimulationInspector{}, func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error) {
		t.Fatal("changed APK accessed credentials/network")
		return nil, nil
	}, &out)
	if code != 2 {
		t.Fatal("changed frozen APK accepted")
	}
}

func TestOPPOProductionFlagsCannotEnableOtherModesOrStateRoots(t *testing.T) {
	for _, args := range [][]string{
		{"simulate", "preflight", "--release", "test", "--credential-name", "name"},
		{"plan", "--manifest", "unused", "--approve-intent", "digest"},
		{"--data-dir", filepath.Join(t.TempDir(), "absent"), "preflight", "--release", "test"},
		{"watch", "--release", "test"},
		{"preflight", "--release", "test", "--store", "http://127.0.0.1:1"},
		{"submit", "--release", "test", "--replace-unused-upload-authorization", strings.Repeat("a", 64)},
		{"preflight", "--release", "test", "--replace-unused-upload-authorization", strings.Repeat("a", 64)},
		{"simulate", "resume", "--release", "test", "--replace-unused-upload-authorization", strings.Repeat("a", 64)},
		{"resume", "--release", "test", "--replace-unused-upload-authorization", "bad-proof"},
	} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr); code != 2 {
			t.Fatalf("unsafe flags accepted: %v code %d", args, code)
		}
	}
}

func TestOPPOReplacementStillRequiresBothFrozenApprovals(t *testing.T) {
	root, snapshot, scope := oppoCLIPlan(t)
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	factory := func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error) {
		return cliOPPOPrepared{scope: scope, intent: snapshot.Digest, binding: binding}, nil
	}
	if code := oppoProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, "client-secret", "", "", nil, artifact.SimulationInspector{}, factory, &out); code != 0 {
		t.Fatal(out.String())
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	targets, _ := s.Targets(snapshot.Manifest.ReleaseID)
	var baseline protocol.RemoteSnapshot
	json.Unmarshal([]byte(targets[0].Preflight), &baseline)
	q := protocol.Request{OperationID: protocol.Digest([]string{snapshot.Digest, "oppo", "authorize"}), TargetID: targets[0].ID, Scope: scope, Channel: "oppo", IntentDigest: snapshot.Digest, Kind: "authorize", PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, ArtifactSHA256: snapshot.Manifest.Artifacts["oppo"].SHA256, SnapshotDigest: protocol.Digest(baseline.Metadata), AdapterBinding: binding, Metadata: map[string]string{"oppo_baseline_ref": baseline.Metadata["oppo_baseline_ref"], "release_notes": snapshot.Notes}, Policy: snapshot.Manifest.ReleasePolicy}
	if err = s.Begin(q); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(q, "unknown", "", "synthetic response lost"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, pair := range [][2]string{{"", ""}, {snapshot.Digest, ""}, {snapshot.Digest, strings.Repeat("c", 64)}} {
		out.Reset()
		code := oppoProductionCommand(context.Background(), "resume", root, snapshot.Manifest.ReleaseID, "", pair[0], pair[1], nil, artifact.SimulationInspector{}, func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error) {
			t.Fatal("unapproved replacement accessed credentials")
			return nil, nil
		}, &out, q.OperationID)
		if code != 2 {
			t.Fatalf("approval bypass: %d %s", code, out.String())
		}
	}
}
