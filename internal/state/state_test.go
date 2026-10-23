package state_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func TestModeIsolationPermissionsAndReadDuringWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	s, err := state.Open(root, manifest.Simulation, false)
	must(t, err)
	if _, err := state.Open(root, manifest.Simulation, false); !errors.Is(err, state.ErrBusy) {
		t.Fatal("second writer allowed")
	}
	reader, err := state.Open(root, manifest.Simulation, true)
	must(t, err)
	must(t, reader.Close())
	for _, path := range []string{root, filepath.Join(root, "state.sqlite"), filepath.Join(root, "state.sqlite-wal"), filepath.Join(root, "state.sqlite-shm")} {
		info, err := os.Stat(path)
		must(t, err)
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("private mode %s: %o", path, info.Mode().Perm())
		}
	}
	must(t, s.Close())
	if _, err = state.Open(root, manifest.Production, false); err == nil {
		t.Fatal("production opened simulation state")
	}
	// Failure must release locks, otherwise a later valid command remains permanently busy.
	s, err = state.Open(root, manifest.Simulation, false)
	must(t, err)
	must(t, s.Close())
}

func TestExistingDatabaseModesAndSidecarSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := state.Open(root, manifest.Simulation, false)
	must(t, err)
	must(t, s.Close())
	db := filepath.Join(root, "state.sqlite")
	must(t, os.Chmod(db, 0644))
	s, err = state.Open(root, manifest.Simulation, false)
	must(t, err)
	info, err := os.Stat(db)
	must(t, err)
	if info.Mode().Perm() != 0600 {
		t.Fatal("existing database mode was not repaired")
	}
	must(t, s.Close())
	target := filepath.Join(t.TempDir(), "protected")
	must(t, os.WriteFile(target, []byte("synthetic-data"), 0600))
	must(t, os.Symlink(target, db+"-wal"))
	if s, err := state.Open(root, manifest.Simulation, false); err == nil {
		s.Close()
		t.Fatal("SQLite sidecar symlink accepted")
	}
	b, err := os.ReadFile(target)
	must(t, err)
	if string(b) != "synthetic-data" {
		t.Fatal("sidecar target was modified")
	}
}

func TestMigrationFailureRollsBackAndFutureSchemaIsPreserved(t *testing.T) {
	for _, kind := range []string{"collision", "future"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(root, "state.sqlite"))
			must(t, err)
			_, err = db.Exec("CREATE TABLE releases(marker TEXT); INSERT INTO releases VALUES('keep')")
			must(t, err)
			if kind == "future" {
				_, err = db.Exec("PRAGMA user_version=999")
				must(t, err)
			}
			must(t, db.Close())
			if _, err = state.Open(root, manifest.Simulation, false); err == nil {
				t.Fatal("unsupported schema accepted")
			}
			db, err = sql.Open("sqlite", filepath.Join(root, "state.sqlite"))
			must(t, err)
			defer db.Close()
			var marker string
			must(t, db.QueryRow("SELECT marker FROM releases").Scan(&marker))
			if marker != "keep" {
				t.Fatal("migration damaged data")
			}
			var count int
			must(t, db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name='meta'").Scan(&count))
			if count != 0 {
				t.Fatal("partial migration escaped rollback")
			}
		})
	}
}

func TestConcurrentLogicalOperationsCannotShareApplication(t *testing.T) {
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	r, err := e.Plan(context.Background(), path)
	must(t, err)
	snapshot, err := s.Load(r.ReleaseID)
	must(t, err)
	snapshot.Manifest.ReleaseID = "revision-2"
	snapshot.Notes += "revision"
	snapshot.Digest = snapshot.CalculateDigest()
	_, err = s.Save(snapshot)
	must(t, err)
	t1, err := s.Target(r.ReleaseID + "/xiaomi")
	must(t, err)
	t2, err := s.Target("revision-2/xiaomi")
	must(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i, target := range []state.Target{t1, t2} {
		wg.Add(1)
		go func(i int, target state.Target) {
			defer wg.Done()
			<-start
			results <- s.Begin(protocol.Request{OperationID: protocol.Digest(i), TargetID: target.ID, Scope: target.Scope, Kind: "push"})
		}(i, target)
	}
	close(start)
	wg.Wait()
	close(results)
	ok, blocked := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, state.ErrConflict) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || blocked != 1 {
		t.Fatalf("scope race escaped journal: ok=%d blocked=%d", ok, blocked)
	}
}

func TestOlderImportedEvidenceAndConflictCannotReplaceProjection(t *testing.T) {
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	r, err := e.Plan(context.Background(), path)
	must(t, err)
	_, err = s.DB.Exec("UPDATE targets SET phase='submitted',remote_ref='remote' WHERE channel='xiaomi'")
	must(t, err)
	target, err := s.Target(r.ReleaseID + "/xiaomi")
	must(t, err)
	at := time.Now().UTC()
	observation := protocol.Observation{Scope: target.Scope, VersionCode: 1, RemoteRef: "remote", Review: "approved", Availability: "live", Source: "simulation", CollectedAt: at}
	g, err := s.BeginObservation(target.ID)
	must(t, err)
	must(t, s.Observe(target, g, 1, observation, nil))
	g, err = s.BeginObservation(target.ID)
	must(t, err)
	observation.CollectedAt = at.Add(-time.Hour)
	observation.Review = "in_review"
	observation.Availability = "not_observed_live"
	must(t, s.Observe(target, g, 1, observation, nil))
	got, err := s.Target(target.ID)
	must(t, err)
	if got.Review != "approved" || got.Availability != "live" {
		t.Fatal("historical import regressed evidence")
	}
	g, err = s.BeginObservation(target.ID)
	must(t, err)
	observation.CollectedAt = at
	must(t, s.Observe(target, g, 1, observation, nil))
	got, err = s.Target(target.ID)
	must(t, err)
	if !got.Disputed || got.Review != "approved" {
		t.Fatal("conflicting evidence did not preserve and flag last proven state")
	}
}

func TestRestoreRejectsCorruptionAndActiveConnections(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "client")
	s, err := state.Open(root, manifest.Simulation, false)
	must(t, err)
	backup := filepath.Join(base, "backup")
	must(t, s.Backup(backup))
	if err = state.Restore(root, backup, manifest.Simulation); !errors.Is(err, state.ErrBusy) {
		t.Fatalf("restore ignored active connection: %v", err)
	}
	must(t, s.Close())
	file, err := os.OpenFile(filepath.Join(backup, "state.sqlite"), os.O_WRONLY|os.O_APPEND, 0600)
	must(t, err)
	_, err = file.WriteString("corrupt")
	must(t, err)
	must(t, file.Close())
	if err = state.Restore(root, backup, manifest.Simulation); err == nil {
		t.Fatal("corrupt backup restored")
	}
	s, err = state.Open(root, manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	isolated, err := s.RecoveryRequired()
	must(t, err)
	if isolated {
		t.Fatal("failed restore changed current database")
	}
}

func TestManagedSymlinksAreRejected(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	must(t, os.WriteFile(target, []byte("keep"), 0600))
	root := filepath.Join(base, "client")
	must(t, os.Mkdir(root, 0700))
	must(t, os.Symlink(target, filepath.Join(root, "state.sqlite")))
	if _, err := state.Open(root, manifest.Simulation, false); err == nil {
		t.Fatal("symlink database accepted")
	}
	data, err := os.ReadFile(target)
	must(t, err)
	if string(data) != "keep" {
		t.Fatal("symlink target overwritten")
	}
}
