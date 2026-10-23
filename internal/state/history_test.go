package state_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func historyFixture(t *testing.T) (*state.Store, manifest.Snapshot) {
	t.Helper()
	path, err := simulation.Init(filepath.Join(t.TempDir(), "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(t.TempDir(), "state"), manifest.Simulation, false)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	result, err := e.Plan(context.Background(), path)
	must(t, err)
	snapshot, err := s.Load(result.ReleaseID)
	must(t, err)
	return s, snapshot
}

func TestHistoryAcceptsValidEscapedReleaseNotes(t *testing.T) {
	s, snapshot := historyFixture(t)
	snapshot.Manifest.ReleaseID = "escaped-note"
	snapshot.Notes = strings.Repeat("<>&", 300000)
	snapshot.Digest = snapshot.CalculateDigest()
	_, err := s.Save(snapshot)
	must(t, err)
	data, err := json.Marshal(snapshot)
	must(t, err)
	if len(data) <= 2<<20 {
		t.Fatal("fixture did not cover serialized expansion")
	}
	records, err := s.ReleaseRecords(context.Background(), []string{snapshot.Manifest.ReleaseID})
	must(t, err)
	if len(records) != 1 || records[0].Snapshot.Notes != snapshot.Notes {
		t.Fatal("escaped notes changed")
	}
	rows, err := s.History(context.Background(), state.HistoryFilter{Limit: 20})
	must(t, err)
	if len(rows) != 2 {
		t.Fatal("valid escaped record absent from history")
	}
}

func TestHistoryFiltersKeepCompleteScopeAndValidateBounds(t *testing.T) {
	s, first := historyFixture(t)
	second := first
	second.Manifest.ReleaseID = "newer-release"
	second.Manifest.VersionCode++
	second.Notes += " newer"
	second.Digest = second.CalculateDigest()
	_, err := s.Save(second)
	must(t, err)
	_, err = s.DB.Exec("UPDATE releases SET created_at='2026-10-21T00:00:00Z' WHERE id=?", first.Manifest.ReleaseID)
	must(t, err)
	_, err = s.DB.Exec("UPDATE releases SET created_at='2026-10-22T00:00:00Z' WHERE id=?", second.Manifest.ReleaseID)
	must(t, err)
	reader, err := state.Open(s.Root, manifest.Simulation, true)
	must(t, err)
	defer reader.Close()
	rows, err := reader.History(context.Background(), state.HistoryFilter{PackageName: first.Manifest.PackageName, Channel: "xiaomi", Limit: 1})
	must(t, err)
	if len(rows) != 1 || rows[0].Snapshot.Manifest.ReleaseID != second.Manifest.ReleaseID || len(rows[0].Targets) != len(first.Manifest.Targets) {
		t.Fatal("filter shrank release scope or order was unstable", rows)
	}
	for _, filter := range []state.HistoryFilter{{Limit: 0}, {Limit: 201}, {Limit: 20, Channel: "invalid"}, {Limit: 20, PackageName: "x' OR 1=1--"}} {
		if _, err := reader.History(context.Background(), filter); err == nil {
			t.Fatal("invalid history filter accepted", filter)
		}
	}
	rows, err = reader.History(context.Background(), state.HistoryFilter{PackageName: "com.example.absent", Limit: 20})
	must(t, err)
	if len(rows) != 0 {
		t.Fatal("package filter ignored")
	}
	if _, err := reader.DB.Exec("UPDATE meta SET value='1' WHERE key='recovery_required'"); err == nil {
		t.Fatal("history reader was writable")
	}
	var count int
	must(t, s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&count))
	if count != 0 {
		t.Fatal("history created operations")
	}
	if _, err := os.Stat(filepath.Join(s.Root, "credentials")); !os.IsNotExist(err) {
		t.Fatal("history touched credential state")
	}
}

func TestReleaseRecordsExcludePrivateOperationFieldsAndRejectCorruption(t *testing.T) {
	s, snapshot := historyFixture(t)
	target := snapshot.Manifest.ReleaseID + "/xiaomi"
	_, err := s.DB.Exec("INSERT INTO operations(id,target_id,kind,request,request_digest,effect,remote_ref,error,created_at) VALUES(?,?,?,?,?,?,?,?,?)", "private-op-id", target, "push", `{"client_secret":"SYNTHETIC_SECRET"}`, strings.Repeat("a", 64), "unknown", "PRIVATE_REMOTE_REF", "PRIVATE_RAW_ERROR", "2026-10-22T00:00:00Z")
	must(t, err)
	rows, err := s.ReleaseRecords(context.Background(), []string{snapshot.Manifest.ReleaseID})
	must(t, err)
	if len(rows) != 1 || len(rows[0].Operations) != 1 || rows[0].Operations[0].NextAction != "reconcile" {
		t.Fatal("missing safe recovery action", rows)
	}
	data, err := json.Marshal(rows[0].Operations)
	must(t, err)
	for _, secret := range []string{"SYNTHETIC_SECRET", "PRIVATE_REMOTE_REF", "PRIVATE_RAW_ERROR", "private-op-id", "request_digest"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("private operation fields exposed", string(data))
		}
	}
	data, err = json.Marshal(rows[0])
	must(t, err)
	if string(data) != "{}" {
		t.Fatal("internal records must not accidentally export snapshots", string(data))
	}
	for _, ids := range [][]string{nil, {snapshot.Manifest.ReleaseID, snapshot.Manifest.ReleaseID}, {"../invalid"}} {
		if _, err := s.ReleaseRecords(context.Background(), ids); err == nil {
			t.Fatal("invalid selection accepted", ids)
		}
	}
	_, err = s.DB.Exec("UPDATE releases SET digest=? WHERE id=?", strings.Repeat("f", 64), snapshot.Manifest.ReleaseID)
	must(t, err)
	if _, err := s.ReleaseRecords(context.Background(), []string{snapshot.Manifest.ReleaseID}); err == nil {
		t.Fatal("corrupt digest accepted")
	}
	_, err = s.DB.Exec("UPDATE releases SET digest=? WHERE id=?", snapshot.Digest, snapshot.Manifest.ReleaseID)
	must(t, err)
	_, err = s.DB.Exec("DELETE FROM operations")
	must(t, err)
	_, err = s.DB.Exec("DELETE FROM targets WHERE id=?", target)
	must(t, err)
	if _, err := s.ReleaseRecords(context.Background(), []string{snapshot.Manifest.ReleaseID}); err == nil {
		t.Fatal("incomplete target scope accepted")
	}
}
