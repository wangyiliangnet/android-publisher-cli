package cli

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
)

// Each poll delegates to the existing read-only production entry point, which
// enforces the saved baseline, account coordination and durable query schedule.
// No database lock or Keychain worker is retained while waiting.
func productionWatch(ctx context.Context, root, id, config, credential string, interactive bool, subset []string, inspector artifact.Inspector, interval time.Duration, maxPolls int, out io.Writer) int {
	poll := func(ctx context.Context, w io.Writer) int {
		if config != "" {
			return runFiveProduction(ctx, "status", root, id, config, "", "", interactive, subset, inspector, w)
		}
		return runOPPOProduction(ctx, "status", root, id, credential, "", "", interactive, subset, inspector, w)
	}
	return pollProduction(ctx, poll, interval, maxPolls, out)
}
func pollProduction(ctx context.Context, poll func(context.Context, io.Writer) int, interval time.Duration, maxPolls int, out io.Writer) int {
	for cycle := 1; ; cycle++ {
		if ctx.Err() != nil {
			return 130
		}
		var line bytes.Buffer
		code := poll(ctx, &line)
		// The delegated CLI writes one bounded, sanitized JSON document.
		if _, err := io.Copy(out, &line); err != nil {
			return 5
		}
		if ctx.Err() != nil || code == 130 {
			return 130
		}
		if code == 2 || code == 4 || code == 5 || (maxPolls > 0 && cycle >= maxPolls) {
			return code
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 130
		case <-timer.C:
		}
	}
}
