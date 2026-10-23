package credentials

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

var ErrReplacementConflict = &Error{"token_replacement_conflict", "not_sent"}

type tokenReplacement struct {
	SchemaVersion int            `json:"schema_version"`
	GrantKey      string         `json:"grant_key"`
	Previous      pendingRefresh `json:"previous_intent"`
	PreviousHash  string         `json:"previous_sha256"`
	PreviousState string         `json:"previous_state"`
	NextID        string         `json:"next_id"`
	CreatedAt     time.Time      `json:"created_at"`
	FetchEffect   string         `json:"fetch_effect"`
	CacheSaved    bool           `json:"cache_saved"`
}

func canonicalHex(value string, size int) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == size && hex.EncodeToString(b) == value
}

// ReplaceUnknownHonorToken issues one new client_credentials grant under the
// account lease. It archives the previous intent as UNKNOWN, not as rejected or
// applied. This is an explicit token-lifecycle replacement, not an idempotent
// replay or a publishing-write recovery. Only a first unknown Honor acquisition
// is eligible; an ambiguous replacement cannot be replaced again this way.
func (s *Session) ReplaceUnknownHonorToken(ctx context.Context, name, grant, expected string, fetch FetchToken) (*Secret, error) {
	if !s.lease.Valid(s.scope) || s.scope.Channel != "honor" || !ValidCredentialName(name) || !strings.HasPrefix(grant, "publishing-") || !canonicalHex(strings.TrimPrefix(grant, "publishing-"), 32) || !canonicalHex(expected, 32) || fetch == nil {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.lease.Context(), cancel)
	defer stop()
	key := account.Key(name + "\x00" + grant)
	marker := "refresh-" + key
	data, err := s.lease.Read(marker)
	if err != nil {
		return nil, ErrReplacementConflict
	}
	hash := sha256.Sum256(data)
	var previous pendingRefresh
	if hex.EncodeToString(hash[:]) != expected || json.Unmarshal(data, &previous) != nil || !canonicalHex(previous.ID, 16) || previous.ReplacementOf != "" {
		return nil, ErrReplacementConflict
	}
	credential, err := s.Secret(ctx, name)
	if err != nil {
		return nil, err
	}
	defer credential.Destroy()
	ref := Ref{s.scope, "token-" + key}
	cached, cacheErr := s.manager.Vault.Get(ctx, ref)
	if cacheErr == nil {
		defer cached.Destroy()
		var record cacheRecord
		if json.Unmarshal(cached.data, &record) != nil {
			return nil, &Error{"token_cache_invalid", "not_sent"}
		}
		defer clear(record.Value)
		if record.RefreshID == previous.ID {
			// The ordinary path can reconcile this completed cache write.
			return nil, ErrReplacementConflict
		}
	} else if !errors.Is(cacheErr, ErrMissing) {
		return nil, cacheErr
	}
	historyName := "token-replacement-" + previous.ID
	if _, err = s.lease.Read(historyName); !os.IsNotExist(err) {
		return nil, ErrReplacementConflict
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, ErrUnavailable
	}
	id := hex.EncodeToString(nonce[:])
	history := tokenReplacement{SchemaVersion: 1, GrantKey: key, Previous: previous, PreviousHash: expected, PreviousState: "unknown", NextID: id, CreatedAt: time.Now().UTC(), FetchEffect: "not_started"}
	saveHistory := func() error {
		b, _ := json.Marshal(history)
		return s.lease.Write(historyName, b)
	}
	if err = saveHistory(); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(pendingRefresh{ID: id, ReplacementOf: previous.ID})
	if err = s.lease.Write(marker, b); err != nil {
		return nil, err
	}
	result, err := fetch(ctx, credential)
	if result.Value != nil {
		defer result.Value.Destroy()
	}
	history.FetchEffect = "unknown"
	if result.Effect == "not_sent" || result.Effect == "confirmed_rejected" {
		history.FetchEffect = result.Effect
		if err = saveHistory(); err != nil {
			return nil, err
		}
		if err = s.lease.Remove(marker); err != nil {
			return nil, err
		}
		return nil, &Error{"token_refresh_rejected", "not_sent"}
	}
	if err != nil || result.Effect != "confirmed_applied" || result.Value == nil || len(result.Value.data) == 0 || time.Until(result.ExpiresAt) <= 30*time.Second {
		if err = saveHistory(); err != nil {
			return nil, err
		}
		return nil, ErrRefreshUnknown
	}
	history.FetchEffect = "confirmed_applied"
	binding := sha256.Sum256(credential.data)
	b, _ = json.Marshal(cacheRecord{result.Value.data, result.ExpiresAt.UTC(), hex.EncodeToString(binding[:]), id})
	defer clear(b)
	stored, err := NewSecret(b)
	if err != nil {
		return nil, ErrRefreshUnknown
	}
	defer stored.Destroy()
	if s.manager.Vault.Put(ctx, ref, stored, true) != nil {
		_ = saveHistory()
		return nil, ErrRefreshUnknown
	}
	history.CacheSaved = true
	if err = saveHistory(); err != nil {
		return nil, err
	}
	if err = s.lease.Remove(marker); err != nil {
		return nil, err
	}
	return NewSecret(result.Value.data)
}
