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
)

func TestHuaweiRenewalFlagsRejectOtherModesAndScopes(t *testing.T) {
	op := strings.Repeat("a", 64)
	for _, args := range [][]string{
		{"simulate", "resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"preflight", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"submit", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"reconcile", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"status", "--release", "synthetic", "--refresh", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"status", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
		{"plan", "--manifest", "unused", "--renew-expired-upload-authorization", op},
		{"resume", "--release", "synthetic", "--renew-expired-upload-authorization", op},
		{"resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", "bad-proof"},
		{"resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", ""},
		{"resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op, "--replace-unused-upload-authorization", op},
		{"resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op, "--credential-name", ""},
		{"--data-dir", t.TempDir(), "resume", "--release", "synthetic", "--account-config", "unused", "--renew-expired-upload-authorization", op},
	} {
		var out, stderr bytes.Buffer
		if code := Run(context.Background(), args, &out, &stderr); code != 2 {
			t.Fatalf("unsafe renewal flags accepted: %v exit %d %s", args, code, &out)
		}
	}
	for _, channel := range []string{"honor", "xiaomi", "vivo", "tencent", "oppo"} {
		path := filepath.Join(t.TempDir(), "config.json")
		b, _ := json.Marshal(channels.PreparationConfig{AccountQuery: channels.AccountQuery{Channel: channel}})
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		code := fiveProductionCommand(context.Background(), "resume", t.TempDir(), "synthetic", path, strings.Repeat("b", 64), strings.Repeat("c", 64), nil, artifact.SimulationInspector{}, func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
			t.Fatal("non-Huawei renewal accessed factory")
			return nil, nil
		}, &out, op)
		if code != 2 || !strings.Contains(out.String(), "Huawei release only") {
			t.Fatalf("wrong channel renewal: %s %d %s", channel, code, &out)
		}
	}
}

func TestHuaweiRenewalRequiresBothFrozenApprovals(t *testing.T) {
	root, snapshot, scope, config := fiveCLIPlan(t, "huawei")
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	writes, observations := 0, 0
	factory := func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writes: &writes, observes: &observations}, nil
	}
	if code := fiveProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, factory, &out); code != 0 {
		t.Fatalf("preflight: %d %s", code, &out)
	}
	op := strings.Repeat("a", 64)
	for _, pair := range [][2]string{{"", ""}, {snapshot.Digest, ""}, {"", binding}, {snapshot.Digest, strings.Repeat("c", 64)}, {strings.Repeat("c", 64), binding}} {
		out.Reset()
		code := fiveProductionCommand(context.Background(), "resume", root, snapshot.Manifest.ReleaseID, config, pair[0], pair[1], nil, artifact.SimulationInspector{}, func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
			t.Fatal("unapproved renewal accessed credentials or network")
			return nil, nil
		}, &out, op)
		if code != 2 || !strings.Contains(out.String(), `"effect":"not_sent"`) {
			t.Fatalf("renewal approval bypass: %d %s", code, &out)
		}
	}
	for _, renewal := range [][]string{{"bad-proof"}, {op, op}} {
		out.Reset()
		code := fiveProductionCommand(context.Background(), "resume", root, snapshot.Manifest.ReleaseID, config, snapshot.Digest, binding, nil, artifact.SimulationInspector{}, func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
			t.Fatal("malformed renewal accessed factory")
			return nil, nil
		}, &out, renewal...)
		if code != 2 {
			t.Fatalf("malformed renewal accepted: %d %s", code, &out)
		}
	}
}

func TestHuaweiFailedRenewalDoesNotContinueNormalSubmission(t *testing.T) {
	root, snapshot, scope, config := fiveCLIPlan(t, "huawei")
	var out bytes.Buffer
	binding := strings.Repeat("b", 64)
	writes, observations, factoryCalls := 0, 0, 0
	factory := func(_ context.Context, _ channels.PreparationInput, _ string, writable bool) (fivePrepared, error) {
		factoryCalls++
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writable: writable, writes: &writes, observes: &observations}, nil
	}
	if code := fiveProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, factory, &out); code != 0 {
		t.Fatal(&out)
	}
	out.Reset()
	// No expired accepted authorization is present. The renewal must fail,
	// preserving the ready target and never falling through to normal Submit.
	code := fiveProductionCommand(context.Background(), "resume", root, snapshot.Manifest.ReleaseID, config, snapshot.Digest, binding, nil, artifact.SimulationInspector{}, factory, &out, strings.Repeat("a", 64))
	if code != 3 || factoryCalls != 2 || writes != 0 || !strings.Contains(out.String(), "upload authorization renewal did not complete") || !strings.Contains(out.String(), `"execution_phase":"ready"`) || !strings.Contains(out.String(), `"operations":[]`) {
		t.Fatalf("failed renewal continued normal execution: %d writes=%d %s", code, writes, &out)
	}
}
