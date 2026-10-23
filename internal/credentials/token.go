package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

// FetchResult is populated only after a channel adapter validates every required
// gateway/business code. Transport success alone never means token success.
type FetchResult struct {
	Value     *Secret
	ExpiresAt time.Time
	Effect    string
}
type FetchToken func(context.Context, *Secret) (FetchResult, error)
type cacheRecord struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expires_at"`
	Binding   string    `json:"binding"`
	RefreshID string    `json:"refresh_id"`
}
type pendingRefresh struct {
	ID            string `json:"id"`
	ReplacementOf string `json:"replacement_of,omitempty"`
}

// Token must be called inside WithAccount, keeping the lease through the request
// that uses the token. Cache data is never written into release state or backups.
// A crash or ambiguous refresh keeps a persistent barrier until vendor-specific
// reconciliation exists; another process must not blindly refresh again.
func (s *Session) Token(ctx context.Context, credentialName, grant string, fetch FetchToken) (*Secret, error) {
	if !s.lease.Valid(s.scope) || !account.ValidName(credentialName) || !account.ValidName(grant) || fetch == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.lease.Context(), cancel)
	defer stop()
	credential, err := s.Secret(ctx, credentialName)
	if err != nil {
		return nil, err
	}
	defer credential.Destroy()
	binding := sha256.Sum256(credential.data)
	key := account.Key(credentialName + "\x00" + grant)
	marker := "refresh-" + key
	ref := Ref{s.scope, "token-" + key}
	var cached cacheRecord
	secret, cacheErr := s.manager.Vault.Get(ctx, ref)
	if cacheErr == nil {
		defer secret.Destroy()
		if json.Unmarshal(secret.data, &cached) != nil {
			return nil, &Error{"token_cache_invalid", "not_sent"}
		}
		defer clear(cached.Value)
	} else if !errors.Is(cacheErr, ErrMissing) {
		return nil, cacheErr
	}
	data, pendingErr := s.lease.Read(marker)
	if pendingErr == nil {
		var pending pendingRefresh
		if json.Unmarshal(data, &pending) != nil || pending.ID == "" || cached.RefreshID != pending.ID {
			return nil, ErrRefreshUnknown
		}
		if err := s.lease.Remove(marker); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(pendingErr) {
		return nil, pendingErr
	}
	if cached.Binding == hex.EncodeToString(binding[:]) && len(cached.Value) > 0 && time.Until(cached.ExpiresAt) > 30*time.Second {
		return NewSecret(cached.Value)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrUnavailable
	}
	refreshID := hex.EncodeToString(nonce[:])
	data, _ = json.Marshal(pendingRefresh{ID: refreshID})
	if err := s.lease.Write(marker, data); err != nil {
		return nil, err
	}
	result, err := fetch(ctx, credential)
	if result.Value != nil {
		defer result.Value.Destroy()
	}
	if result.Effect == "not_sent" || result.Effect == "confirmed_rejected" {
		if removeErr := s.lease.Remove(marker); removeErr != nil {
			return nil, removeErr
		}
		return nil, &Error{"token_refresh_rejected", "not_sent"}
	}
	if err != nil || result.Effect != "confirmed_applied" || result.Value == nil || len(result.Value.data) == 0 || time.Until(result.ExpiresAt) <= 30*time.Second {
		return nil, ErrRefreshUnknown
	}
	cached = cacheRecord{result.Value.data, result.ExpiresAt.UTC(), hex.EncodeToString(binding[:]), refreshID}
	data, _ = json.Marshal(cached)
	defer clear(data)
	stored, err := NewSecret(data)
	if err != nil {
		return nil, ErrRefreshUnknown
	}
	defer stored.Destroy()
	if s.manager.Vault.Put(ctx, ref, stored, true) != nil {
		return nil, ErrRefreshUnknown
	}
	if s.lease.Remove(marker) != nil {
		return nil, ErrRefreshUnknown
	}
	return NewSecret(result.Value.data)
}
