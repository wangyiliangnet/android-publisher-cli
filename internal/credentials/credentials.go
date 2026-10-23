package credentials

import (
	"context"
	"errors"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

const MaxSecret = 64 << 10

type Error struct {
	Code   string
	Effect string
}

func (e *Error) Error() string { return "credential operation: " + e.Code }
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

var (
	ErrInvalid        = &Error{"invalid_credential_input", "not_sent"}
	ErrMissing        = &Error{"credential_not_found", "not_sent"}
	ErrExists         = &Error{"credential_already_exists", "not_sent"}
	ErrInteraction    = &Error{"auth_required", "not_sent"}
	ErrDenied         = &Error{"credential_access_denied", "not_sent"}
	ErrUnavailable    = &Error{"keychain_unavailable", "not_sent"}
	ErrTimeout        = &Error{"keychain_timeout", "unknown"}
	ErrRefreshUnknown = &Error{"token_refresh_unknown", "unknown"}
)

// Secret deliberately refuses formatting/serialization of its content. Bytes is
// an explicit boundary for local protocol code; callers must not log those bytes.
type Secret struct{ data []byte }

func NewSecret(data []byte) (*Secret, error) {
	if len(data) == 0 || len(data) > MaxSecret {
		return nil, ErrInvalid
	}
	return &Secret{append([]byte(nil), data...)}, nil
}
func (s *Secret) String() string               { return "[REDACTED]" }
func (s *Secret) GoString() string             { return "[REDACTED]" }
func (s *Secret) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }
func (s *Secret) Bytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.data...)
}
func (s *Secret) Destroy() {
	if s != nil {
		clear(s.data)
		s.data = nil
	}
}

type Ref struct {
	Scope account.Scope `json:"scope"`
	Name  string        `json:"name"`
}

func (r Ref) Valid() bool { return r.Scope.Valid() && account.ValidName(r.Name) }
func ValidCredentialName(name string) bool {
	return account.ValidName(name) && !strings.HasPrefix(name, "token-")
}
func (r Ref) service() string {
	return "io.github.wangyiliangnet.android-publisher-cli.v1/" + r.Scope.Channel
}
func (r Ref) key() string { return r.Scope.Account + "/" + r.Name }

type Vault interface {
	Get(context.Context, Ref) (*Secret, error)
	Put(context.Context, Ref, *Secret, bool) error
	Delete(context.Context, Ref) error
}

type Manager struct {
	Vault    Vault
	Accounts account.Coordinator
}
type Session struct {
	manager *Manager
	scope   account.Scope
	lease   *account.Lease
}

func (s *Session) Lease() *account.Lease { return s.lease }
func (m *Manager) WithAccount(ctx context.Context, scope account.Scope, use func(*Session) error) error {
	if m.Vault == nil || use == nil {
		return ErrInvalid
	}
	return m.Accounts.With(ctx, scope, func(l *account.Lease) error {
		return use(&Session{manager: m, scope: scope, lease: l})
	})
}
func (m *Manager) Put(ctx context.Context, ref Ref, value *Secret, replace bool) error {
	if !ref.Valid() || !ValidCredentialName(ref.Name) || value == nil || len(value.data) == 0 {
		return ErrInvalid
	}
	return m.WithAccount(ctx, ref.Scope, func(s *Session) error { return m.Vault.Put(ctx, ref, value, replace) })
}
func (m *Manager) Delete(ctx context.Context, ref Ref) error {
	if !ref.Valid() || !ValidCredentialName(ref.Name) {
		return ErrInvalid
	}
	return m.WithAccount(ctx, ref.Scope, func(s *Session) error { return m.Vault.Delete(ctx, ref) })
}
func (m *Manager) Check(ctx context.Context, ref Ref) error {
	if !ref.Valid() || !ValidCredentialName(ref.Name) {
		return ErrInvalid
	}
	return m.WithAccount(ctx, ref.Scope, func(s *Session) error {
		secret, err := s.Secret(ctx, ref.Name)
		if secret != nil {
			secret.Destroy()
		}
		return err
	})
}
func (s *Session) Secret(ctx context.Context, name string) (*Secret, error) {
	if !s.lease.Valid(s.scope) || !ValidCredentialName(name) {
		return nil, ErrInvalid
	}
	return s.manager.Vault.Get(ctx, Ref{s.scope, name})
}

func classifyStatus(status int) error {
	switch status {
	case 0:
		return nil
	case -25300:
		return ErrMissing
	case -25299:
		return ErrExists
	case -25308, -25315:
		return ErrInteraction
	case -25293, -128, -61:
		return ErrDenied
	case -25291, -25294, -25307:
		return ErrUnavailable
	case -50:
		return ErrInvalid
	default:
		return &Error{"keychain_error", "unknown"}
	}
}

// PublicError never returns errors originating from a vendor, URL, subprocess or
// SDK. These may contain credentials; only locally defined classifications leave.
func PublicError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) {
		return &Error{"canceled", "unknown"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return &Error{"credential_storage_error", "unknown"}
}
