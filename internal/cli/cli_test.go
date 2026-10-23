package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/cli"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
)

func invoke(t *testing.T, want int, args ...string) []byte {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(context.Background(), args, &out, &errOut)
	if code != want {
		t.Fatalf("%v: code %d want %d; stdout %s stderr %s", args, code, want, out.String(), errOut.String())
	}
	return out.Bytes()
}
func TestCLICompleteSimulationAndProductionGuard(t *testing.T) {
	base := t.TempDir()
	fixture := filepath.Join(base, "fixture")
	invoke(t, 0, "simulate", "init", "--dir", fixture)
	root := filepath.Join(base, "client")
	args := []string{"simulate", "--data-dir", root}
	appendArgs := func(command ...string) []string { return append(append([]string{}, args...), command...) }
	data := invoke(t, 0, appendArgs("plan", "--manifest", filepath.Join(fixture, "manifest.yaml"))...)
	var plan engine.Summary
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	fake, err := simulation.New(filepath.Join(base, "remote"), simulation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(fake)
	defer func() { server.Close(); fake.Close() }()
	for _, command := range [][]string{{"submit", "--release", "no-such", "--store", server.URL}, {"submit", "--release", plan.ReleaseID, "--channels", "typo", "--store", server.URL}} {
		data := invoke(t, 2, appendArgs(command...)...)
		var failure map[string]any
		if err := json.Unmarshal(data, &failure); err != nil {
			t.Fatal(err)
		}
		if failure["effect"] != "not_sent" || failure["next_action"] != "fix_config" {
			t.Fatalf("local input error reported remote uncertainty: %s", data)
		}
	}
	invoke(t, 0, appendArgs("preflight", "--release", plan.ReleaseID, "--store", server.URL)...)
	invoke(t, 0, appendArgs("submit", "--release", plan.ReleaseID, "--store", server.URL)...)
	data = invoke(t, 0, appendArgs("status", "--release", plan.ReleaseID)...)
	var status engine.Summary
	if err = json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	if !status.AllAccepted || status.AllApproved || status.Mode != "simulation" {
		t.Fatal("CLI misrepresented acceptance")
	}
	for _, op := range status.Operations {
		if op.OperationID == "" || op.Sequence <= 0 || op.Effect != "confirmed_applied" || op.NextAction != "observe" {
			t.Fatal("operation trace incomplete")
		}
	}
	productionRoot := filepath.Join(base, "production")
	invoke(t, 2, "--data-dir", productionRoot, "submit", "--release", "anything")
	if _, err = os.Stat(productionRoot); !os.IsNotExist(err) {
		t.Fatal("disabled production command touched state")
	}
	invoke(t, 2, "--data-dir", productionRoot, "plan", "--manifest", filepath.Join(fixture, "manifest.yaml"))
}
func TestPlanRefusesNetworkAndUnknownArguments(t *testing.T) {
	base := t.TempDir()
	invoke(t, 2, "simulate", "--data-dir", filepath.Join(base, "data"), "plan", "--manifest", "unused", "--store", "http://127.0.0.1:1")
	if _, err := os.Stat(filepath.Join(base, "data")); !os.IsNotExist(err) {
		t.Fatal("invalid offline plan opened a state store")
	}
	invoke(t, 2, "bogus")
	invoke(t, 0, "version")
	invoke(t, 0, "--version")
	invoke(t, 2, "simulate", "submit", "--release", "id", "--refresh")
	invoke(t, 0, "--help")
}
