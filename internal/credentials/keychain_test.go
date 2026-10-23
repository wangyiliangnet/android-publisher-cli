package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

func TestKeychainHelper(t *testing.T) {
	last := os.Args[len(os.Args)-1]
	if last == "__keychain-block" {
		time.Sleep(30 * time.Second)
		os.Exit(1)
	}
	if last != WorkerArgument {
		return
	}
	os.Exit(ServeWorker(os.NewFile(3, "request"), os.NewFile(4, "response")))
}

func TestInteractiveKeychainCannotAuthorizeMutations(t *testing.T) {
	if NewKeychain().interactive {
		t.Fatal("ordinary Keychain access became interactive")
	}
	for _, action := range []string{"put", "delete"} {
		input, send, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		receive, output, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		q := workerRequest{Action: action, Ref: Ref{Scope: account.Scope{Channel: "oppo", Account: "synthetic"}, Name: "client-secret"}, Interactive: true}
		if err = json.NewEncoder(send).Encode(q); err != nil {
			t.Fatal(err)
		}
		send.Close()
		code := ServeWorker(input, output)
		input.Close()
		output.Close()
		receive.Close()
		if code != 2 {
			t.Fatal("interactive worker accepted credential mutation")
		}
	}
}
func helperVault(t *testing.T, path string) *Keychain {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return &Keychain{executable: executable, args: []string{"-test.run=^TestKeychainHelper$", "--", WorkerArgument}, path: path, timeout: 5 * time.Second}
}
func disposableKeychain(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("native macOS Keychain integration")
	}
	path := filepath.Join(t.TempDir(), "fixture.keychain")
	before, err := exec.Command("/usr/bin/security", "list-keychains", "-d", "user").Output()
	if err != nil {
		t.Fatal("cannot snapshot Keychain search list")
	}
	password := []byte("disposable-synthetic-keychain-password")
	if err := testKeychainControl(path, password, 0); err != nil {
		t.Fatal("disposable Keychain creation:", err)
	}
	after, err := exec.Command("/usr/bin/security", "list-keychains", "-d", "user").Output()
	if err != nil {
		t.Fatal("cannot verify Keychain search list")
	}
	t.Cleanup(func() {
		if err := testKeychainControl(path, nil, 3); err != nil {
			t.Error("disposable Keychain cleanup:", err)
		}
	})
	if !bytes.Equal(before, after) {
		t.Fatal("disposable fixture changed user Keychain search list")
	}
	return path
}
func TestNativeKeychainCRUDAndLocked(t *testing.T) {
	path := disposableKeychain(t)
	vault := helperVault(t, path)
	ref := Ref{account.Scope{Channel: "oppo", Account: "synthetic-account"}, "client-secret"}
	ctx := context.Background()
	if _, err := vault.Get(ctx, ref); !errors.Is(err, ErrMissing) {
		t.Fatal("missing classification:", err)
	}
	value := []byte("中文 credential + & space\nsecond line\x00binary")
	secret, _ := NewSecret(value)
	defer secret.Destroy()
	if err := vault.Put(ctx, ref, secret, false); err != nil {
		t.Fatal("put:", err)
	}
	if err := vault.Put(ctx, ref, secret, false); !errors.Is(err, ErrExists) {
		t.Fatal("duplicate:", err)
	}
	got, err := vault.Get(ctx, ref)
	if err != nil {
		t.Fatal("get:", err)
	}
	if !bytes.Equal(got.Bytes(), value) {
		t.Fatal("Keychain changed credential bytes")
	}
	got.Destroy()
	updated, _ := NewSecret([]byte("rotated test value"))
	defer updated.Destroy()
	if err := vault.Put(ctx, ref, updated, true); err != nil {
		t.Fatal("replace:", err)
	}
	if err := testKeychainControl(path, nil, 1); err != nil {
		t.Fatal("lock fixture:", err)
	}
	started := time.Now()
	if _, err := vault.Get(ctx, ref); !errors.Is(err, ErrInteraction) && !errors.Is(err, ErrDenied) {
		t.Fatal("locked Keychain must require explicit access:", err)
	}
	if time.Since(started) > 6*time.Second {
		t.Fatal("locked Keychain exceeded bound")
	}
	if err := testKeychainControl(path, []byte("disposable-synthetic-keychain-password"), 2); err != nil {
		t.Fatal("unlock fixture:", err)
	}
	if err := vault.Delete(ctx, ref); err != nil {
		t.Fatal("delete:", err)
	}
	if _, err := vault.Get(ctx, ref); !errors.Is(err, ErrMissing) {
		t.Fatal("deleted item remains:", err)
	}
}
func TestKeychainBinaryReplacementAtSamePath(t *testing.T) {
	path := disposableKeychain(t)
	bin := filepath.Join(t.TempDir(), "release")
	build := func(id string) {
		t.Helper()
		cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-ldflags=-buildid="+id, "-o", bin, "../../cmd/release")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture worker: %v: %s", err, out)
		}
	}
	build("m2-keychain-before")
	vault := &Keychain{executable: bin, args: []string{WorkerArgument}, path: path, timeout: 5 * time.Second}
	ref := Ref{account.Scope{Channel: "vivo", Account: "upgrade-fixture"}, "client-secret"}
	secret, _ := NewSecret([]byte("synthetic-upgrade-secret"))
	defer secret.Destroy()
	if err := vault.Put(context.Background(), ref, secret, false); err != nil {
		t.Fatal(err)
	}
	before, err := vault.Get(context.Background(), ref)
	if err != nil {
		t.Fatal("before upgrade:", err)
	}
	before.Destroy()
	build("m2-keychain-after")
	after, err := vault.Get(context.Background(), ref)
	if err != nil {
		if !errors.Is(err, ErrInteraction) && !errors.Is(err, ErrDenied) {
			t.Fatal("upgrade produced unclassified failure:", err)
		}
		t.Log("same-path replacement requires explicit Keychain access review:", err)
	} else {
		defer after.Destroy()
		if !bytes.Equal(after.Bytes(), secret.Bytes()) {
			t.Fatal("upgrade changed credential")
		}
		t.Log("same-path replacement retained credential access without UI")
	}
}
func TestKeychainTimeoutAndPrivatePipes(t *testing.T) {
	k := helperVault(t, "")
	k.args = []string{"-test.run=^TestKeychainHelper$", "--", "__keychain-block"}
	k.timeout = 80 * time.Millisecond
	ref := Ref{account.Scope{Channel: "oppo", Account: "test"}, "secret"}
	started := time.Now()
	_, err := k.Get(context.Background(), ref)
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != "keychain_timeout" || safe.Effect != "not_sent" {
		t.Fatal(err)
	}
	secret, _ := NewSecret([]byte("fixture-secret"))
	defer secret.Destroy()
	if err := k.Put(context.Background(), ref, secret, false); !errors.Is(err, ErrTimeout) {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("worker did not terminate promptly")
	}
	f, _ := os.Open(os.DevNull)
	defer f.Close()
	if ServeWorker(f, os.Stdout) != 2 {
		t.Fatal("ordinary descriptors accepted")
	}
}
func TestCredentialDiagnosticsNeverExposeSecret(t *testing.T) {
	const value = "unique-fixture-private-data"
	s, _ := NewSecret([]byte(value))
	defer s.Destroy()
	data, _ := json.Marshal(s)
	for _, text := range []string{fmt.Sprint(s), fmt.Sprintf("%#v", s), string(data), PublicError(fmt.Errorf("SDK rejected %s", value)).Error()} {
		if strings.Contains(text, value) {
			t.Fatal("secret in diagnostic")
		}
	}
	for _, tc := range []struct {
		status int
		want   error
	}{{-25308, ErrInteraction}, {-25315, ErrInteraction}, {-25293, ErrDenied}, {-128, ErrDenied}, {-25291, ErrUnavailable}, {-25300, ErrMissing}} {
		if !errors.Is(classifyStatus(tc.status), tc.want) {
			t.Fatal("unexpected Keychain classification")
		}
	}
}
