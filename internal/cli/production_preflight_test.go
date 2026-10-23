package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func TestPreparedPreflightRejectsMissingIdentityAndChangedAPKBeforeCredentials(t *testing.T) {
	root, old, _ := oppoCLIPlan(t)
	snapshot := old
	snapshot.Manifest.ReleaseID = "prepared-cli-test"
	snapshot.Manifest.Targets = map[string]manifest.Target{"tencent": {AccountRef: "fixture", AppRef: old.Manifest.PackageName, Artifact: "oppo"}}
	snapshot.Digest = snapshot.CalculateDigest()
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	s.Close()
	cfg := channels.PreparationConfig{AccountQuery: channels.AccountQuery{Channel: "tencent", Account: "fixture", ClientID: "fixture-uid", SecretName: "secret", PackageName: old.Manifest.PackageName}}
	file := filepath.Join(t.TempDir(), "config.json")
	write := func() {
		b, _ := json.Marshal(cfg)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	var out bytes.Buffer
	code := runPreparedPreflight(context.Background(), root, snapshot.Manifest.ReleaseID, file, false, nil, artifact.SimulationInspector{}, &out)
	if code != 2 || !strings.Contains(out.String(), `"missing_fields":["app_id"]`) {
		t.Fatalf("missing AppID: %d %s", code, out.String())
	}
	cfg.AppID = "123"
	cfg.Account = "wrong-account"
	write()
	out.Reset()
	if code = runPreparedPreflight(context.Background(), root, snapshot.Manifest.ReleaseID, file, false, nil, artifact.SimulationInspector{}, &out); code != 2 || !strings.Contains(out.String(), "invalid private account") {
		t.Fatal("account mismatch was not rejected")
	}
	cfg.Account = "fixture"
	write()
	for _, a := range snapshot.Manifest.Artifacts {
		if err = os.WriteFile(artifact.Path(root, a.SHA256), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out.Reset()
	code = runPreparedPreflight(context.Background(), root, snapshot.Manifest.ReleaseID, file, false, nil, artifact.SimulationInspector{}, &out)
	if code != 2 || !strings.Contains(out.String(), "frozen APK verification failed") {
		t.Fatalf("changed APK: %d %s", code, out.String())
	}
}

func TestPreparedPreflightFlagsCannotEnableWritesOrAlternateLedger(t *testing.T) {
	for _, args := range [][]string{
		{"simulate", "preflight", "--release", "fixture", "--account-config", "unused"},
		{"plan", "--manifest", "unused", "--account-config", "unused"},
		{"submit", "--release", "fixture", "--account-config", "unused"},
		{"preflight", "--release", "fixture", "--account-config", "unused", "--approve-intent", "digest"},
		{"--data-dir", t.TempDir(), "preflight", "--release", "fixture", "--account-config", "unused"},
	} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr); code != 2 {
			t.Fatalf("unsafe flags accepted: %v exit %d", args, code)
		}
	}
	var out bytes.Buffer
	if code := productionFailure(&out, credentials.ErrRefreshUnknown); code != 3 || !strings.Contains(out.String(), `"next_action":"reconcile_account_token"`) {
		t.Fatal("unknown token refresh was reported as Keychain permission failure")
	}
}
