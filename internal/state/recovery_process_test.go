package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
)

func TestRecoveryClosureKilledBeforeCommit(t *testing.T) {
	if root := os.Getenv("PUBLISHER_TEST_RECOVERY_ROOT"); root != "" {
		s, err := Open(root, manifest.Simulation, false)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "case.json"))
		if err != nil {
			t.Fatal(err)
		}
		var r RecoveryReview
		if json.Unmarshal(data, &r) != nil {
			t.Fatal("case")
		}
		digest, err := s.ValidateRecoveryReview(r)
		if err != nil {
			t.Fatal(err)
		}
		err = s.applyRecoveryReview(r, digest, func() error {
			if err := os.WriteFile(filepath.Join(root, "kill-marker"), []byte("audit inserted but not committed"), 0600); err != nil {
				return err
			}
			return syscall.Kill(os.Getpid(), syscall.SIGKILL)
		})
		t.Fatal("child survived", err)
	}
	root := filepath.Join(t.TempDir(), "client")
	s, err := Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("synthetic frozen artifact")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if err = os.MkdirAll(filepath.Join(root, "artifacts", digest), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "artifacts", digest, "blob"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot := manifest.Snapshot{Manifest: manifest.Manifest{SchemaVersion: 1, ReleaseID: "kill-recovery", Mode: manifest.Simulation, PackageName: "com.example.fixture", VersionCode: 1, VersionName: "1", Artifacts: map[string]manifest.Artifact{"fixture": {SHA256: digest, SignerSHA256: strings.Repeat("a", 64)}}, Targets: map[string]manifest.Target{"xiaomi": {AccountRef: "fixture", AppRef: "com.example.fixture", Artifact: "fixture"}}, ReleasePolicy: manifest.Policy{Mode: "after_approval"}}, Notes: "synthetic"}
	snapshot.Digest = snapshot.CalculateDigest()
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = s.Backup(backup); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err = Restore(root, backup, manifest.Simulation); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RecoveryTemplate()
	if err != nil {
		t.Fatal(err)
	}
	r.ReviewedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r.ReviewedBy = "synthetic"
	r.Reason = "process termination during recovery closure"
	r.CompleteAccountInventory = true
	r.Applications[0].LatestVersion = 2
	r.Applications[0].HistoryComplete = true
	r.Applications[0].NoPendingWrites = true
	r.Applications[0].Evidence = "Synthetic complete remote history and task inventory, captured independently for this test."
	data, _ := json.Marshal(r)
	if err = os.WriteFile(filepath.Join(root, "case.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryClosureKilledBeforeCommit$")
	cmd.Env = append(os.Environ(), "PUBLISHER_TEST_RECOVERY_ROOT="+root)
	if err = cmd.Run(); err == nil {
		t.Fatal("child was not killed")
	}
	if _, err = os.Stat(filepath.Join(root, "kill-marker")); err != nil {
		t.Fatal("child never reached transactional fault seam", err)
	}
	s, err = Open(root, manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	required, err := s.RecoveryRequired()
	if err != nil || !required {
		t.Fatal("SIGKILL removed recovery barrier", err)
	}
	var count int
	if err = s.DB.QueryRow("SELECT COUNT(*) FROM meta WHERE key LIKE 'recovery_review_%' OR key='recovery_version_floors'").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial closure survived", count, err)
	}
	approval, err := s.ValidateRecoveryReview(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyRecoveryReview(r, approval); err != nil {
		t.Fatal(err)
	}
}
