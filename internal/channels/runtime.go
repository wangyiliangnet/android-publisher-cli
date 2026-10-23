package channels

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
)

// CredentialConfig contains references and public identifiers, never secret bytes.
type CredentialConfig struct {
	ID, SecretName, PublicKeyName string
}

// Materials keeps signed URLs and vendor file URLs outside the operation journal.
// Keys are opaque hashes derived from the already persisted operation identity.
type Materials interface {
	Get(context.Context, string) (*credentials.Secret, error)
	Put(context.Context, string, *credentials.Secret) error
}

// Executor retains the account lease while resolving credentials, fetching a
// token, using that token and saving sensitive response material.
type Executor interface {
	WithClient(context.Context, Target, func(*Client, Materials) error) error
}

type Runtime struct {
	Manager          *credentials.Manager
	honorReplacement string
	// sender is an in-package synthetic-test seam; production always uses Controlled.
	sender func(*account.Lease, account.Scope) Sender
	// publicStore is a synthetic seam; a synthetic API sender never silently
	// falls through to the real public web. Production leaves both seams nil.
	publicStore func(context.Context, string) (publicStoreVersion, error)
}

func (Runtime) String() string               { return "channel credential runtime" }
func (r Runtime) GoString() string           { return r.String() }
func (Runtime) MarshalJSON() ([]byte, error) { return []byte(`"channel credential runtime"`), nil }

func (r Runtime) WithClient(ctx context.Context, t Target, use func(*Client, Materials) error) error {
	if r.Manager == nil || use == nil || !t.Account.Valid() || !credentials.ValidCredentialName(t.Credential.SecretName) {
		return fail("invalid_runtime", "not_sent")
	}
	return r.Manager.WithAccount(ctx, t.Account, func(session *credentials.Session) error {
		secret, err := session.Secret(ctx, t.Credential.SecretName)
		if err != nil {
			return err
		}
		defer secret.Destroy()
		var sender Sender = Controlled{Lease: session.Lease(), Scope: t.Account}
		if r.sender != nil {
			sender = r.sender(session.Lease(), t.Account)
		}
		if t.Account.Channel == "honor" && r.honorReplacement != "" {
			sender = honorTokenRecorder{Sender: sender, materials: keychainMaterials{vault: r.Manager.Vault, scope: t.Account, lease: session.Lease()}, proof: r.honorReplacement}
		}
		client := &Client{Channel: t.Account.Channel, Wire: sender, Auth: Auth{ID: t.Credential.ID, Secret: secret}}
		switch t.Account.Channel {
		case "xiaomi":
			if !credentials.ValidCredentialName(t.Credential.PublicKeyName) {
				return fail("missing_public_key_reference", "not_sent")
			}
			public, err := session.Secret(ctx, t.Credential.PublicKeyName)
			if err != nil {
				return err
			}
			defer public.Destroy()
			client.Auth.PublicKey, err = parsePublicRSA(public, 1024)
			if err != nil {
				return err
			}
		case "huawei":
			var service struct {
				KeyID      string `json:"key_id"`
				SubAccount string `json:"sub_account"`
				PrivateKey string `json:"private_key"`
			}
			b := secret.Bytes()
			err = json.Unmarshal(b, &service)
			clear(b)
			if err != nil || service.KeyID == "" || service.SubAccount == "" || service.PrivateKey == "" {
				return fail("invalid_service_account", "not_sent")
			}
			key, err := credentials.NewSecret([]byte(service.PrivateKey))
			service.PrivateKey = ""
			if err != nil {
				return fail("invalid_service_account", "not_sent")
			}
			defer key.Destroy()
			token, err := ServiceAccountJWT(service.KeyID, service.SubAccount, key, time.Now().UTC())
			if err != nil {
				return err
			}
			defer token.Destroy()
			client.Auth = Auth{Token: token}
		case "honor", "oppo":
			fetch := func(ctx context.Context, value *credentials.Secret) (credentials.FetchResult, error) {
				client.Auth.Secret = value
				if t.Account.Channel == "honor" {
					return client.HonorToken(ctx)
				}
				return client.OPPOToken(ctx)
			}
			var token *credentials.Secret
			if t.Account.Channel == "honor" && r.honorReplacement != "" {
				token, err = session.ReplaceUnknownHonorToken(ctx, t.Credential.SecretName, "publishing-"+account.Key(t.Credential.ID), r.honorReplacement, fetch)
			} else {
				token, err = session.Token(ctx, t.Credential.SecretName, "publishing-"+account.Key(t.Credential.ID), fetch)
			}
			client.Auth.Secret = secret
			if err != nil {
				return err
			}
			defer token.Destroy()
			client.Auth.Token = token
		}
		return use(client, keychainMaterials{vault: r.Manager.Vault, scope: t.Account, lease: session.Lease()})
	})
}

func publicRSA(secret *credentials.Secret) (*rsa.PublicKey, error) {
	return parsePublicRSA(secret, 2048)
}

// The assigned Xiaomi encryption certificate may be RSA-1024. Other RSA
// consumers, including Service Account signing, keep their existing minimum.
func parsePublicRSA(secret *credentials.Secret, minimum int) (*rsa.PublicKey, error) {
	b := secret.Bytes()
	defer clear(b)
	block, rest := pem.Decode(b)
	if block != nil && len(bytes.TrimSpace(rest)) != 0 {
		return nil, fail("invalid_public_key", "not_sent")
	}
	var public *rsa.PublicKey
	if block == nil {
		if cert, err := x509.ParseCertificate(b); err == nil {
			public, _ = cert.PublicKey.(*rsa.PublicKey)
		} else if key, err := x509.ParsePKIXPublicKey(b); err == nil {
			public, _ = key.(*rsa.PublicKey)
		} else {
			public, _ = x509.ParsePKCS1PublicKey(b)
		}
	} else {
		switch block.Type {
		case "PUBLIC KEY":
			key, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err == nil {
				public, _ = key.(*rsa.PublicKey)
			}
		case "RSA PUBLIC KEY":
			public, _ = x509.ParsePKCS1PublicKey(block.Bytes)
		case "CERTIFICATE":
			certificate, err := x509.ParseCertificate(block.Bytes)
			if err == nil {
				public, _ = certificate.PublicKey.(*rsa.PublicKey)
			}
		}
	}
	if public == nil || public.N == nil || public.N.BitLen() < minimum || public.E < 3 || public.E%2 == 0 {
		return nil, fail("invalid_public_key", "not_sent")
	}
	return public, nil
}

type keychainMaterials struct {
	vault credentials.Vault
	scope account.Scope
	lease *account.Lease
}

func (keychainMaterials) String() string     { return "[REDACTED account material vault]" }
func (m keychainMaterials) GoString() string { return m.String() }
func (keychainMaterials) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED account material vault]"`), nil
}

func materialName(key string) string { return "upload-" + account.Key(key) }
func (m keychainMaterials) Get(ctx context.Context, key string) (*credentials.Secret, error) {
	if !m.lease.Valid(m.scope) {
		return nil, credentials.ErrInvalid
	}
	return m.vault.Get(ctx, credentials.Ref{Scope: m.scope, Name: materialName(key)})
}
func (m keychainMaterials) Put(ctx context.Context, key string, value *credentials.Secret) error {
	if !m.lease.Valid(m.scope) {
		return credentials.ErrInvalid
	}
	return m.vault.Put(ctx, credentials.Ref{Scope: m.scope, Name: materialName(key)}, value, false)
}

// MemoryGrants is an isolated synthetic-test vault. It has no disk persistence
// and is never used by Runtime or by the production command path.
type MemoryGrants struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (*MemoryGrants) String() string     { return "[REDACTED synthetic material vault]" }
func (m *MemoryGrants) GoString() string { return m.String() }
func (*MemoryGrants) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED synthetic material vault]"`), nil
}
func (m *MemoryGrants) Get(_ context.Context, key string) (*credentials.Secret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.values[key]
	if !ok {
		return nil, credentials.ErrMissing
	}
	return credentials.NewSecret(b)
}
func (m *MemoryGrants) Put(_ context.Context, key string, value *credentials.Secret) error {
	if key == "" || value == nil || len(value.Bytes()) == 0 {
		return credentials.ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string][]byte{}
	}
	if _, ok := m.values[key]; ok {
		return credentials.ErrExists
	}
	m.values[key] = value.Bytes()
	return nil
}

// StaticRuntime is the injection seam for protocol simulation. It never opens
// Keychain, creates a production transport or requests a real account token.
type StaticRuntime struct {
	Wire   Sender
	Auth   map[string]Auth
	Grants Materials
	Now    func() time.Time
}

func (*StaticRuntime) String() string     { return "synthetic channel runtime" }
func (s *StaticRuntime) GoString() string { return s.String() }
func (*StaticRuntime) MarshalJSON() ([]byte, error) {
	return []byte(`"synthetic channel runtime"`), nil
}
func (s *StaticRuntime) WithClient(ctx context.Context, t Target, use func(*Client, Materials) error) error {
	if s == nil || s.Wire == nil || s.Grants == nil || use == nil {
		return fail("invalid_synthetic_runtime", "not_sent")
	}
	return use(&Client{Channel: t.Account.Channel, Wire: s.Wire, Auth: s.Auth[t.Account.Channel], Now: s.Now}, s.Grants)
}

// Stored material deliberately uses a private, explicit encoder. UploadGrant's
// normal JSON and formatting methods remain redacted at every public boundary.
type grantPayload struct {
	URL, Ref, Method   string
	ExpiresAt, ReadyAt time.Time
	Headers            http.Header
}

func saveGrant(ctx context.Context, store Materials, key string, g UploadGrant) (string, error) {
	return saveMaterial(ctx, store, key, grantPayload{URL: g.URL, Ref: g.Ref, Method: g.Method, ExpiresAt: g.ExpiresAt, ReadyAt: g.ReadyAt, Headers: g.Headers})
}
func loadGrant(ctx context.Context, store Materials, key string) (UploadGrant, error) {
	var p grantPayload
	if err := loadMaterial(ctx, store, key, &p); err != nil {
		return UploadGrant{}, err
	}
	return UploadGrant{URL: p.URL, Ref: p.Ref, Method: p.Method, ExpiresAt: p.ExpiresAt, ReadyAt: p.ReadyAt, Headers: p.Headers}, nil
}
func saveMaterial(ctx context.Context, store Materials, key string, value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", fail("material_encoding_failed", "unknown")
	}
	defer clear(b)
	secret, err := credentials.NewSecret(b)
	if err != nil {
		return "", fail("material_encoding_failed", "unknown")
	}
	defer secret.Destroy()
	ref := "material-" + account.Key(key)
	if store == nil {
		return "", fail("material_store_unavailable", "unknown")
	}
	if err = store.Put(ctx, ref, secret); err != nil {
		return "", fail("material_store_failed", "unknown")
	}
	return ref, nil
}
func loadMaterial(ctx context.Context, store Materials, key string, value any) error {
	if store == nil || len(key) != 73 || key[:9] != "material-" {
		return fail("checkpoint_missing", "not_sent")
	}
	secret, err := store.Get(ctx, key)
	if err != nil {
		if errors.Is(err, credentials.ErrMissing) {
			return fail("checkpoint_unavailable", "not_sent")
		}
		return credentials.PublicError(err)
	}
	defer secret.Destroy()
	b := secret.Bytes()
	defer clear(b)
	if json.Unmarshal(b, value) != nil {
		return fail("checkpoint_invalid", "not_sent")
	}
	return nil
}
