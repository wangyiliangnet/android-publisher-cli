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
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func fiveCLIPlan(t *testing.T, channel string) (string, manifest.Snapshot, string, string) {
	t.Helper()
	root, snapshot, _ := oppoCLIPlan(t)
	snapshot.Manifest.ReleaseID = channel + "-cli-test"
	snapshot.Manifest.Targets = map[string]manifest.Target{channel: {AccountRef: "fixture", AppRef: snapshot.Manifest.PackageName, Artifact: "oppo"}}
	snapshot.Digest = snapshot.CalculateDigest()
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.Targets(snapshot.Manifest.ReleaseID)
	s.Close()
	config := channels.PreparationConfig{AccountQuery: channels.AccountQuery{Channel: channel, Account: "fixture", ClientID: "synthetic-id", SecretName: "synthetic-secret", PackageName: snapshot.Manifest.PackageName, AppID: "123"}}
	file := filepath.Join(t.TempDir(), "config.json")
	b, _ := json.Marshal(config)
	if err = os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	return root, snapshot, targets[0].Scope, file
}

type cliFivePrepared struct {
	scope, intent, binding string
	writable               bool
	writes, observes       *int
	writeErr               error
}

func (p cliFivePrepared) ProductionScope() (string, string, bool) {
	return p.scope, p.intent, p.writable
}
func (p cliFivePrepared) BaselineRef() string                   { return "material-" + strings.Repeat("a", 64) }
func (p cliFivePrepared) AdapterBinding(string) (string, error) { return p.binding, nil }
func (p cliFivePrepared) BuildPlan(protocol.Request) ([]string, error) {
	return []string{"submit"}, nil
}
func (p cliFivePrepared) Inspect(context.Context, string) (protocol.RemoteSnapshot, error) {
	return protocol.RemoteSnapshot{Scope: p.scope, AdapterBinding: p.binding, Metadata: map[string]string{"existing_baseline_ref": p.BaselineRef()}}, nil
}
func (p cliFivePrepared) ExecuteOne(_ context.Context, q protocol.Request) (protocol.Receipt, error) {
	*p.writes++
	if !p.writable {
		return protocol.Receipt{}, errors.New("read-only adapter used for a write")
	}
	if p.writeErr != nil {
		return protocol.Receipt{}, p.writeErr
	}
	return protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: "synthetic-release"}, nil
}
func (p cliFivePrepared) Reconcile(_ context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	return protocol.Reconciliation{OperationID: q.OperationID, RequestDigest: protocol.Digest(q), Result: "inconclusive"}, nil
}
func (p cliFivePrepared) Observe(_ context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	*p.observes++
	return protocol.Observation{Scope: scope, AdapterBinding: p.binding, VersionCode: version, RemoteRef: ref, Review: "in_review", Availability: "unknown", Source: "synthetic", CollectedAt: time.Now().UTC()}, nil
}

func TestFiveProductionCLIApprovalAndSavedBaseline(t *testing.T) {
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			root, snapshot, scope, config := fiveCLIPlan(t, channel)
			var out bytes.Buffer
			binding := strings.Repeat("b", 64)
			calls, writes, observations := 0, 0, 0
			factory := func(_ context.Context, in channels.PreparationInput, ref string, writable bool) (fivePrepared, error) {
				calls++
				if in.Snapshot.Digest != snapshot.Digest || in.Scope != scope || in.Config.Channel != channel || in.Config.SecretName != "synthetic-secret" || (calls == 1 && (ref != "" || writable)) || (calls > 1 && ref != "material-"+strings.Repeat("a", 64)) {
					t.Fatal("factory lost frozen scope or private baseline")
				}
				return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writable: writable, writes: &writes, observes: &observations}, nil
			}
			invoke := func(command, intent, approved string) int {
				out.Reset()
				return fiveProductionCommand(context.Background(), command, root, snapshot.Manifest.ReleaseID, config, intent, approved, nil, artifact.SimulationInspector{}, factory, &out)
			}
			if code := invoke("preflight", "", ""); code != 0 || calls != 1 || writes != 0 || !strings.Contains(out.String(), `"business_writes_enabled":false`) {
				t.Fatalf("preflight: %d %s", code, &out)
			}
			// Legacy preparation bindings have this flag false. It is not an
			// approval; even a locally edited true value must grant nothing.
			path := filepath.Join(root, "production", channel, snapshot.Manifest.ReleaseID+".json")
			b, err := privatefs.Read(path, 8192)
			if err != nil {
				t.Fatal(err)
			}
			var saved fiveBinding
			if json.Unmarshal(b, &saved) != nil || saved.BusinessWritesEnabled {
				t.Fatal("preflight binding did not retain read-only semantics")
			}
			saved.BusinessWritesEnabled = true
			b, _ = json.Marshal(saved)
			if err = privatefs.Write(path, b); err != nil {
				t.Fatal(err)
			}
			for _, pair := range [][2]string{{"", ""}, {snapshot.Digest, ""}, {strings.Repeat("c", 64), binding}, {snapshot.Digest, strings.Repeat("c", 64)}} {
				for _, command := range []string{"submit", "resume"} {
					if code := invoke(command, pair[0], pair[1]); code != 2 || calls != 1 || writes != 0 {
						t.Fatalf("approval bypass: %d %s", code, &out)
					}
				}
			}
			if code := invoke("submit", snapshot.Digest, binding); code != 0 || writes != 1 || !strings.Contains(out.String(), `"publishing_permission":"verified_for_this_release"`) {
				t.Fatalf("approved submit: %d %s", code, &out)
			}
			if code := invoke("resume", snapshot.Digest, binding); code != 0 || writes != 1 {
				t.Fatalf("completed resume resent: %d %s", code, &out)
			}
			if code := invoke("status", "", ""); code != 0 || observations != 1 || writes != 1 {
				t.Fatalf("read-only observation: %d %s", code, &out)
			}
			priorCalls := calls
			if code := invoke("preflight", "", ""); code != 2 || calls != priorCalls {
				t.Fatalf("baseline reset after write: %d %s", code, &out)
			}
		})
	}
}

func TestFiveProductionCLIUnknownWriteCannotReplay(t *testing.T) {
	root, snapshot, scope, config := fiveCLIPlan(t, "vivo")
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	writes, observations := 0, 0
	factory := func(_ context.Context, _ channels.PreparationInput, _ string, writable bool) (fivePrepared, error) {
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writable: writable, writes: &writes, observes: &observations, writeErr: errors.New("response lost; secret=https://synthetic.invalid/token")}, nil
	}
	invoke := func(command, intent, approved string) int {
		out.Reset()
		return fiveProductionCommand(context.Background(), command, root, snapshot.Manifest.ReleaseID, config, intent, approved, nil, artifact.SimulationInspector{}, factory, &out)
	}
	if invoke("preflight", "", "") != 0 || invoke("submit", snapshot.Digest, binding) != 3 || writes != 1 {
		t.Fatal(&out)
	}
	if strings.Contains(out.String(), "synthetic.invalid") || strings.Contains(out.String(), "secret=") {
		t.Fatal("raw adapter error leaked")
	}
	if invoke("reconcile", "", "") != 3 || invoke("resume", snapshot.Digest, binding) != 3 || writes != 1 {
		t.Fatalf("unknown write replayed: count %d %s", writes, &out)
	}
}

func TestFiveProductionCLIRejectsChangedBindingBeforeBusinessCall(t *testing.T) {
	root, snapshot, scope, config := fiveCLIPlan(t, "honor")
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	writes, observations := 0, 0
	factory := func(_ context.Context, _ channels.PreparationInput, _ string, writable bool) (fivePrepared, error) {
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writable: writable, writes: &writes, observes: &observations}, nil
	}
	if fiveProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, factory, &out) != 0 {
		t.Fatal(&out)
	}
	binding = strings.Repeat("c", 64)
	out.Reset()
	if code := fiveProductionCommand(context.Background(), "submit", root, snapshot.Manifest.ReleaseID, config, snapshot.Digest, strings.Repeat("b", 64), nil, artifact.SimulationInspector{}, factory, &out); code != 2 || writes != 0 {
		t.Fatalf("changed binding accepted: %d %s", code, &out)
	}
}

func TestFiveProductionCLIStrictPrivateConfiguration(t *testing.T) {
	for _, data := range []string{
		`{"channel":"honor","channel":"vivo"}`,
		`{"credential_name":"one","credential_name":"two"}`,
		`{"channel":null}`,
		`{"channel":"honor","nested":{}}`,
		`{"channel":"honor"} {}`,
		`{"channel":"honor","unexpected":"value"}`,
	} {
		var config channels.PreparationConfig
		if decodeFiveConfiguration([]byte(data), &config) == nil {
			t.Fatalf("ambiguous or unknown configuration accepted: %s", data)
		}
	}
}

func TestFiveProductionCLIRoutesOnlineCommandsAndRejectsOPPOFlags(t *testing.T) {
	for _, command := range []string{"preflight", "submit", "resume", "reconcile", "status"} {
		args := []string{command, "--release", "synthetic-does-not-exist", "--account-config", filepath.Join(t.TempDir(), "absent.json")}
		if command == "status" {
			args = append(args, "--refresh")
		}
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr); code != 2 || !strings.Contains(out.String(), "invalid private account configuration") {
			t.Fatalf("five command routed outside private config validation: %s %d %s", command, code, &out)
		}
	}
	for _, extra := range [][]string{
		{"--credential-name", "synthetic"},
		{"--replace-unused-upload-authorization", strings.Repeat("a", 64)},
	} {
		var out, stderr bytes.Buffer
		args := append([]string{"resume", "--release", "synthetic", "--account-config", "unused"}, extra...)
		if code := Run(context.Background(), args, &out, &stderr); code != 2 || !strings.Contains(out.String(), "cannot be combined with OPPO") {
			t.Fatalf("mixed production scope accepted: %d %s", code, &out)
		}
	}
}

func TestFiveConfigurationRejectsCaseAliases(t *testing.T) {
	for _, key := range []string{"channel", "account", "client_id", "credential_name", "public_key_name", "package_name", "app_id", "language_id", "icon_path", "privacy_url"} {
		for _, data := range []string{
			`{"` + strings.ToUpper(key) + `":"shadow"}`,
			`{"` + key + `":"reviewed","` + strings.ToUpper(key) + `":"shadow"}`,
		} {
			var config channels.PreparationConfig
			if decodeFiveConfiguration([]byte(data), &config) == nil {
				t.Fatalf("account config case alias accepted: %s", data)
			}
		}
	}
	for key, scalar := range map[string]string{"schema_version": "1", "intent_digest": `"reviewed"`, "scope": `"reviewed"`, "baseline_ref": `"reviewed"`, "adapter_binding": `"reviewed"`, "business_writes_enabled": "false"} {
		var canonical fiveBinding
		if decodeFiveConfiguration([]byte(`{"`+key+`":`+scalar+`}`), &canonical) != nil {
			t.Fatalf("canonical preflight field rejected: %s", key)
		}
		for _, data := range []string{
			`{"` + strings.ToUpper(key) + `":` + scalar + `}`,
			`{"` + key + `":` + scalar + `,"` + strings.ToUpper(key) + `":` + scalar + `}`,
		} {
			var binding fiveBinding
			if decodeFiveConfiguration([]byte(data), &binding) == nil {
				t.Fatalf("preflight binding case alias accepted: %s", data)
			}
		}
	}
	// Mixed casing reproduces encoding/json's semantic-duplicate behavior.
	for _, data := range []string{`{"channel":"honor","Channel":"vivo"}`, `{"intent_digest":"reviewed","Intent_Digest":"shadow"}`} {
		var config channels.PreparationConfig
		var binding fiveBinding
		if decodeFiveConfiguration([]byte(data), &config) == nil || decodeFiveConfiguration([]byte(data), &binding) == nil {
			t.Fatalf("semantic duplicate accepted: %s", data)
		}
	}
}

func TestFiveProductionCLIRejectsCaseAliasesBeforeFactory(t *testing.T) {
	root, snapshot, scope, configFile := fiveCLIPlan(t, "honor")
	original, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	calls := 0
	factory := func(_ context.Context, _ channels.PreparationInput, _ string, writable bool) (fivePrepared, error) {
		calls++
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writable: writable}, nil
	}
	invoke := func(command, intent, approved string) int {
		out.Reset()
		return fiveProductionCommand(context.Background(), command, root, snapshot.Manifest.ReleaseID, configFile, intent, approved, nil, artifact.SimulationInspector{}, factory, &out)
	}
	for _, suffix := range []string{`,"Channel":"honor"}`, `,"CHANNEL":"vivo"}`} {
		aliased := append(append([]byte{}, original[:len(original)-1]...), []byte(suffix)...)
		if err = os.WriteFile(configFile, aliased, 0600); err != nil {
			t.Fatal(err)
		}
		if code := invoke("preflight", "", ""); code != 2 || calls != 0 {
			t.Fatalf("account alias reached factory: %d %s", code, &out)
		}
	}
	if err = os.WriteFile(configFile, original, 0600); err != nil {
		t.Fatal(err)
	}
	if invoke("preflight", "", "") != 0 || calls != 1 {
		t.Fatal(&out)
	}
	path := filepath.Join(root, "production", "honor", snapshot.Manifest.ReleaseID+".json")
	original, err = privatefs.Read(path, 8192)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Intent_Digest", "INTENT_DIGEST", "Scope", "Adapter_Binding"} {
		aliased := append(append([]byte{}, original[:len(original)-1]...), []byte(`,"`+key+`":"shadow"}`)...)
		if err = privatefs.Write(path, aliased); err != nil {
			t.Fatal(err)
		}
		if code := invoke("submit", snapshot.Digest, binding); code != 2 || calls != 1 {
			t.Fatalf("preflight alias reached factory: %d %s", code, &out)
		}
	}
}
