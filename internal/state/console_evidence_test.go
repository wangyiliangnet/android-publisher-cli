package state_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func acceptedConsoleFixture(t *testing.T) (*state.Store, state.ConsoleEvidence) {
	t.Helper()
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	fake, err := simulation.New(filepath.Join(base, "remote"), simulation.Options{})
	must(t, err)
	server := httptest.NewServer(fake)
	t.Cleanup(func() { server.Close(); fake.Close() })
	client, err := simulation.NewClient(server.URL)
	must(t, err)
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: client}
	plan, err := e.Plan(context.Background(), path)
	must(t, err)
	_, err = e.Preflight(context.Background(), plan.ReleaseID, nil)
	must(t, err)
	_, err = e.Submit(context.Background(), plan.ReleaseID, nil)
	must(t, err)
	evidence, err := s.ConsoleTemplate(plan.ReleaseID, "xiaomi")
	must(t, err)
	evidence.Observed = evidence.Expected
	evidence.Review = "approved"
	evidence.Availability = "live"
	evidence.CollectedAt = time.Now().UTC().Format(time.RFC3339Nano)
	evidence.CollectedBy = "synthetic reviewer"
	evidence.SourceURL = "https://dev.mi.com/console"
	evidence.Confirmed = true
	return s, evidence
}
func TestConsoleEvidenceExactBindingAndBackup(t *testing.T) {
	s, e := acceptedConsoleFixture(t)
	var before int
	must(t, s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&before))
	for _, kind := range []string{"old-version", "wrong-ref", "future", "unconfirmed", "query-secret", "wrong-account", "old-time"} {
		bad := e
		switch kind {
		case "old-version":
			bad.Observed.VersionCode--
		case "wrong-ref":
			bad.Observed.SubmissionRef = "other"
		case "future":
			bad.CollectedAt = time.Now().Add(time.Hour).Format(time.RFC3339Nano)
		case "unconfirmed":
			bad.Confirmed = false
		case "query-secret":
			bad.SourceURL += "?token=synthetic"
		case "wrong-account":
			bad.Observed.Account = "other"
		case "old-time":
			bad.CollectedAt = "2020-01-01T00:00:00Z"
		}
		if err := s.ImportConsoleEvidence(bad); err == nil {
			t.Fatal("accepted", kind)
		}
	}
	must(t, s.ImportConsoleEvidence(e))
	var schemaVersion int
	must(t, s.DB.QueryRow("PRAGMA user_version").Scan(&schemaVersion))
	if schemaVersion != 3 {
		t.Fatal("manual evidence did not exclude incompatible old readers")
	}
	target, err := s.Target(e.ReleaseID + "/xiaomi")
	must(t, err)
	if target.Review != "approved" || target.Availability != "live" {
		t.Fatal(target)
	}
	// A vendor API without target audit fields cannot erase the manual evidence.
	o := protocol.Observation{Scope: target.Scope, VersionCode: e.Expected.VersionCode, RemoteRef: target.RemoteRef, Review: "unknown", Availability: "unknown", Source: "xiaomi_api", CollectedAt: time.Now().UTC()}
	generation, err := s.BeginObservation(target.ID)
	must(t, err)
	must(t, s.Observe(target, generation, e.Expected.VersionCode, o, nil))
	target, err = s.Target(target.ID)
	must(t, err)
	if target.Review != "approved" || target.Availability != "live" {
		t.Fatal("unknown erased evidence", target)
	}
	var after int
	must(t, s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&after))
	if before != after {
		t.Fatal("observation changed business operations")
	}
	backup := filepath.Join(t.TempDir(), "backup")
	must(t, s.Backup(backup))
	restoredRoot := filepath.Join(t.TempDir(), "restored")
	must(t, state.Restore(restoredRoot, backup, manifest.Simulation))
	restored, err := state.Open(restoredRoot, manifest.Simulation, false)
	must(t, err)
	defer restored.Close()
	target, err = restored.Target(target.ID)
	must(t, err)
	if target.Availability != "live" {
		t.Fatal(target)
	}
}
func TestRecoveryClosureRequiresCompleteReviewAndFloors(t *testing.T) {
	s, e := acceptedConsoleFixture(t)
	root := s.Root
	backup := filepath.Join(t.TempDir(), "backup")
	must(t, s.Backup(backup))
	must(t, s.Close())
	must(t, state.Restore(root, backup, manifest.Simulation))
	s, err := state.Open(root, manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	r, err := s.RecoveryTemplate()
	must(t, err)
	if _, err = s.ValidateRecoveryReview(r); err == nil {
		t.Fatal("unfilled template unlocked")
	}
	r.ReviewedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.ReviewedBy = "synthetic reviewer"
	r.Reason = "Synthetic full-account history inspection after a controlled restore"
	r.CompleteAccountInventory = true
	for i := range r.Applications {
		r.Applications[i].LatestVersion = 2
		r.Applications[i].HistoryComplete = true
		r.Applications[i].NoPendingWrites = true
		r.Applications[i].Evidence = "Synthetic complete submission history: original version 1 and a later accepted version 2; all requests are terminal."
	}
	omit := r
	omit.Applications = append([]state.RecoveryApplication{}, r.Applications[1:]...)
	if _, err = s.ValidateRecoveryReview(omit); err == nil {
		t.Fatal("omitted scope accepted")
	}
	digest, err := s.ValidateRecoveryReview(r)
	must(t, err)
	if err = s.ApplyRecoveryReview(r, "wrong"); err == nil {
		t.Fatal("wrong approval accepted")
	}
	required, err := s.RecoveryRequired()
	must(t, err)
	if !required {
		t.Fatal("wrong approval removed isolation")
	}
	must(t, s.ApplyRecoveryReview(r, digest))
	var schemaVersion int
	must(t, s.DB.QueryRow("PRAGMA user_version").Scan(&schemaVersion))
	if schemaVersion != 3 {
		t.Fatal("recovery version floors can be bypassed by an old reader")
	}
	required, err = s.RecoveryRequired()
	must(t, err)
	if required {
		t.Fatal("valid full-history review did not close")
	}
	old, err := s.Target(e.ReleaseID + "/xiaomi")
	must(t, err)
	if err = s.CheckWritable(old.Scope, old.ID); err == nil {
		t.Fatal("restored version below remote floor writable")
	}
	snapshot, err := s.Load(e.ReleaseID)
	must(t, err)
	snapshot.Manifest.ReleaseID = "post-recovery"
	snapshot.Manifest.VersionCode = 3
	snapshot.Manifest.VersionName = "3"
	snapshot.Digest = snapshot.CalculateDigest()
	_, err = s.Save(snapshot)
	must(t, err)
	target, err := s.Target("post-recovery/xiaomi")
	must(t, err)
	must(t, s.CheckWritable(target.Scope, target.ID))
	if err = s.ApplyRecoveryReview(r, digest); err == nil {
		t.Fatal("recovery approval replay accepted")
	}
}
