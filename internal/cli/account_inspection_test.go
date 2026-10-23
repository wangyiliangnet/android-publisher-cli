package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
)

type inspectionArtifact func(context.Context, string) (artifact.Info, error)

func (f inspectionArtifact) Inspect(c context.Context, p string) (artifact.Info, error) {
	return f(c, p)
}

type inspectionRemote func(context.Context, string, string, string) (channels.AccountInspection, error)

func (f inspectionRemote) InspectOPPO(c context.Context, id, name, pkg string) (channels.AccountInspection, error) {
	return f(c, id, name, pkg)
}

func TestAccountInspectionCLIHasSeparateProductionReadOnlyGate(t *testing.T) {
	calls := 0
	remote := inspectionRemote(func(_ context.Context, id, name, pkg string) (channels.AccountInspection, error) {
		calls++
		if id != "synthetic-client" || name != "client-secret" || pkg != "com.example.app" {
			t.Fatal("wrong frozen scope")
		}
		return channels.AccountInspection{Channel: "oppo", PackageName: pkg, QueryPermission: "verified", PublishingPermission: "not_verified"}, nil
	})
	base := []string{"inspect", "--channel", "oppo", "--account", "synthetic-client", "--package", "com.example.app"}
	var out bytes.Buffer
	if c := accountCommand(context.Background(), "production", base, nil, remote, &out, &out); c != 0 || !strings.Contains(out.String(), `"publishing_effect":"not_sent"`) {
		t.Fatalf("readonly entry failed: %d %s", c, out.String())
	}
	for _, test := range []struct {
		mode string
		args []string
	}{
		{"simulation", base},
		{"production", append(append([]string{}, base...), "--store", "https://fixture.invalid")},
		{"production", []string{"inspect", "--channel", "vivo", "--account", "synthetic-client", "--package", "com.example.app"}},
		{"production", []string{"inspect", "--channel", "oppo", "--account", "synthetic-client", "--package", "com.example.app", "--apk", "missing"}},
		{"production", []string{"submit", "--channel", "oppo", "--account", "synthetic-client", "--package", "com.example.app"}},
	} {
		out.Reset()
		if c := accountCommand(context.Background(), test.mode, test.args, nil, remote, &out, &out); c != 2 {
			t.Fatalf("invalid route accepted: %d %s", c, out.String())
		}
	}
	if calls != 1 {
		t.Fatal("invalid configuration reached production query")
	}
}

func TestAccountInspectionVerifiesArtifactBeforeAnyNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.apk")
	os.WriteFile(path, []byte("synthetic fixture, not an APK"), 0600)
	args := []string{"inspect", "--channel", "oppo", "--account", "synthetic-client", "--apk", path}
	calls := 0
	remote := inspectionRemote(func(_ context.Context, _, _, pkg string) (channels.AccountInspection, error) {
		calls++
		if pkg != "com.example.app" {
			t.Fatal("wrong APK package")
		}
		return channels.AccountInspection{PackageName: pkg}, nil
	})
	for _, kind := range []string{"bad_signature", "changed_during_inspection", "verified"} {
		os.WriteFile(path, []byte("synthetic fixture, not an APK"), 0600)
		inspector := inspectionArtifact(func(context.Context, string) (artifact.Info, error) {
			if kind == "bad_signature" {
				return artifact.Info{}, errors.New("sensitive exception")
			}
			if kind == "changed_during_inspection" {
				os.WriteFile(path, []byte("changed"), 0600)
			}
			return artifact.Info{PackageName: "com.example.app", VersionCode: 123}, nil
		})
		var out bytes.Buffer
		code := accountCommand(context.Background(), "production", args, inspector, remote, &out, &out)
		if kind == "verified" {
			if code != 0 || !strings.Contains(out.String(), `"artifact_sha256":`) {
				t.Fatal("verified artifact missing")
			}
		} else if code != 2 || strings.Contains(out.String(), "sensitive exception") {
			t.Fatal("unsafe inspection reached remote or exposed error")
		}
	}
	if calls != 1 {
		t.Fatal("network before stable APK verification")
	}
}

func TestAccountInspectionErrorsAreSanitizedAndCredentialFailureIsActionable(t *testing.T) {
	args := []string{"inspect", "--channel", "oppo", "--account", "synthetic-client", "--package", "com.example.app"}
	for _, err := range []error{errors.New("https://fixture.invalid/?client_secret=synthetic-secret"), credentials.ErrMissing} {
		remote := inspectionRemote(func(context.Context, string, string, string) (channels.AccountInspection, error) {
			return channels.AccountInspection{}, err
		})
		var out bytes.Buffer
		c := accountCommand(context.Background(), "production", args, nil, remote, &out, &out)
		if c == 0 || strings.Contains(out.String(), "synthetic-secret") || strings.Contains(out.String(), "https://") {
			t.Fatal("inspection error leaked credential")
		}
		if errors.Is(err, credentials.ErrMissing) && c != 4 {
			t.Fatal("credential failure must request local access review")
		}
	}
}

func TestTokenReplacementCannotSelectOtherChannelsOrInvalidProof(t *testing.T) {
	remote := inspectionRemote(func(context.Context, string, string, string) (channels.AccountInspection, error) {
		t.Fatal("invalid replacement reached credentials")
		return channels.AccountInspection{}, nil
	})
	for _, args := range [][]string{
		{"inspect", "--channel", "oppo", "--account", "fixture", "--package", "com.example.app", "--replace-unknown-token", strings.Repeat("a", 64)},
		{"inspect", "--channel", "huawei", "--account", "fixture", "--package", "com.example.app", "--replace-unknown-token", strings.Repeat("a", 64)},
		{"inspect", "--channel", "honor", "--account", "fixture", "--client-id", "fixture", "--package", "com.example.app", "--replace-unknown-token", "invalid"},
	} {
		var out bytes.Buffer
		if code := accountCommand(context.Background(), "production", args, nil, remote, &out, &out); code != 2 {
			t.Fatalf("unsafe replacement flags accepted: %d", code)
		}
	}
}

func TestCredentialRejectionDoesNotAskForKeychainAuthorization(t *testing.T) {
	remote := inspectionRemote(func(context.Context, string, string, string) (channels.AccountInspection, error) {
		return channels.AccountInspection{}, &credentials.Error{Code: "token_refresh_rejected", Effect: "not_sent"}
	})
	var out bytes.Buffer
	code := accountCommand(context.Background(), "production", []string{"inspect", "--channel", "oppo", "--account", "fixture", "--package", "com.example.app"}, nil, remote, &out, &out)
	if code != 4 || !strings.Contains(out.String(), `"next_action":"verify_api_credentials"`) {
		t.Fatal("rejected API credentials were reported as Keychain permission failure")
	}
}
