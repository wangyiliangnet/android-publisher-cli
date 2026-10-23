package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
)

type cliVault struct {
	value []byte
	err   error
	puts  int
}
type canceledReadVault struct {
	cliVault
	cancel context.CancelFunc
}

func (v *canceledReadVault) Get(context.Context, credentials.Ref) (*credentials.Secret, error) {
	v.cancel()
	return nil, &credentials.Error{Code: "keychain_timeout", Effect: "not_sent"}
}

func TestCanceledCredentialWorkerCheckSuggestsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vault := &canceledReadVault{cancel: cancel}
	manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
	var out, errOut bytes.Buffer
	code := credentialsCommand(ctx, "production", []string{"check", "--channel", "oppo", "--account", "stable-account", "--name", "client"}, strings.NewReader(""), &out, &errOut, manager)
	var result map[string]any
	if code != 130 || json.Unmarshal(out.Bytes(), &result) != nil || result["effect"] != "not_sent" || result["next_action"] != "retry_credential_check" {
		t.Fatalf("canceled worker check suggested unrelated action: %s", out.String())
	}
}

func TestCanceledCredentialInputAndReadOnlyWaitAreNotSent(t *testing.T) {
	for _, action := range []string{"import", "check"} {
		t.Run(action, func(t *testing.T) {
			vault := &cliVault{}
			manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			input, writer, _ := os.Pipe()
			defer input.Close()
			defer writer.Close()
			args := []string{action, "--channel", "oppo", "--account", "stable-account", "--name", "client"}
			if action == "import" {
				args = append(args, "--stdin")
			}
			var out, errOut bytes.Buffer
			started := time.Now()
			if code := credentialsCommand(ctx, "production", args, input, &out, &errOut, manager); code != 130 {
				t.Fatalf("canceled command exit=%d", code)
			}
			var result map[string]any
			if json.Unmarshal(out.Bytes(), &result) != nil || result["effect"] != "not_sent" || vault.puts != 0 {
				t.Fatalf("known zero-write cancellation mislabeled: %s", out.String())
			}
			wantNext := "retry_credential_check"
			if action == "import" {
				wantNext = "retry_credential_input"
			}
			if result["next_action"] != wantNext {
				t.Fatal("cancellation suggested unrelated Keychain access review")
			}
			if time.Since(started) > time.Second {
				t.Fatal("cancellation was not bounded")
			}
		})
	}
}

func TestCredentialWriteTimeoutRemainsUnknown(t *testing.T) {
	vault := &cliVault{err: credentials.ErrTimeout}
	manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
	var out, errOut bytes.Buffer
	code := credentialsCommand(context.Background(), "production", []string{"import", "--channel", "oppo", "--account", "stable-account", "--name", "client", "--stdin"}, strings.NewReader("synthetic-secret"), &out, &errOut, manager)
	var result map[string]any
	if code != 4 || json.Unmarshal(out.Bytes(), &result) != nil || result["effect"] != "unknown" {
		t.Fatalf("uncertain Keychain write lost its effect: exit=%d output=%s", code, out.String())
	}
}

func (v *cliVault) Get(context.Context, credentials.Ref) (*credentials.Secret, error) {
	if v.err != nil {
		return nil, v.err
	}
	return credentials.NewSecret(v.value)
}
func (v *cliVault) Put(_ context.Context, _ credentials.Ref, s *credentials.Secret, replace bool) error {
	if v.err != nil {
		return v.err
	}
	if v.value != nil && !replace {
		return credentials.ErrExists
	}
	v.value = s.Bytes()
	v.puts++
	return nil
}
func (v *cliVault) Delete(context.Context, credentials.Ref) error {
	if v.err != nil {
		return v.err
	}
	clear(v.value)
	v.value = nil
	return nil
}
func TestCredentialImportCLIAndRedaction(t *testing.T) {
	const secret = "synthetic secret 中文 + &\nline two\n"
	vault := &cliVault{}
	manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
	base := []string{"--channel", "oppo", "--account", "stable-account", "--name", "client"}
	invoke := func(want int, mode, action string, extra ...string) {
		t.Helper()
		var out, errOut bytes.Buffer
		args := append([]string{action}, base...)
		args = append(args, extra...)
		code := credentialsCommand(context.Background(), mode, args, strings.NewReader(secret), &out, &errOut, manager)
		if code != want {
			t.Fatalf("exit=%d want=%d stdout=%s", code, want, out.String())
		}
		if strings.Contains(out.String()+errOut.String(), secret) {
			t.Fatal("credential in stdout/stderr")
		}
	}
	invoke(0, "production", "import", "--stdin")
	if !bytes.Equal(vault.value, []byte(secret)) || vault.puts != 1 {
		t.Fatal("stdin credential bytes changed")
	}
	invoke(2, "production", "import", "--stdin")
	invoke(0, "production", "import", "--stdin", "--replace")
	invoke(0, "production", "check")
	invoke(2, "simulation", "check")
	invoke(2, "production", "import", "--stdin", "--file", "do-not-echo-sensitive-path")
	invoke(2, "production", "check", "--stdin")
	vault.err = credentials.ErrInteraction
	invoke(4, "production", "check")
	vault.err = credentials.ErrDenied
	invoke(4, "production", "check")
	vault.err = errors.New(secret)
	invoke(4, "production", "check")
	vault.err = nil
	invoke(0, "production", "delete")
	path := filepath.Join(t.TempDir(), "credential")
	os.WriteFile(path, []byte(secret), 0644)
	invoke(2, "production", "import", "--file", path)
	os.Chmod(path, 0600)
	invoke(0, "production", "import", "--file", path)
	if !bytes.Equal(vault.value, []byte(secret)) {
		t.Fatal("file import changed newlines")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	os.Symlink(path, alias)
	invoke(2, "production", "import", "--file", alias, "--replace")
}
