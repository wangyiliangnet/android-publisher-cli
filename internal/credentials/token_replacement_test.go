package credentials

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

var replacementScope = account.Scope{Channel: "honor", Account: "synthetic-account"}
var replacementGrant = "publishing-" + account.Key("synthetic-client")
var replacementID = strings.Repeat("a", 32)

func replacementManager(root string) *Manager {
	return &Manager{Vault: &memoryVault{values: map[Ref][]byte{{replacementScope, "client"}: []byte("SYNTHETIC-PRIVATE")}}, Accounts: account.Coordinator{Root: root}}
}
func seedReplacement(t *testing.T, m *Manager) string {
	t.Helper()
	var digest string
	err := m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
		b, _ := json.Marshal(pendingRefresh{ID: replacementID})
		sum := sha256.Sum256(b)
		digest = hex.EncodeToString(sum[:])
		return s.lease.Write("refresh-"+account.Key("client\x00"+replacementGrant), b)
	})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
func TestHonorReplacementArchivesUnknownAndUsesNewConfirmedCache(t *testing.T) {
	m := replacementManager(t.TempDir())
	digest := seedReplacement(t, m)
	calls := 0
	err := m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
		token, err := s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, digest, func(context.Context, *Secret) (FetchResult, error) {
			calls++
			b, e := s.lease.Read("token-replacement-" + replacementID)
			var h tokenReplacement
			if e != nil || json.Unmarshal(b, &h) != nil || h.PreviousState != "unknown" || h.FetchEffect != "not_started" {
				t.Fatal("replacement request preceded its archived intent")
			}
			pending, _ := s.lease.Read("refresh-" + account.Key("client\x00"+replacementGrant))
			var p pendingRefresh
			json.Unmarshal(pending, &p)
			if p.ReplacementOf != replacementID || p.ID == replacementID {
				t.Fatal("new intent was not durable before HTTP")
			}
			return tokenResult("SYNTHETIC-NEW-TOKEN"), nil
		})
		if err != nil {
			return err
		}
		token.Destroy()
		b, _ := s.lease.Read("token-replacement-" + replacementID)
		var h tokenReplacement
		json.Unmarshal(b, &h)
		if h.PreviousState != "unknown" || h.FetchEffect != "confirmed_applied" || !h.CacheSaved {
			t.Fatal("old outcome overwritten or new receipt missing")
		}
		if strings.Contains(string(b), "SYNTHETIC") {
			t.Fatal("secret in account metadata")
		}
		token, err = s.Token(context.Background(), "client", replacementGrant, func(context.Context, *Secret) (FetchResult, error) {
			t.Fatal("confirmed replacement refreshed again")
			return FetchResult{}, nil
		})
		if token != nil {
			token.Destroy()
		}
		return err
	})
	if err != nil || calls != 1 {
		t.Fatalf("replacement failed: %v calls %d", err, calls)
	}
}
func TestHonorReplacementScopeProofAndStorageFailuresSendNothing(t *testing.T) {
	for _, kind := range []string{"stale-proof", "wrong-channel", "missing-marker", "archive-exists", "archive-write-fails"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			m := replacementManager(root)
			digest := seedReplacement(t, m)
			if kind == "stale-proof" {
				digest = strings.Repeat("0", 64)
			}
			scope := replacementScope
			if kind == "wrong-channel" {
				scope.Channel = "oppo"
			}
			calls := 0
			err := m.WithAccount(context.Background(), scope, func(s *Session) error {
				marker := "refresh-" + account.Key("client\x00"+replacementGrant)
				switch kind {
				case "missing-marker":
					s.lease.Remove(marker)
				case "archive-exists":
					s.lease.Write("token-replacement-"+replacementID, []byte(`{}`))
				case "archive-write-fails":
					p := filepath.Join(root, account.Key(scope.Channel+"\x00"+scope.Account), "token-replacement-"+replacementID+".json")
					os.Symlink("absent", p)
				}
				_, err := s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, digest, func(context.Context, *Secret) (FetchResult, error) { calls++; return tokenResult("SYNTHETIC"), nil })
				return err
			})
			if err == nil || calls != 0 {
				t.Fatal("unsafe replacement sent a request")
			}
		})
	}
}
func TestHonorAmbiguousReplacementCannotLoopAndRejectedAttemptKeepsHistory(t *testing.T) {
	for _, effect := range []string{"unknown", "confirmed_rejected"} {
		t.Run(effect, func(t *testing.T) {
			m := replacementManager(t.TempDir())
			digest := seedReplacement(t, m)
			calls := 0
			m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
				fetch := func(context.Context, *Secret) (FetchResult, error) {
					calls++
					return FetchResult{Effect: effect}, errors.New("SYNTHETIC-PRIVATE")
				}
				_, err := s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, digest, fetch)
				if err == nil {
					t.Fatal("unconfirmed credential accepted")
				}
				marker := "refresh-" + account.Key("client\x00"+replacementGrant)
				if effect == "unknown" {
					b, _ := s.lease.Read(marker)
					sum := sha256.Sum256(b)
					if _, err = s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, hex.EncodeToString(sum[:]), fetch); !errors.Is(err, ErrReplacementConflict) {
						t.Fatal("ambiguous replacement replayed")
					}
					if _, err = s.Token(context.Background(), "client", replacementGrant, fetch); !errors.Is(err, ErrRefreshUnknown) {
						t.Fatal("unknown replacement did not block ordinary refresh")
					}
				} else if _, err = s.lease.Read(marker); !os.IsNotExist(err) {
					t.Fatal("confirmed rejection kept new pending marker")
				}
				b, _ := s.lease.Read("token-replacement-" + replacementID)
				var h tokenReplacement
				json.Unmarshal(b, &h)
				if h.PreviousState != "unknown" || h.FetchEffect != effect || calls != 1 {
					t.Fatal("history or request bound changed")
				}
				return nil
			})
		})
	}
}
func TestHonorReplacementKillWorker(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--honor-replacement-worker" {
		return
	}
	m := replacementManager(os.Args[len(os.Args)-1])
	digest := seedReplacement(t, m)
	m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
		_, err := s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, digest, func(context.Context, *Secret) (FetchResult, error) { fmt.Println("REPLACEMENT_SENT"); select {} })
		return err
	})
	os.Exit(7)
}
func TestKilledHonorReplacementRemainsUnknownAcrossProcesses(t *testing.T) {
	root := t.TempDir()
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestHonorReplacementKillWorker$", "--", "--honor-replacement-worker", root)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != "REPLACEMENT_SENT" {
		t.Fatal("worker did not reach the request")
	}
	cmd.Process.Kill()
	cmd.Wait()
	m := replacementManager(root)
	calls := 0
	err = m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
		if _, err := s.lease.Read("token-replacement-" + replacementID); err != nil {
			t.Fatal("old intent lost after kill")
		}
		fetch := func(context.Context, *Secret) (FetchResult, error) { calls++; return tokenResult("SYNTHETIC"), nil }
		_, err := s.Token(context.Background(), "client", replacementGrant, fetch)
		return err
	})
	if !errors.Is(err, ErrRefreshUnknown) || calls != 0 {
		t.Fatal("restart refreshed an ambiguous replacement")
	}
}

func TestHonorReplacementCacheFailureKeepsNewBarrier(t *testing.T) {
	m := replacementManager(t.TempDir())
	digest := seedReplacement(t, m)
	vault := m.Vault.(*memoryVault)
	calls := 0
	err := m.WithAccount(context.Background(), replacementScope, func(s *Session) error {
		_, err := s.ReplaceUnknownHonorToken(context.Background(), "client", replacementGrant, digest, func(context.Context, *Secret) (FetchResult, error) {
			calls++
			vault.fail = ErrUnavailable
			return tokenResult("SYNTHETIC-TOKEN"), nil
		})
		vault.fail = nil
		if !errors.Is(err, ErrRefreshUnknown) {
			t.Fatal("failed cache did not block")
		}
		_, err = s.Token(context.Background(), "client", replacementGrant, func(context.Context, *Secret) (FetchResult, error) { calls++; return tokenResult("SYNTHETIC"), nil })
		b, _ := s.lease.Read("token-replacement-" + replacementID)
		var h tokenReplacement
		json.Unmarshal(b, &h)
		if h.PreviousState != "unknown" || h.FetchEffect != "confirmed_applied" || h.CacheSaved {
			t.Fatal("cache failure rewrote the old outcome or lost the new receipt")
		}
		return err
	})
	if !errors.Is(err, ErrRefreshUnknown) || calls != 1 {
		t.Fatal("cache failure retried token request")
	}
}
