package cli

import (
	"context"
	"io"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
)

// Retained for callers of the original read-only preparation command. A
// preflight never enables business writes, even when the adapter supports them.
func runPreparedPreflight(ctx context.Context, root, id, configFile string, interactive bool, subset []string, inspector artifact.Inspector, out io.Writer) int {
	return runFiveProduction(ctx, "preflight", root, id, configFile, "", "", interactive, subset, inspector, out)
}
