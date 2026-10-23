package state_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func evidenceFixture(t *testing.T) (*state.Store, state.Target, protocol.Observation) {
	t.Helper()
	base := t.TempDir()
	manifestPath, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	result, err := e.Plan(context.Background(), manifestPath)
	must(t, err)
	_, err = s.DB.Exec("UPDATE targets SET phase='submitted',remote_ref='remote' WHERE channel='xiaomi'")
	must(t, err)
	target, err := s.Target(result.ReleaseID + "/xiaomi")
	must(t, err)
	o := protocol.Observation{Scope: target.Scope, VersionCode: 1, RemoteRef: "remote", Review: "approved", Availability: "live", Source: "browser_fixture", CollectedAt: time.Now().UTC()}
	for _, file := range []struct{ kind, name, content string }{{"text", "evidence.json", `{"kind":"synthetic"}`}, {"png", "evidence.png", "\x89PNG\r\n\x1a\nsynthetic"}} {
		rel := "evidence/" + strings.Repeat("a", 64) + "/" + file.name
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(s.Root, rel)), 0700))
		must(t, os.WriteFile(filepath.Join(s.Root, rel), []byte(file.content), 0600))
		h := sha256.Sum256([]byte(file.content))
		o.Evidence = append(o.Evidence, protocol.Evidence{Kind: file.kind, Path: rel, SHA256: hex.EncodeToString(h[:]), Size: int64(len(file.content))})
	}
	return s, target, o
}

func recordEvidence(t *testing.T, s *state.Store, target state.Target, o protocol.Observation) {
	t.Helper()
	generation, err := s.BeginObservation(target.ID)
	must(t, err)
	must(t, s.Observe(target, generation, 1, o, nil))
}

func TestEvidenceValidationPreservesProjectionAndExcludesUnsafeReferences(t *testing.T) {
	for _, scenario := range []string{"missing-pair", "traversal", "absolute", "wrong-name", "wrong-kind", "duplicate-kind", "different-directory", "missing", "tampered", "oversized", "public-file", "public-directory", "symlink-file", "symlink-directory"} {
		t.Run(scenario, func(t *testing.T) {
			s, target, observation := evidenceFixture(t)
			first := observation
			first.Source, first.Evidence = "simulation", nil
			recordEvidence(t, s, target, first)
			observation.CollectedAt = observation.CollectedAt.Add(time.Second)
			observation.Review, observation.Availability = "in_review", "not_observed_live"
			file := filepath.Join(s.Root, observation.Evidence[0].Path)
			dir := filepath.Dir(file)
			switch scenario {
			case "missing-pair":
				observation.Evidence = nil
			case "traversal":
				observation.Evidence[0].Path = "../evidence.json"
			case "absolute":
				observation.Evidence[0].Path = file
			case "wrong-name":
				observation.Evidence[0].Path = filepath.ToSlash(filepath.Join(filepath.Dir(observation.Evidence[0].Path), "cookies.json"))
			case "wrong-kind":
				observation.Evidence[0].Kind = "profile"
			case "duplicate-kind":
				observation.Evidence[1] = observation.Evidence[0]
			case "different-directory":
				observation.Evidence[0].Path = strings.Replace(observation.Evidence[0].Path, strings.Repeat("a", 64), strings.Repeat("b", 64), 1)
			case "missing":
				must(t, os.Remove(file))
			case "tampered":
				must(t, os.WriteFile(file, []byte(strings.Repeat("!", int(observation.Evidence[0].Size))), 0600))
			case "oversized":
				observation.Evidence[0].Size = (64 << 10) + 1
			case "public-file":
				must(t, os.Chmod(file, 0644))
			case "public-directory":
				must(t, os.Chmod(dir, 0755))
			case "symlink-file":
				must(t, os.Rename(file, file+".original"))
				must(t, os.Symlink(file+".original", file))
			case "symlink-directory":
				must(t, os.Rename(dir, dir+".original"))
				must(t, os.Symlink(dir+".original", dir))
			}
			recordEvidence(t, s, target, observation)
			got, err := s.Target(target.ID)
			must(t, err)
			if got.Review != "approved" || got.Availability != "live" || got.QueryFailures != 1 || got.LastSuccessAt != first.CollectedAt.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("invalid evidence changed last good projection: %+v", got)
			}
			var document string
			must(t, s.DB.QueryRow("SELECT document FROM observations ORDER BY id DESC LIMIT 1").Scan(&document))
			var stored struct {
				Observation protocol.Observation `json:"observation"`
				Valid       bool                 `json:"valid"`
			}
			must(t, json.Unmarshal([]byte(document), &stored))
			if stored.Valid || len(stored.Observation.Evidence) != 0 {
				t.Fatal("unsafe evidence entered observation history")
			}
			// Failed observations with rejected references do not poison backups.
			must(t, s.Backup(filepath.Join(t.TempDir(), "backup")))
		})
	}
}

func TestEvidenceBackupRestoreRoundTripAndPrivateInventory(t *testing.T) {
	s, target, observation := evidenceFixture(t)
	recordEvidence(t, s, target, observation)
	observation.CollectedAt = observation.CollectedAt.Add(time.Second)
	recordEvidence(t, s, target, observation) // Repeated references are indexed once.
	profile := filepath.Join(s.Root, "browser", "xiaomi", "synthetic", "profile")
	must(t, os.MkdirAll(profile, 0700))
	must(t, os.WriteFile(filepath.Join(profile, "Cookies"), []byte("synthetic-cookie"), 0600))
	backup := filepath.Join(t.TempDir(), "backup")
	must(t, s.Backup(backup))
	if _, err := os.Stat(filepath.Join(backup, "browser")); !os.IsNotExist(err) {
		t.Fatal("profile was included in backup")
	}
	data, err := os.ReadFile(filepath.Join(backup, "index.json"))
	must(t, err)
	var index struct {
		Evidence []protocol.Evidence `json:"evidence"`
	}
	must(t, json.Unmarshal(data, &index))
	if len(index.Evidence) != 2 {
		t.Fatalf("evidence inventory was not deduplicated: %+v", index.Evidence)
	}
	root := filepath.Join(t.TempDir(), "restored")
	must(t, state.Restore(root, backup, manifest.Simulation))
	restored, err := state.Open(root, manifest.Simulation, false)
	must(t, err)
	defer restored.Close()
	isolated, err := restored.RecoveryRequired()
	must(t, err)
	if !isolated {
		t.Fatal("restore did not preserve recovery isolation")
	}
	for _, ref := range observation.Evidence {
		file := filepath.Join(root, ref.Path)
		data, err := os.ReadFile(file)
		must(t, err)
		h := sha256.Sum256(data)
		info, err := os.Stat(file)
		must(t, err)
		if hex.EncodeToString(h[:]) != ref.SHA256 || info.Mode().Perm() != 0600 {
			t.Fatal("restored evidence changed")
		}
	}
	got, err := restored.Target(target.ID)
	must(t, err)
	if got.Review != "approved" || got.Availability != "live" {
		t.Fatal("restored projection lost evidence")
	}
	// Evidence remains backed up after the first restore, alongside isolation.
	must(t, restored.Backup(filepath.Join(t.TempDir(), "second-backup")))
}

func TestEvidenceBackupAndRestoreRejectMissingTamperedAndIncompleteInputs(t *testing.T) {
	for _, scenario := range []string{"backup-missing", "backup-tampered", "restore-missing", "restore-tampered", "restore-symlink", "index-missing", "index-extra", "destination-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			s, target, observation := evidenceFixture(t)
			recordEvidence(t, s, target, observation)
			backup := filepath.Join(t.TempDir(), "backup")
			if strings.HasPrefix(scenario, "backup-") {
				file := filepath.Join(s.Root, observation.Evidence[0].Path)
				if scenario == "backup-missing" {
					must(t, os.Remove(file))
				} else {
					must(t, os.WriteFile(file, []byte("tampered"), 0600))
				}
				if s.Backup(backup) == nil {
					t.Fatal("invalid evidence was backed up")
				}
				return
			}
			must(t, s.Backup(backup))
			file := filepath.Join(backup, observation.Evidence[0].Path)
			root := filepath.Join(t.TempDir(), "restored")
			switch scenario {
			case "restore-missing":
				must(t, os.Remove(file))
			case "restore-tampered":
				must(t, os.WriteFile(file, []byte("tampered"), 0600))
			case "restore-symlink":
				must(t, os.Rename(file, file+".original"))
				must(t, os.Symlink(file+".original", file))
			case "index-missing", "index-extra":
				indexPath := filepath.Join(backup, "index.json")
				data, err := os.ReadFile(indexPath)
				must(t, err)
				var index map[string]json.RawMessage
				must(t, json.Unmarshal(data, &index))
				if scenario == "index-missing" {
					delete(index, "evidence")
				} else {
					index["evidence"], err = json.Marshal(append(observation.Evidence, observation.Evidence[0]))
					must(t, err)
				}
				data, err = json.Marshal(index)
				must(t, err)
				must(t, os.WriteFile(indexPath, data, 0600))
			case "destination-symlink":
				must(t, os.Mkdir(root, 0700))
				must(t, os.Symlink(t.TempDir(), filepath.Join(root, "evidence")))
			}
			if state.Restore(root, backup, manifest.Simulation) == nil {
				t.Fatal("invalid evidence restore succeeded")
			}
			if _, err := os.Stat(filepath.Join(root, "state.sqlite")); !os.IsNotExist(err) {
				t.Fatal("failed restore activated its database")
			}
		})
	}
}
