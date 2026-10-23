// Package privatefs owns files beneath the publisher's private data directory.
package privatefs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func Dir(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private directory is not a regular directory: %s", path)
		}
		return os.Chmod(path, 0700)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
	}
	if err := os.Mkdir(path, 0700); os.IsExist(err) {
		// Another process may create the same managed directory concurrently.
		// Recheck its type before accepting it, including symlink rejection.
		return Dir(path)
	} else {
		return err
	}
}

func Open(path string, flags int) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	return f, nil
}

func Read(path string, limit int64) ([]byte, error) {
	f, err := Open(path, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds allowed size")
	}
	return data, nil
}

// Disjoint protects a data directory from being used as its own backup/restore input.
func Disjoint(a, b string) error {
	canonical := func(path string) (string, error) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		parent := abs
		for {
			if _, err := os.Lstat(parent); err == nil {
				real, err := filepath.EvalSymlinks(parent)
				if err != nil {
					return "", err
				}
				rel, err := filepath.Rel(parent, abs)
				if err != nil {
					return "", err
				}
				return filepath.Join(real, rel), nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
			next := filepath.Dir(parent)
			if next == parent {
				return "", fmt.Errorf("cannot resolve directory")
			}
			parent = next
		}
	}
	x, err := canonical(a)
	if err != nil {
		return err
	}
	y, err := canonical(b)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		x = strings.ToLower(x)
		y = strings.ToLower(y)
	}
	if x == y || strings.HasPrefix(x, y+string(os.PathSeparator)) || strings.HasPrefix(y, x+string(os.PathSeparator)) {
		return fmt.Errorf("backup and live state directories must be disjoint")
	}
	return nil
}

func Hash(path string) (string, int64, error) {
	f, err := Open(path, os.O_RDONLY)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func Write(path string, data []byte) error {
	if err := Dir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

func Copy(src, dst string) error {
	in, err := Open(src, os.O_RDONLY)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := Dir(filepath.Dir(dst)); err != nil {
		return err
	}
	out, err := Open(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return SyncDir(filepath.Dir(dst))
}
