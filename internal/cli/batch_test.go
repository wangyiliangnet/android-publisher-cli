package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func TestPrepareAndImmutableBatchOffline(t *testing.T) {
	root, snapshot, _ := oppoCLIPlan(t)
	notes := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notes, []byte("同事版测试\n- 保留原文\n"), 0600)
	a := snapshot.Manifest.Artifacts["oppo"]
	input := prepareInput{SchemaVersion: 1, BatchID: "team-test", PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, SignerSHA256: a.SignerSHA256, NotesFile: notes, Channels: []prepareChannel{{Channel: "oppo", Account: "fixture", APK: artifact.Path(root, a.SHA256), CredentialName: "fixture-secret"}, {Channel: "honor", Account: "fixture", APK: artifact.Path(root, a.SHA256), CredentialName: "fixture-secret", ClientID: "synthetic-client", LanguageID: "zh-CN"}}}
	file := filepath.Join(t.TempDir(), "input.json")
	data, _ := json.Marshal(input)
	os.WriteFile(file, data, 0600)
	batch, err := prepareRelease(context.Background(), manifest.Production, root, file, artifact.SimulationInspector{})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Entries) != 2 {
		t.Fatal(batch)
	}
	same, err := prepareRelease(context.Background(), manifest.Production, root, file, artifact.SimulationInspector{})
	if err != nil || same.ID != batch.ID {
		t.Fatal("retry did not reuse batch", err)
	}
	result, code := collectBatch(context.Background(), root, manifest.Production, batch, false, nil)
	if code != 0 || result.Total != 2 || result.Accepted != 0 {
		t.Fatal(result, code)
	}
	calls := 0
	result, code = collectBatch(context.Background(), root, manifest.Production, batch, true, func(context.Context, batchEntry, io.Writer) int { calls++; return 3 })
	if calls != 2 || code != 3 || len(result.Targets) != 2 {
		t.Fatal("failed channel hid other targets", result, code)
	}
	config := batch.Entries[1].AccountConfig
	os.WriteFile(config, []byte("{}"), 0600)
	calls = 0
	result, code = collectBatch(context.Background(), root, manifest.Production, batch, true, func(context.Context, batchEntry, io.Writer) int { calls++; return 0 })
	if calls != 1 || code != 3 || result.Targets[1].QueryStatus != "query_failed" {
		t.Fatal("changed account config was queried", result, code, calls)
	}
	os.WriteFile(notes, []byte("changed"), 0600)
	if _, err = prepareRelease(context.Background(), manifest.Production, root, file, artifact.SimulationInspector{}); err == nil {
		t.Fatal("changed frozen notes accepted")
	}
	s, err := state.Open(root, manifest.Production, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var operations int
	s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&operations)
	if operations != 0 {
		t.Fatal("offline preparation made operations")
	}
}
func TestBatchRejectsWrongSignerBeforeNewRelease(t *testing.T) {
	root, snapshot, _ := oppoCLIPlan(t)
	a := snapshot.Manifest.Artifacts["oppo"]
	notes := filepath.Join(t.TempDir(), "notes")
	os.WriteFile(notes, []byte("notes"), 0600)
	input := prepareInput{SchemaVersion: 1, BatchID: "wrong-signer", PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, SignerSHA256: strings.Repeat("a", 64), NotesFile: notes, Channels: []prepareChannel{{Channel: "oppo", Account: "fixture", APK: artifact.Path(root, a.SHA256), CredentialName: "secret"}}}
	file := filepath.Join(t.TempDir(), "input.json")
	data, _ := json.Marshal(input)
	os.WriteFile(file, data, 0600)
	if _, err := prepareRelease(context.Background(), manifest.Production, root, file, artifact.SimulationInspector{}); err == nil {
		t.Fatal("wrong certificate accepted")
	}
	s, _ := state.Open(root, manifest.Production, true)
	defer s.Close()
	if _, err := s.Load("wrong-signer-oppo"); err == nil {
		t.Fatal("bad APK was frozen")
	}
}
func TestProductionWatchCancelsAndBoundsFailure(t *testing.T) {
	var out bytes.Buffer
	calls := 0
	code := pollProduction(context.Background(), func(_ context.Context, w io.Writer) int { calls++; io.WriteString(w, "{}\n"); return 3 }, time.Millisecond, 3, &out)
	if code != 3 || calls != 3 || strings.Count(out.String(), "\n") != 3 {
		t.Fatal(code, calls, out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	calls = 0
	code = pollProduction(ctx, func(_ context.Context, w io.Writer) int { calls++; cancel(); return 0 }, time.Hour, 0, io.Discard)
	if code != 130 || calls != 1 {
		t.Fatal(code, calls)
	}
	code = pollProduction(context.Background(), func(context.Context, io.Writer) int { return 4 }, time.Hour, 0, io.Discard)
	if code != 4 {
		t.Fatal(code)
	}
}
func TestProductionWatchRejectsApprovalFlags(t *testing.T) {
	var out, stderr bytes.Buffer
	code := Run(context.Background(), []string{"watch", "--release", "test", "--approve-intent", strings.Repeat("a", 64)}, &out, &stderr)
	if code != 2 || !strings.Contains(out.String(), "read-only") {
		t.Fatal(code, out.String())
	}
}
