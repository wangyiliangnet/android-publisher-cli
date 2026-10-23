package credentials

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestControlledPipePreservesBytesAndCancels(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	go func() { write.Write([]byte("中文 + &\nline two\n")); write.Close() }()
	secret, err := ReadPipe(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	if string(secret.Bytes()) != "中文 + &\nline two\n" {
		t.Fatal("pipe bytes changed")
	}
	blocked, writer, _ := os.Pipe()
	defer blocked.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := ReadPipe(ctx, blocked); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("credential input cancellation blocked")
	}
}
