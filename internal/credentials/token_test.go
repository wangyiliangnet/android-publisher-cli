package credentials

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

type memoryVault struct {
	mu     sync.Mutex
	values map[Ref][]byte
	fail   error
}

func (v *memoryVault) Get(ctx context.Context, r Ref) (*Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail != nil {
		return nil, v.fail
	}
	b, ok := v.values[r]
	if !ok {
		return nil, ErrMissing
	}
	return NewSecret(b)
}
func (v *memoryVault) Put(ctx context.Context, r Ref, s *Secret, replace bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail != nil {
		return v.fail
	}
	if _, ok := v.values[r]; ok && !replace {
		return ErrExists
	}
	v.values[r] = s.Bytes()
	return nil
}
func (v *memoryVault) Delete(ctx context.Context, r Ref) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.fail != nil {
		return v.fail
	}
	delete(v.values, r)
	return nil
}

var tokenScope = account.Scope{Channel: "oppo", Account: "shared-account"}

func testManager(root string) *Manager {
	return &Manager{Vault: &memoryVault{values: map[Ref][]byte{{tokenScope, "client"}: []byte("synthetic-credential")}}, Accounts: account.Coordinator{Root: root}}
}
func tokenResult(value string) FetchResult {
	s, _ := NewSecret([]byte(value))
	return FetchResult{s, time.Now().Add(time.Hour), "confirmed_applied"}
}

func TestConcurrentSharedTokenCacheAndCredentialRotation(t *testing.T) {
	m := testManager(t.TempDir())
	var calls atomic.Int32
	fetch := func(context.Context, *Secret) (FetchResult, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return tokenResult("synthetic-token"), nil
	}
	var group sync.WaitGroup
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			err := m.WithAccount(context.Background(), tokenScope, func(s *Session) error {
				token, err := s.Token(context.Background(), "client", "read-write", fetch)
				if token != nil {
					token.Destroy()
				}
				return err
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatal("shared cache refreshed more than once:", calls.Load())
	}
	// A new Manager still uses the same Keychain namespace and installation lock.
	other := &Manager{Vault: m.Vault, Accounts: account.Coordinator{Root: m.Accounts.Root}}
	rotated, _ := NewSecret([]byte("rotated-synthetic-credential"))
	defer rotated.Destroy()
	if err := other.Put(context.Background(), Ref{tokenScope, "client"}, rotated, true); err != nil {
		t.Fatal(err)
	}
	if err := other.WithAccount(context.Background(), tokenScope, func(s *Session) error {
		token, err := s.Token(context.Background(), "client", "read-write", fetch)
		if token != nil {
			token.Destroy()
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("credential rotation reused old token")
	}
	if err := filepath.Walk(m.Accounts.Root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			b, _ := os.ReadFile(path)
			for _, secret := range []string{"synthetic-token", "synthetic-credential"} {
				if bytesContains(b, secret) {
					t.Fatal("secret in coordination metadata")
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func bytesContains(b []byte, s string) bool { return strings.Contains(string(b), s) }

func TestRefreshUnknownAndFailedPersistenceNeverRefreshAgain(t *testing.T) {
	for _, failure := range []string{"ambiguous_response", "cache_write"} {
		t.Run(failure, func(t *testing.T) {
			m := testManager(t.TempDir())
			calls := 0
			fetch := func(context.Context, *Secret) (FetchResult, error) {
				calls++
				if failure == "cache_write" {
					m.Vault.(*memoryVault).fail = ErrDenied
					return tokenResult("sensitive-token"), nil
				}
				return FetchResult{}, fmt.Errorf("vendor error contains sensitive-token")
			}
			err := m.WithAccount(context.Background(), tokenScope, func(s *Session) error { _, err := s.Token(context.Background(), "client", "grant", fetch); return err })
			if !errors.Is(err, ErrRefreshUnknown) {
				t.Fatal(err)
			}
			m.Vault.(*memoryVault).fail = nil
			err = m.WithAccount(context.Background(), tokenScope, func(s *Session) error { _, err := s.Token(context.Background(), "client", "grant", fetch); return err })
			if !errors.Is(err, ErrRefreshUnknown) || calls != 1 {
				t.Fatal("ambiguous refresh was repeated:", err, calls)
			}
		})
	}
}
func TestExpiredCacheAndConfirmedRefreshRejection(t *testing.T) {
	m := testManager(t.TempDir())
	calls := 0
	fetch := func(context.Context, *Secret) (FetchResult, error) {
		calls++
		return FetchResult{Effect: "confirmed_rejected"}, errors.New("synthetic upper/lower business error")
	}
	for i := 0; i < 2; i++ {
		if err := m.WithAccount(context.Background(), tokenScope, func(s *Session) error { _, err := s.Token(context.Background(), "client", "grant", fetch); return err }); err == nil {
			t.Fatal("rejected token accepted")
		}
	}
	if calls != 2 {
		t.Fatal("confirmed rejection incorrectly left unknown barrier")
	}
	// The next successful refresh is cached; an expired cache forces one refresh.
	fetch = func(context.Context, *Secret) (FetchResult, error) { calls++; return tokenResult("fresh-token"), nil }
	use := func(s *Session) error {
		token, err := s.Token(context.Background(), "client", "grant", fetch)
		if token != nil {
			token.Destroy()
		}
		return err
	}
	if err := m.WithAccount(context.Background(), tokenScope, use); err != nil {
		t.Fatal(err)
	}
	vault := m.Vault.(*memoryVault)
	vault.mu.Lock()
	for ref, b := range vault.values {
		if ref.Name != "client" {
			var cache cacheRecord
			if json.Unmarshal(b, &cache) != nil {
				t.Fatal("invalid cache")
			}
			cache.ExpiresAt = time.Now().Add(-time.Minute)
			vault.values[ref], _ = json.Marshal(cache)
		}
	}
	vault.mu.Unlock()
	if err := m.WithAccount(context.Background(), tokenScope, use); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("expired token was reused")
	}
}
func TestTokenRefreshWorker(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--token-worker" {
		return
	}
	m := testManager(os.Args[len(os.Args)-1])
	m.WithAccount(context.Background(), tokenScope, func(s *Session) error {
		_, err := s.Token(context.Background(), "client", "grant", func(context.Context, *Secret) (FetchResult, error) {
			fmt.Println("remote-refreshed")
			var b [1]byte
			os.Stdin.Read(b[:])
			return tokenResult("token"), nil
		})
		return err
	})
	os.Exit(0)
}
func TestKilledRefreshLeavesDurableBarrier(t *testing.T) {
	root := t.TempDir()
	executable, _ := os.Executable()
	cmd := exec.Command(executable, "-test.run=^TestTokenRefreshWorker$", "--", "--token-worker", root)
	stdout, _ := cmd.StdoutPipe()
	input, _ := cmd.StdinPipe()
	defer input.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "remote-refreshed\n" {
		t.Fatal("refresh helper failed")
	}
	cmd.Process.Kill()
	cmd.Wait()
	m := testManager(root)
	calls := 0
	err = m.WithAccount(context.Background(), tokenScope, func(s *Session) error {
		_, err := s.Token(context.Background(), "client", "grant", func(context.Context, *Secret) (FetchResult, error) { calls++; return tokenResult("duplicate"), nil })
		return err
	})
	if !errors.Is(err, ErrRefreshUnknown) || calls != 0 {
		t.Fatal("process death allowed blind token refresh:", err, calls)
	}
}

func TestSyntheticGatewayAndBusinessCodesGateTokenCache(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		accepted      bool
	}{
		{"both_success", `{"code":"0","subCode":"0","token":"synthetic-token"}`, true},
		{"gateway_rejected", `{"code":"denied","subCode":"0","token":"synthetic-token"}`, false},
		{"business_rejected", `{"code":"0","subCode":"denied","token":"synthetic-token"}`, false},
		{"missing_gateway", `{"subCode":"0","token":"synthetic-token"}`, false},
		{"missing_business", `{"code":"0","token":"synthetic-token"}`, false},
		{"wrong_code_type", `{"code":0,"subCode":"0","token":"synthetic-token"}`, false},
		{"unknown_business", `{"code":"0","subCode":"new-code","token":"synthetic-token"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := testManager(t.TempDir())
			// This is a synthetic adapter contract, not a shared vendor decoder.
			fetch := func(context.Context, *Secret) (FetchResult, error) {
				var response struct {
					Code    *string `json:"code"`
					SubCode *string `json:"subCode"`
					Token   string  `json:"token"`
				}
				if json.Unmarshal([]byte(tc.payload), &response) != nil || response.Code == nil || response.SubCode == nil {
					return FetchResult{}, errors.New("synthetic schema incomplete")
				}
				if *response.Code == "denied" || *response.SubCode == "denied" {
					return FetchResult{Effect: "confirmed_rejected"}, nil
				}
				if *response.Code != "0" || *response.SubCode != "0" {
					return FetchResult{}, errors.New("unknown synthetic business code")
				}
				return tokenResult(response.Token), nil
			}
			err := m.WithAccount(context.Background(), tokenScope, func(s *Session) error {
				token, err := s.Token(context.Background(), "client", "grant", fetch)
				if token != nil {
					token.Destroy()
				}
				return err
			})
			if (err == nil) != tc.accepted {
				t.Fatal("incorrect token acceptance:", err)
			}
			if !tc.accepted {
				for ref := range m.Vault.(*memoryVault).values {
					if ref.Name != "client" {
						t.Fatal("failed business response was cached")
					}
				}
			}
		})
	}
}
