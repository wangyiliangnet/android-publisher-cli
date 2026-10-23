// Package account coordinates token lifecycle and request quotas across processes.
// Its root is installation-wide and is intentionally separate from release backups.
package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

var ErrInvalid = errors.New("invalid account scope or inactive account lease")
var ErrStorage = errors.New("account coordination storage is unavailable")

type Scope struct {
	Channel string `json:"channel"`
	Account string `json:"account"`
}

func (s Scope) Valid() bool {
	channel := false
	for _, c := range []string{"oppo", "vivo", "xiaomi", "huawei", "honor", "tencent"} {
		channel = channel || s.Channel == c
	}
	return channel && ValidName(s.Account)
}

func ValidName(s string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`).MatchString(s)
}
func Key(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }

type Coordinator struct{ Root string }

func Default() Coordinator {
	home, _ := os.UserHomeDir()
	return Coordinator{Root: filepath.Join(home, "Library", "Application Support", "AndroidPublisherCLI", "AccountCoordination")}
}

type Lease struct {
	scope  Scope
	dir    string
	ctx    context.Context
	active atomic.Bool
}

func (l *Lease) Valid(scope Scope) bool   { return l != nil && l.active.Load() && l.scope == scope }
func (l *Lease) Context() context.Context { return l.ctx }

// With holds the account lock through token acquisition AND the request using it.
// A second data-dir cannot create an independent production account lock.
func (c Coordinator) With(ctx context.Context, scope Scope, use func(*Lease) error) error {
	if !scope.Valid() || c.Root == "" || use == nil {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := privatefs.Dir(c.Root); err != nil {
		return ErrStorage
	}
	dir := filepath.Join(c.Root, Key(scope.Channel+"\x00"+scope.Account))
	if err := privatefs.Dir(dir); err != nil {
		return ErrStorage
	}
	f, err := privatefs.Open(filepath.Join(dir, "account.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return ErrStorage
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return ErrStorage
	}
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return ErrStorage
		}
		if err = wait(ctx, 10*time.Millisecond); err != nil {
			return err
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	l := &Lease{scope: scope, dir: dir, ctx: ctx}
	l.active.Store(true)
	defer l.active.Store(false)
	return use(l)
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Wait reserves the next start time durably, under the same account lease.
// Only metadata is stored here; credentials and tokens live in Keychain.
func (l *Lease) Wait(ctx context.Context, interval time.Duration) error {
	if l == nil || !l.active.Load() || interval < 0 || interval > time.Hour {
		return ErrInvalid
	}
	var next time.Time
	data, err := l.Read("request-start")
	if err == nil {
		if json.Unmarshal(data, &next) != nil {
			return ErrStorage
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if d := time.Until(next); d > 0 {
		if err := wait(ctx, d); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	data, _ = json.Marshal(time.Now().UTC().Add(interval))
	return l.Write("request-start", data)
}

func (l *Lease) DeferUntil(at time.Time) error {
	if l == nil || !l.active.Load() {
		return ErrInvalid
	}
	data, err := l.Read("request-start")
	if err == nil {
		var previous time.Time
		if json.Unmarshal(data, &previous) != nil {
			return ErrStorage
		}
		if previous.After(at) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err = json.Marshal(at.UTC())
	if err != nil {
		return ErrStorage
	}
	return l.Write("request-start", data)
}

func (l *Lease) path(name string) (string, error) {
	if l == nil || !l.active.Load() || !ValidName(name) {
		return "", ErrInvalid
	}
	return filepath.Join(l.dir, name+".json"), nil
}
func (l *Lease) Read(name string) ([]byte, error) {
	p, err := l.path(name)
	if err != nil {
		return nil, err
	}
	b, err := privatefs.Read(p, 16384)
	if os.IsNotExist(err) {
		return nil, err
	}
	if err != nil {
		return nil, ErrStorage
	}
	return b, nil
}
func (l *Lease) Write(name string, data []byte) error {
	p, err := l.path(name)
	if err != nil {
		return err
	}
	if len(data) > 16384 {
		return fmt.Errorf("%w: metadata too large", ErrStorage)
	}
	if privatefs.Write(p, data) != nil {
		return ErrStorage
	}
	return nil
}
func (l *Lease) Remove(name string) error {
	p, err := l.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return ErrStorage
	}
	if privatefs.SyncDir(l.dir) != nil {
		return ErrStorage
	}
	return nil
}
