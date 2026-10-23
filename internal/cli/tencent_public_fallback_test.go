package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func TestTencentPublicFactsSurviveAuthenticatedFactoryFailure(t *testing.T) {
	root, snapshot, scope, config := fiveCLIPlan(t, "tencent")
	binding := strings.Repeat("b", 64)
	writes, observes := 0, 0
	var out bytes.Buffer
	factory := func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
		return cliFivePrepared{scope: scope, intent: snapshot.Digest, binding: binding, writes: &writes, observes: &observes}, nil
	}
	if code := fiveProductionCommand(context.Background(), "preflight", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, factory, &out); code != 0 {
		t.Fatal(out.String(), code)
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec("UPDATE targets SET phase='submitted',remote_ref='safe-ref' WHERE release_id=?", snapshot.Manifest.ReleaseID)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	reads := 0
	public := func(_ context.Context, pkg string) (*protocol.ChannelContext, error) {
		reads++
		if pkg != snapshot.Manifest.PackageName {
			t.Fatal("wrong public application")
		}
		return &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "not_queried", StoreQueryHealth: "healthy", StoreVersionName: "3.2.6", SourceURL: "https://sj.qq.com/appdetail/" + pkg}, nil
	}
	failing := func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error) {
		return nil, errors.New("RAW_PRIVATE_SENTINEL")
	}
	out.Reset()
	code := fiveProductionCommandObserved(context.Background(), "status", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, failing, public, &out)
	if code != 3 || reads != 1 || writes != 0 || strings.Contains(out.String(), "RAW_PRIVATE_SENTINEL") || !strings.Contains(out.String(), `"store_version_name":"3.2.6"`) || !strings.Contains(out.String(), `"status":"partial"`) || !strings.Contains(out.String(), `"review_state":"unknown"`) {
		t.Fatal("fallback lost typed public facts", code, reads, out.String())
	}
	s, err = state.Open(root, manifest.Production, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var operations int
	if err = s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&operations); err != nil || operations != 0 {
		t.Fatal("public fallback changed business journal", err, operations)
	}
	out.Reset()
	code = fiveProductionCommandObserved(context.Background(), "status", root, snapshot.Manifest.ReleaseID, config, "", "", nil, artifact.SimulationInspector{}, failing, public, &out)
	if code != 3 || reads != 1 {
		t.Fatal("fallback bypassed durable query schedule", code, reads)
	}
}
