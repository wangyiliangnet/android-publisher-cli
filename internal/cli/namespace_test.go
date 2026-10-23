package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
)

func TestPublicRuntimeRootsShareOnlyTheIntendedNamespace(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "Application Support", "AndroidPublisherCLI")
	if defaultRoot(manifest.Production) != want || defaultRoot(manifest.Simulation) != filepath.Join(want, "Simulation") || account.Default().Root != filepath.Join(want, "AccountCoordination") {
		t.Fatal("runtime and account coordination roots diverged")
	}
}
