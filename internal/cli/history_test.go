package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func historyCLIState(t *testing.T) (string, manifest.Snapshot) {
	t.Helper()
	path, err := simulation.Init(filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "runtime")
	s, err := state.Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	r, err := e.Plan(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Load(r.ReleaseID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return root, snapshot
}

func privateReportPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

func TestHistoryOfflineEmptyFiltersAndFullScope(t *testing.T) {
	var out, stderr bytes.Buffer
	empty := filepath.Join(t.TempDir(), "absent")
	if code := runHistory(context.Background(), manifest.Simulation, empty, []string{"--format", "json"}, &out, &stderr); code != 0 || !strings.Contains(out.String(), `"releases":[]`) {
		t.Fatal(code, out.String())
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("empty history initialized runtime state")
	}
	root, snapshot := historyCLIState(t)
	out.Reset()
	if code := runHistory(context.Background(), manifest.Simulation, root, []string{"--format", "json", "--channel", "xiaomi", "--package", snapshot.Manifest.PackageName, "--limit", "1"}, &out, &stderr); code != 0 {
		t.Fatal(code, out.String())
	}
	var result struct {
		Mode     string       `json:"mode"`
		Effect   string       `json:"effect"`
		Releases []historyRow `json:"releases"`
	}
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Mode != manifest.Simulation || result.Effect != "offline" || len(result.Releases) != 1 || result.Releases[0].Total != 6 || len(result.Releases[0].Channels) != 6 || result.Releases[0].IntentDigest != snapshot.Digest {
		t.Fatal("channel filter lost complete scope", out.String())
	}
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "201"}, {"--format", "html"}, {"--channel", "bogus"}, {"--package", "x' OR 1=1--"}, {"--refresh"}} {
		out.Reset()
		if code := runHistory(context.Background(), manifest.Simulation, root, args, &out, &stderr); code != 2 {
			t.Fatal("invalid history flags accepted", args, code, out.String())
		}
	}
	out.Reset()
	if code := runHistory(context.Background(), manifest.Production, root, nil, &out, &stderr); code != 2 {
		t.Fatal("custom production root accepted", code)
	}
}

func TestReportSafeProjectionContextCountsAndNoEffects(t *testing.T) {
	root, snapshot := historyCLIState(t)
	s, err := state.Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	// These private values deliberately appear in columns which must never be
	// projected into a shareable release report.
	_, err = s.DB.Exec("UPDATE targets SET phase='submitted',remote_ref='PRIVATE_REMOTE',last_error='PRIVATE_RAW_RESPONSE',preflight='PRIVATE_BASELINE'")
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"oppo", "honor", "xiaomi", "tencent"} {
		target, err := s.Target(snapshot.Manifest.ReleaseID + "/" + channel)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := s.BeginObservation(target.ID)
		if err != nil {
			t.Fatal(err)
		}
		observation := protocol.Observation{Scope: target.Scope, VersionCode: snapshot.Manifest.VersionCode, RemoteRef: target.RemoteRef, Review: "unknown", Availability: "unknown", Source: "simulation", CollectedAt: time.Now().UTC()}
		if channel == "oppo" || channel == "honor" {
			observation.Review, observation.Availability = "approved", "live"
		}
		if channel == "xiaomi" {
			observation.Context = &protocol.ChannelContext{APIVersionCode: snapshot.Manifest.VersionCode, APIVersionName: "1.0", Association: "unconfirmed", InferredReview: "approved", InferenceBasis: "xiaomi_api_version_equals_target", APIQueryHealth: "healthy", StoreQueryHealth: "not_supported"}
		}
		if channel == "tencent" {
			observation.Context = &protocol.ChannelContext{APIVersionCode: 1, AuditStatus: "rejected", AuditCode: "2", RejectionReason: "隐私政策缺少说明", StoreVersionName: "0.9", Association: "unconfirmed", APIQueryHealth: "healthy", StoreQueryHealth: "healthy", SourceURL: "https://sj.qq.com/appdetail/" + snapshot.Manifest.PackageName}
		}
		if err := observation.Validate(target.Scope, snapshot.Manifest.VersionCode, target.RemoteRef); err != nil {
			t.Fatal("invalid synthetic observation", channel, err)
		}
		// Preflight is not needed for an offline simulation observation.
		_, err = s.DB.Exec("UPDATE targets SET preflight='' WHERE id=?", target.ID)
		if err != nil {
			t.Fatal(err)
		}
		target.Preflight = ""
		if err = s.Observe(target, generation, snapshot.Manifest.VersionCode, observation, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.DB.Exec("UPDATE targets SET disputed=1 WHERE channel='honor'")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec("INSERT INTO operations(id,target_id,kind,request,request_digest,effect,remote_ref,error,created_at) VALUES(?,?,?,?,?,?,?,?,?)", "PRIVATE_OPERATION", snapshot.Manifest.ReleaseID+"/tencent", "submit", `{"secret":"PRIVATE_SECRET"}`, strings.Repeat("a", 64), "unknown", "PRIVATE_REMOTE", "PRIVATE_RAW_RESPONSE", "2026-10-22T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	file := privateReportPath(t, "report.json")
	var out, stderr bytes.Buffer
	if code := runReport(context.Background(), manifest.Simulation, root, []string{"--release", snapshot.Manifest.ReleaseID, "--file", file, "--format", "json"}, &out, &stderr); code != 0 {
		t.Fatal(code, out.String())
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var report releaseReport
	if json.Unmarshal(data, &report) != nil || report.Total != 6 || report.Accepted != 6 || report.Approved != 1 || report.Live != 1 || report.AllApproved || report.AllLive || !report.FullScope {
		t.Fatal("context or disputed evidence counted as verified", string(data))
	}
	for _, forbidden := range []string{"PRIVATE_REMOTE", "PRIVATE_RAW_RESPONSE", "PRIVATE_BASELINE", "PRIVATE_SECRET", "PRIVATE_OPERATION", "account_ref", "app_ref", "scope", "remote_ref", "preflight", "request_digest", "notes_file"} {
		if forbidden == "scope" {
			continue // The explicit full_scope boolean is public, target scope is absent.
		}
		if strings.Contains(string(data), forbidden) {
			t.Fatal("private report data leaked", forbidden, string(data))
		}
	}
	for _, target := range report.Releases[0].Targets {
		if target.Channel == "xiaomi" && (target.Context == nil || target.Context.InferredReview != "approved" || target.Review != "unknown") {
			t.Fatal("Xiaomi inference attribution missing", target)
		}
		if target.Channel == "tencent" && (target.Context == nil || target.Context.Association != "unconfirmed" || target.Context.RejectionReason == "" || target.Context.StoreVersionName != "0.9" || target.Review != "unknown") {
			t.Fatal("Tencent unassociated facts missing", target)
		}
	}
	if len(report.Releases[0].Operations) != 1 || report.Releases[0].Operations[0].NextAction != "reconcile" {
		t.Fatal("unknown write action lost")
	}
	reader, err := state.Open(root, manifest.Simulation, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var operations int
	reader.DB.QueryRow("SELECT count(*) FROM operations").Scan(&operations)
	if operations != 1 {
		t.Fatal("report changed operation journal")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("report must be private", info.Mode())
	}
}

func TestReportMarkdownEscapesAndExclusiveExport(t *testing.T) {
	r := releaseReport{Mode: manifest.Simulation, FullScope: true, Releases: []reportedRelease{{ReleaseID: "fixture", PackageName: "com.example.app", VersionName: "<script>alert(1)</script>|x", Notes: "<img src=x onerror=evil>\n[open](https://invalid.example)\n```sh\necho bad\n```", Targets: []reportedTarget{{Channel: "xiaomi", Review: "unknown", Availability: "unknown", Context: &protocol.ChannelContext{APIVersionCode: 1, Association: "unconfirmed", InferredReview: "approved", InferenceBasis: "xiaomi_api_version_equals_target", APIQueryHealth: "healthy", StoreQueryHealth: "not_supported"}}}}}}
	data := renderReleaseReport(r)
	for _, forbidden := range []string{"<script>", "<img", "\n```", "[open]("} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatal("active markdown content escaped incorrectly", string(data))
		}
	}
	if !bytes.Contains(data, []byte("审核推测")) || !bytes.Contains(data, []byte("未确认属于本次版本")) {
		t.Fatal("unassociated context was not labeled", string(data))
	}
	file := privateReportPath(t, "report.md")
	if err := writeNewReport(file, data); err != nil {
		t.Fatal(err)
	}
	if err := writeNewReport(file, []byte("replace")); err == nil {
		t.Fatal("existing report overwritten")
	}
	original, _ := os.ReadFile(file)
	if !bytes.Equal(original, data) {
		t.Fatal("existing report truncated")
	}
	link := filepath.Join(filepath.Dir(file), "symlink.md")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := writeNewReport(link, []byte("replace")); err == nil {
		t.Fatal("report symlink accepted")
	}
	dirLink := filepath.Join(filepath.Dir(file), "parent-link")
	if err := os.Symlink(filepath.Dir(file), dirLink); err != nil {
		t.Fatal(err)
	}
	if err := writeNewReport(filepath.Join(dirLink, "new.md"), data); err == nil {
		t.Fatal("report parent symlink accepted")
	}
	if err := writeNewReport("relative.md", data); err == nil {
		t.Fatal("relative report path accepted")
	}
}

func TestBatchReportBindsAllFrozenIntents(t *testing.T) {
	root, original := historyCLIState(t)
	s, err := state.Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	b := batchFile{SchemaVersion: 1, ID: "history-batch"}
	for _, channel := range []string{"xiaomi", "tencent"} {
		snapshot := original
		snapshot.Manifest.ReleaseID = "history-" + channel
		snapshot.Manifest.Targets = map[string]manifest.Target{channel: original.Manifest.Targets[channel]}
		snapshot.Digest = snapshot.CalculateDigest()
		if _, err := s.Save(snapshot); err != nil {
			t.Fatal(err)
		}
		b.Entries = append(b.Entries, batchEntry{ReleaseID: snapshot.Manifest.ReleaseID, Channel: channel})
	}
	s.Close()
	input := privateReportPath(t, "batch-input.json")
	data, _ := json.Marshal(b)
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	b, err = createBatch(root, manifest.Simulation, input)
	if err != nil {
		t.Fatal(err)
	}
	file := privateReportPath(t, "batch-report.json")
	var out, stderr bytes.Buffer
	if code := runReport(context.Background(), manifest.Simulation, root, []string{"--batch", b.ID, "--file", file, "--format", "json"}, &out, &stderr); code != 0 {
		t.Fatal(code, out.String())
	}
	data, _ = os.ReadFile(file)
	var report releaseReport
	if json.Unmarshal(data, &report) != nil || report.Total != 2 || len(report.Releases) != 2 || report.BatchID != b.ID {
		t.Fatal("batch scope lost", string(data))
	}
	b.Entries[0].IntentDigest = strings.Repeat("f", 64)
	data, _ = json.Marshal(b)
	if err := os.WriteFile(batchPath(root, b.ID), data, 0600); err != nil {
		t.Fatal(err)
	}
	invalidFile := privateReportPath(t, "invalid-report.md")
	out.Reset()
	if code := runReport(context.Background(), manifest.Simulation, root, []string{"--batch", b.ID, "--file", invalidFile}, &out, &stderr); code != 2 {
		t.Fatal("changed batch intent accepted", code, out.String())
	}
	if _, err := os.Stat(invalidFile); !os.IsNotExist(err) {
		t.Fatal("invalid batch produced a report")
	}
	for _, args := range [][]string{{"--release", original.Manifest.ReleaseID, "--batch", b.ID, "--file", file}, {"--release", original.Manifest.ReleaseID, "--file", "relative.md"}, {"--release", original.Manifest.ReleaseID, "--file", file, "--format", "html"}, {"--release", original.Manifest.ReleaseID, "--file", file, "--refresh"}} {
		out.Reset()
		if code := runReport(context.Background(), manifest.Simulation, root, args, &out, &stderr); code != 2 {
			t.Fatal("invalid report flags accepted", args, code)
		}
	}
}

func TestReportRetainsIndependentPartialQueryHealth(t *testing.T) {
	c := &protocol.ChannelContext{StoreVersionName: "1.0", Association: "unconfirmed", APIQueryHealth: "failed", StoreQueryHealth: "healthy"}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	r := makeReleaseReport(manifest.Simulation, "", []state.ReleaseRecord{{Targets: []state.Target{{Channel: "tencent", LastError: "one or more channel sources failed", QueryFailures: 1, Context: c}}}})
	if r.Releases[0].Targets[0].QueryHealth != "partial" || r.Releases[0].Targets[0].Context.StoreVersionName != "1.0" {
		t.Fatal("partial source success was discarded", r)
	}
	r = makeReleaseReport(manifest.Simulation, "", []state.ReleaseRecord{{Targets: []state.Target{{Channel: "tencent", LastError: "query failed or evidence insufficient", QueryFailures: 2, Context: c}}}})
	if r.Releases[0].Targets[0].QueryHealth != "failed" {
		t.Fatal("older context hid current failed query", r)
	}
}
