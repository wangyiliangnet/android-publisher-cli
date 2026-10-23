package account

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

var testScope = Scope{Channel: "oppo", Account: "stable-account"}

func TestAccountWorker(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--account-worker" {
		return
	}
	c := Coordinator{Root: os.Args[len(os.Args)-1]}
	if err := c.With(context.Background(), testScope, func(*Lease) error { fmt.Println("locked"); var b [1]byte; _, err := os.Stdin.Read(b[:]); return err }); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
func TestProcessOwnershipDeathAndLeaseScope(t *testing.T) {
	c := Coordinator{Root: t.TempDir()}
	executable, _ := os.Executable()
	cmd := exec.Command(executable, "-test.run=^TestAccountWorker$", "--", "--account-worker", c.Root)
	stdout, _ := cmd.StdoutPipe()
	input, _ := cmd.StdinPipe()
	defer input.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatal("worker failed to acquire lock")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := c.With(ctx, testScope, func(*Lease) error { t.Fatal("second process bypassed account owner"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	cmd.Process.Kill()
	cmd.Wait()
	var escaped *Lease
	if err := c.With(context.Background(), testScope, func(l *Lease) error {
		escaped = l
		if !l.Valid(testScope) || l.Valid(Scope{Channel: "vivo", Account: testScope.Account}) {
			t.Fatal("scope mismatch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if escaped.Valid(testScope) || escaped.Wait(context.Background(), 0) != ErrInvalid {
		t.Fatal("escaped lease remained usable")
	}
}
func TestQuotaSurvivesCoordinatorReopen(t *testing.T) {
	c := Coordinator{Root: t.TempDir()}
	if err := c.With(context.Background(), testScope, func(l *Lease) error { return l.Wait(context.Background(), 250*time.Millisecond) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	other := Coordinator{Root: c.Root}
	if err := other.With(ctx, testScope, func(l *Lease) error { return l.Wait(ctx, 0) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("persisted request start was bypassed:", err)
	}
}
