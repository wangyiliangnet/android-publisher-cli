package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"golang.org/x/sys/unix"
)

func evidenceLimit(e protocol.Evidence) (int64, error) {
	parts := strings.Split(e.Path, "/")
	if len(parts) != 3 || parts[0] != "evidence" || !manifest.ValidHash(parts[1]) || path.Clean(e.Path) != e.Path || !manifest.ValidHash(e.SHA256) {
		return 0, fmt.Errorf("invalid observation evidence reference")
	}
	var limit int64
	switch {
	case e.Kind == "text" && parts[2] == "evidence.json":
		limit = 64 << 10
	case e.Kind == "png" && parts[2] == "evidence.png":
		limit = 4 << 20
	default:
		return 0, fmt.Errorf("unsupported observation evidence reference")
	}
	if e.Size <= 0 || e.Size > limit {
		return 0, fmt.Errorf("invalid observation evidence size")
	}
	return limit, nil
}

// Open each managed path component relative to an already-open directory. This
// rejects symlinks even if a component is replaced between validation and open.
// Canonicalizing only the explicit root permits macOS's /var -> /private/var.
func withEvidenceFile(root string, e protocol.Evidence, create bool, fn func(*os.File) error) error {
	if _, err := evidenceLimit(e); err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	parent, err := unix.Open(canonical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { unix.Close(parent) }()
	parts := strings.Split(e.Path, "/")
	for _, component := range parts[:2] {
		if create {
			if err = unix.Mkdirat(parent, component, 0700); err != nil && err != unix.EEXIST {
				return err
			}
		}
		next, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		var info unix.Stat_t
		if statErr := unix.Fstat(next, &info); statErr != nil || info.Mode&0777 != 0700 {
			unix.Close(next)
			return fmt.Errorf("observation evidence directory is not private")
		}
		if create {
			if err = unix.Fsync(parent); err != nil {
				unix.Close(next)
				return err
			}
		}
		unix.Close(parent)
		parent = next
	}
	flags := unix.O_RDONLY
	if create {
		flags = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(parent, parts[2], flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), e.Path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("observation evidence file is not private and regular")
	}
	if err = fn(f); err != nil {
		return err
	}
	if create {
		return unix.Fsync(parent)
	}
	return nil
}

func readEvidence(root string, e protocol.Evidence) ([]byte, error) {
	limit, err := evidenceLimit(e)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = withEvidenceFile(root, e, false, func(f *os.File) error {
		info, err := f.Stat()
		if err != nil || info.Size() != e.Size {
			return fmt.Errorf("observation evidence size mismatch")
		}
		data, err = io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(data)) != e.Size {
			return fmt.Errorf("observation evidence cannot be read completely")
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != e.SHA256 {
			return fmt.Errorf("observation evidence digest mismatch")
		}
		return nil
	})
	return data, err
}

func validateEvidenceSet(refs []protocol.Evidence) error {
	if len(refs) == 1 && refs[0].Kind == "text" {
		_, err := evidenceLimit(refs[0])
		return err
	}
	if len(refs) != 2 {
		return fmt.Errorf("browser observation requires text and screenshot evidence")
	}
	seen := map[string]bool{}
	for _, e := range refs {
		if _, err := evidenceLimit(e); err != nil {
			return err
		}
		if seen[e.Kind] || path.Dir(e.Path) != path.Dir(refs[0].Path) {
			return fmt.Errorf("browser evidence references are inconsistent")
		}
		seen[e.Kind] = true
	}
	return nil
}

func (s *Store) validateObservationEvidence(o protocol.Observation) error {
	if o.Source == "browser_fixture" && len(o.Evidence) != 2 {
		return fmt.Errorf("browser observation requires text and screenshot evidence")
	}
	if o.Source == "manual_console" && (len(o.Evidence) != 1 || o.Evidence[0].Kind != "text") {
		return fmt.Errorf("console observation requires its attestation evidence")
	}
	if o.Source != "browser_fixture" && len(o.Evidence) == 0 {
		return nil
	}
	if err := validateEvidenceSet(o.Evidence); err != nil {
		return err
	}
	for _, e := range o.Evidence {
		if _, err := readEvidence(s.Root, e); err != nil {
			return fmt.Errorf("observation evidence is unavailable or corrupt")
		}
	}
	return nil
}

// Inventory the frozen database, including valid historical observations that
// did not become the current projection. Profiles and unreferenced files never
// enter the backup inventory.
func evidenceInventory(db *sql.DB) ([]protocol.Evidence, error) {
	rows, err := db.Query("SELECT document FROM observations ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]protocol.Evidence{}
	for rows.Next() {
		var document string
		if err = rows.Scan(&document); err != nil {
			return nil, err
		}
		var entry struct {
			Observation protocol.Observation `json:"observation"`
		}
		if json.Unmarshal([]byte(document), &entry) != nil {
			return nil, fmt.Errorf("cannot back up corrupt observation history")
		}
		refs := entry.Observation.Evidence
		if len(refs) == 0 {
			continue
		}
		if err = validateEvidenceSet(refs); err != nil {
			return nil, err
		}
		for _, e := range refs {
			if existing, ok := seen[e.Path]; ok && existing != e {
				return nil, fmt.Errorf("observation evidence references conflict")
			}
			seen[e.Path] = e
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := make([]protocol.Evidence, 0, len(seen))
	for _, e := range seen {
		result = append(result, e)
	}
	slices.SortFunc(result, func(a, b protocol.Evidence) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

func copyEvidence(source, destination string, e protocol.Evidence) error {
	data, err := readEvidence(source, e)
	if err != nil {
		return err
	}
	err = withEvidenceFile(destination, e, true, func(f *os.File) error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	})
	if err == unix.EEXIST {
		_, err = readEvidence(destination, e)
	}
	return err
}
