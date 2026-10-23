package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/wangyiliangnet/android-publisher-cli/internal/cli"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == credentials.WorkerArgument {
		os.Exit(credentials.ServeWorker(os.NewFile(3, "keychain-request"), os.NewFile(4, "keychain-response")))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
