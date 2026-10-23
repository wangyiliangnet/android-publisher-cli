package privatefs_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

func TestFIFOInputIsRejectedWithoutBlocking(t *testing.T) {
	if path := os.Getenv("PRIVATEFS_TEST_FIFO"); path != "" {
		if _, err := privatefs.Read(path, 1024); err == nil {
			t.Fatal("FIFO accepted")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "input.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFIFOInputIsRejectedWithoutBlocking$")
	cmd.Env = append(os.Environ(), "PRIVATEFS_TEST_FIFO="+path)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("opening FIFO blocked before regular-file rejection")
	}
	if err != nil {
		t.Fatalf("FIFO subprocess failed: %v %s", err, output)
	}
}
