package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

type Identity struct {
	Channel     string `json:"channel"`
	AccountRef  string `json:"account_ref"`
	AppRef      string `json:"app_ref"`
	PackageName string `json:"package_name"`
	Scope       string `json:"scope"`
}
type backupIndex struct {
	SchemaVersion  int                 `json:"schema_version"`
	Mode           string              `json:"mode"`
	CreatedAt      string              `json:"created_at"`
	DatabaseSHA256 string              `json:"database_sha256"`
	Artifacts      []string            `json:"artifacts"`
	Identities     []Identity          `json:"identities"`
	Evidence       []protocol.Evidence `json:"evidence,omitempty"`
}

func inventory(db *sql.DB) ([]string, []Identity, error) {
	rows, err := db.Query("SELECT snapshot FROM releases ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	identitySet := map[string]bool{}
	identities := []Identity{}
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, nil, err
		}
		var s manifest.Snapshot
		if json.Unmarshal([]byte(data), &s) != nil || s.Verify(s.Manifest.Mode) != nil {
			return nil, nil, fmt.Errorf("cannot back up a corrupt frozen intent")
		}
		for _, a := range s.Manifest.Artifacts {
			seen[a.SHA256] = true
		}
		for channel, t := range s.Manifest.Targets {
			scope := Scope(channel, t, s.Manifest.PackageName)
			if !identitySet[scope] {
				identities = append(identities, Identity{channel, t.AccountRef, t.AppRef, s.Manifest.PackageName, scope})
				identitySet[scope] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	digests := []string{}
	for h := range seen {
		digests = append(digests, h)
	}
	slices.Sort(digests)
	slices.SortFunc(identities, func(a, b Identity) int {
		if a.Scope < b.Scope {
			return -1
		}
		if a.Scope > b.Scope {
			return 1
		}
		return 0
	})
	return digests, identities, nil
}
func copyArtifact(srcRoot, dstRoot, digest string) error {
	if !manifest.ValidHash(digest) {
		return fmt.Errorf("invalid artifact index")
	}
	src, dst := artifact.Path(srcRoot, digest), artifact.Path(dstRoot, digest)
	h, _, err := privatefs.Hash(src)
	if err != nil {
		return err
	}
	if h != digest {
		return fmt.Errorf("backup artifact digest mismatch")
	}
	if _, err = os.Lstat(dst); err == nil {
		existing, _, err := privatefs.Hash(dst)
		if err != nil {
			return err
		}
		if existing != digest {
			return fmt.Errorf("existing artifact is corrupt")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := privatefs.Dir(filepath.Join(dstRoot, "artifacts")); err != nil {
		return err
	}
	if err := privatefs.Dir(filepath.Dir(dst)); err != nil {
		return err
	}
	if err := privatefs.Copy(src, dst); err != nil {
		return err
	}
	h, _, err = privatefs.Hash(dst)
	if err != nil {
		return err
	}
	if h != digest {
		return fmt.Errorf("copied artifact digest mismatch")
	}
	return nil
}
func (s *Store) Backup(dest string) error {
	var absErr error
	dest, absErr = filepath.Abs(dest)
	if absErr != nil {
		return absErr
	}
	if err := privatefs.Disjoint(s.Root, dest); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		return fmt.Errorf("backup destination must not exist")
	}
	if err := privatefs.Dir(dest); err != nil {
		return err
	}
	path := filepath.Join(dest, "state.sqlite")
	// VACUUM INTO creates a consistent SQLite snapshot including committed WAL contents.
	if _, err := s.DB.Exec("VACUUM INTO ?", path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	f, err := privatefs.Open(path, os.O_RDWR)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	// Derive every copied reference from this frozen snapshot, not the active
	// connection whose history may change after VACUUM INTO.
	frozen, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	frozen.SetMaxOpenConns(1)
	digests, identities, err := inventory(frozen)
	var evidence []protocol.Evidence
	if err == nil {
		evidence, err = evidenceInventory(frozen)
	}
	closeErr := frozen.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, digest := range digests {
		if err = copyArtifact(s.Root, dest, digest); err != nil {
			return err
		}
	}
	for _, ref := range evidence {
		if err = copyEvidence(s.Root, dest, ref); err != nil {
			return err
		}
	}
	h, _, err := privatefs.Hash(path)
	if err != nil {
		return err
	}
	index := backupIndex{SchemaVersion: 1, Mode: s.Mode, CreatedAt: now(), DatabaseSHA256: h, Artifacts: digests, Identities: identities, Evidence: evidence}
	data, _ := json.MarshalIndent(index, "", "  ")
	return privatefs.Write(filepath.Join(dest, "index.json"), data)
}

func Restore(root, source, mode string) error {
	var absErr error
	root, absErr = filepath.Abs(root)
	if absErr != nil {
		return absErr
	}
	source, absErr = filepath.Abs(source)
	if absErr != nil {
		return absErr
	}
	if err := privatefs.Disjoint(root, source); err != nil {
		return err
	}
	if mode != manifest.Production && mode != manifest.Simulation {
		return fmt.Errorf("invalid restore mode")
	}
	data, err := privatefs.Read(filepath.Join(source, "index.json"), 4<<20)
	if err != nil {
		return err
	}
	var index backupIndex
	if err = json.Unmarshal(data, &index); err != nil || index.SchemaVersion != 1 || index.Mode != mode || !manifest.ValidHash(index.DatabaseSHA256) {
		return fmt.Errorf("unsupported backup index")
	}
	h, _, err := privatefs.Hash(filepath.Join(source, "state.sqlite"))
	if err != nil {
		return err
	}
	if h != index.DatabaseSHA256 {
		return fmt.Errorf("backup database digest mismatch")
	}
	if err = privatefs.Dir(root); err != nil {
		return err
	}
	lifecycle, err := lock(root, ".lifecycle.lock", syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer lifecycle.Close()
	writer, err := lock(root, ".writer.lock", syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer writer.Close()
	stage, err := os.MkdirTemp(root, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = privatefs.Copy(filepath.Join(source, "state.sqlite"), filepath.Join(stage, "state.sqlite")); err != nil {
		return err
	}
	incoming, err := sql.Open("sqlite", filepath.Join(stage, "state.sqlite"))
	if err != nil {
		return err
	}
	incoming.SetMaxOpenConns(1)
	defer incoming.Close()
	var version int
	var storedMode, integrity string
	if err = incoming.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != 1 && version != 2 && version != 3 {
		return fmt.Errorf("unsupported backup database schema")
	}
	if err = incoming.QueryRow("SELECT value FROM meta WHERE key='mode'").Scan(&storedMode); err != nil || storedMode != mode {
		return fmt.Errorf("backup mode mismatch")
	}
	if err = incoming.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return fmt.Errorf("backup failed integrity check")
	}
	digests, identities, err := inventory(incoming)
	if err != nil {
		return err
	}
	if !slices.Equal(digests, index.Artifacts) {
		return fmt.Errorf("backup artifact index is incomplete")
	}
	evidence, err := evidenceInventory(incoming)
	if err != nil {
		return err
	}
	if !slices.Equal(evidence, index.Evidence) {
		return fmt.Errorf("backup evidence index is incomplete")
	}
	// Validate every input before copying anything into the active root.
	for _, ref := range evidence {
		if _, err = readEvidence(source, ref); err != nil {
			return err
		}
	}
	// Keep the current inventory as well: the restored backup can predate whole releases.
	if _, err = os.Stat(filepath.Join(root, "state.sqlite")); err == nil {
		current, e := sql.Open("sqlite", filepath.Join(root, "state.sqlite"))
		if e != nil {
			return e
		}
		current.SetMaxOpenConns(1)
		var currentMode string
		e = current.QueryRow("SELECT value FROM meta WHERE key='mode'").Scan(&currentMode)
		if e != nil || currentMode != mode {
			current.Close()
			return fmt.Errorf("current database mode cannot be replaced")
		}
		_, currentIdentities, e := inventory(current)
		if e != nil {
			current.Close()
			return e
		}
		identities = append(identities, currentIdentities...)
		_, e = current.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		closeErr := current.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, digest := range digests {
		if err = copyArtifact(source, root, digest); err != nil {
			return err
		}
	}
	for _, ref := range evidence {
		if err = copyEvidence(source, root, ref); err != nil {
			return err
		}
	}
	encoded, _ := json.Marshal(identities)
	if _, err = incoming.Exec("PRAGMA synchronous=FULL; UPDATE meta SET value='1' WHERE key='recovery_required';"); err != nil {
		return err
	}
	if _, err = incoming.Exec("INSERT OR REPLACE INTO meta VALUES ('restore_inventory',?),('restore_time',?),('restore_source',?)", string(encoded), now(), index.DatabaseSHA256); err != nil {
		return err
	}
	if _, err = incoming.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	if err = incoming.Close(); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = os.Remove(filepath.Join(root, "state.sqlite") + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.Rename(filepath.Join(stage, "state.sqlite"), filepath.Join(root, "state.sqlite")); err != nil {
		return err
	}
	return privatefs.SyncDir(root)
}
