package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type runtimeSender func(context.Context, transport.Request) ([]byte, error)

func (s runtimeSender) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	if q.Body != nil {
		defer q.Body.Close()
	}
	return s(ctx, q)
}

func runtimeTarget(t *testing.T, channel string) Target {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.apk")
	if err := os.WriteFile(path, []byte("synthetic frozen payload"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]string{}
	target := Target{Scope: channel + "/test/pkg", IntentDigest: protocol.Digest("synthetic frozen intent"), Account: account.Scope{Channel: channel, Account: "test"}, AppID: "987654321987654321", PackageName: "test.synthetic", VersionCode: 2, VersionName: "2.0", APK: f, Notes: "合成更新说明", Policy: manifest.Policy{Mode: "after_approval"}, Metadata: metadata, Existing: Details{PackageName: "test.synthetic", AppID: "987654321987654321", VersionCode: 1, Metadata: map[string]string{}}, CompatibleDevice: 1, ABI: []string{"arm64-v8a"}}
	if channel == "vivo" {
		target.Metadata["copyrightList"] = "https://example.invalid/existing-proof"
		target.Existing.Metadata["copyrightList"] = target.Metadata["copyrightList"]
	}
	if channel == "huawei" || channel == "honor" {
		target.LanguageID = "zh-CN"
		target.Metadata = map[string]string{"lang_id": "zh-CN", "lang_app_name": "Synthetic app", "lang_intro": "Existing description", "lang_brief": "Existing brief", "lang_notes": "Existing notes"}
	}
	return target
}
func runtimeRequest(target Target, kind string) protocol.Request {
	return protocol.Request{OperationID: protocol.Digest([]string{target.IntentDigest, target.Account.Channel, kind}), TargetID: "target", Scope: target.Scope, Channel: target.Account.Channel, IntentDigest: target.IntentDigest, Kind: kind, ArtifactSHA256: target.APK.SHA256(), PackageName: target.PackageName, VersionCode: target.VersionCode, VersionName: target.VersionName, SnapshotDigest: protocol.Digest(target.Metadata), AdapterBinding: adapterBinding(target), Metadata: desiredMetadata(target), Policy: target.Policy, Checkpoints: map[string]string{}}
}
func runtimeSecret(t *testing.T, value string) *credentials.Secret {
	t.Helper()
	s, err := credentials.NewSecret([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Destroy)
	return s
}

func TestAdapterFrozenTargetAndUnknownReconciliation(t *testing.T) {
	target := runtimeTarget(t, "vivo")
	calls := 0
	run := &StaticRuntime{Wire: runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if q.Body == nil {
			t.Fatal("missing payload")
		}
		return nil, errors.New("ambiguous synthetic response")
	}), Auth: map[string]Auth{"vivo": {ID: "id", Secret: runtimeSecret(t, "secret")}}, Grants: &MemoryGrants{}}
	adapter, err := NewAdapter([]Target{target}, run)
	if err != nil {
		t.Fatal(err)
	}
	q := runtimeRequest(target, "upload")
	// Mutating the caller's input after construction cannot rewrite this intent.
	target.Metadata["injected"] = "changed"
	target.ABI[0] = "x86"
	if _, err = adapter.ExecuteOne(context.Background(), q); err == nil || calls != 1 {
		t.Fatalf("single ambiguous send: %v, calls %d", err, calls)
	}
	result, err := adapter.Reconcile(context.Background(), q)
	if err != nil || result.Result != "inconclusive" || result.Receipt != nil || calls != 1 {
		t.Fatalf("unknown reconciliation resent: %#v %v calls %d", result, err, calls)
	}
	q.VersionCode++
	if _, err = adapter.ExecuteOne(context.Background(), q); err == nil || calls != 1 {
		t.Fatalf("altered target sent: %v calls %d", err, calls)
	}
	q = runtimeRequest(runtimeTarget(t, "vivo"), "upload")
	q.OperationID = "arbitrary-operation"
	if _, err = adapter.ExecuteOne(context.Background(), q); err == nil || calls != 1 {
		t.Fatalf("unbound operation sent: %v calls %d", err, calls)
	}
}

func TestAdapterGrantNeverEntersReceiptAndCannotCrossIntent(t *testing.T) {
	target := runtimeTarget(t, "tencent")
	now := time.Now().UTC()
	calls := 0
	const signed = "https://fixture.cos.ap-test.myqcloud.com/file?q-sign-time=1%3B4102444800&private-signature=synthetic-secret"
	store := &MemoryGrants{}
	run := &StaticRuntime{Now: func() time.Time { return now }, Wire: runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if strings.Contains(q.URL, "get_file_upload_info") {
			return []byte(`{"ret":0,"pre_sign_url":"` + signed + `","serial_number":"vendor-serial"}`), nil
		}
		return nil, errors.New("unexpected send")
	}), Auth: map[string]Auth{"tencent": {ID: "id", Secret: runtimeSecret(t, "synthetic-secret")}}, Grants: store}
	adapter, err := NewAdapter([]Target{target}, run)
	if err != nil {
		t.Fatal(err)
	}
	q := runtimeRequest(target, "authorize")
	receipt, err := adapter.ExecuteOne(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(receipt)
	if bytes.Contains(b, []byte("synthetic-secret")) || bytes.Contains(b, []byte("myqcloud")) || receipt.RemoteRef == "vendor-serial" {
		t.Fatalf("sensitive grant in receipt: %s", b)
	}
	g, err := loadGrant(context.Background(), store, receipt.RemoteRef)
	if err != nil || g.URL != signed {
		t.Fatalf("grant not recoverable: %v", err)
	}
	u := runtimeRequest(target, "upload")
	u.Checkpoints["authorize"] = "material-" + account.Key("another intent")
	if _, err = adapter.ExecuteOne(context.Background(), u); err == nil || calls != 1 {
		t.Fatalf("cross-intent material used: %v calls %d", err, calls)
	}
	formatted := fmt.Sprintf("%+v %#v", g, store)
	encoded, _ := json.Marshal([]any{g, store})
	if strings.Contains(formatted, "synthetic-secret") || bytes.Contains(encoded, []byte("synthetic-secret")) {
		t.Fatal("public formatting leaked upload grant")
	}
}

func TestAdapterHuaweiWaitPersistsBeyondGrantExpiry(t *testing.T) {
	target := runtimeTarget(t, "huawei")
	now := time.Now().UTC()
	calls := 0
	store := &MemoryGrants{}
	run := &StaticRuntime{Now: func() time.Time { return now }, Wire: runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if q.Method != "POST" || !strings.Contains(q.URL, "app-submit") {
			t.Fatal("unexpected request")
		}
		return []byte(`{"ret":{"code":0}}`), nil
	}), Auth: map[string]Auth{"huawei": {Token: runtimeSecret(t, "synthetic-jwt")}}, Grants: store}
	adapter, err := NewAdapter([]Target{target}, run)
	if err != nil {
		t.Fatal(err)
	}
	q := runtimeRequest(target, "submit")
	bindID := protocol.Digest([]string{q.IntentDigest, q.Channel, "bind"})
	key, err := saveGrant(context.Background(), store, bindID, UploadGrant{Ref: "uploaded-object", ReadyAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	q.Checkpoints["bind"] = key
	q.Checkpoints["metadata"] = ack(protocol.Digest([]string{q.IntentDigest, q.Channel, "metadata"})).Ref
	_, err = adapter.ExecuteOne(context.Background(), q)
	var outcome protocol.WriteOutcome
	if !errors.As(err, &outcome) || outcome.WriteEffect() != "not_sent" || calls != 0 {
		t.Fatalf("processing delay lost: %v calls %d", err, calls)
	}
	// Recreating the adapter reads the durable deadline, not a process-local timer.
	now = now.Add(3 * time.Minute)
	adapter, err = NewAdapter([]Target{target}, run)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.ExecuteOne(context.Background(), q)
	if err != nil || receipt.Effect != "confirmed_applied" || calls != 1 {
		t.Fatalf("ready package not submitted once: %v calls %d", err, calls)
	}
}

func TestAdapterExpectedLanguageChangeIsPure(t *testing.T) {
	target := runtimeTarget(t, "honor")
	adapter, err := NewAdapter([]Target{target}, &StaticRuntime{Wire: runtimeSender(func(context.Context, transport.Request) ([]byte, error) { t.Fatal("unexpected HTTP"); return nil, nil }), Grants: &MemoryGrants{}})
	if err != nil {
		t.Fatal(err)
	}
	before := protocol.RemoteSnapshot{Scope: target.Scope, AdapterBinding: adapterBinding(target), Metadata: copyFields(target.Metadata)}
	after, err := adapter.ExpectedSnapshot(runtimeRequest(target, "metadata"), before)
	if err != nil {
		t.Fatal(err)
	}
	if before.Metadata["lang_notes"] != "Existing notes" || after.Metadata["lang_notes"] != target.Notes {
		t.Fatal("snapshot was mutated or notes lost")
	}
	before.Metadata["lang_intro"] = "external change"
	if _, err = adapter.ExpectedSnapshot(runtimeRequest(target, "metadata"), before); err == nil {
		t.Fatal("external drift accepted as own change")
	}
}

type runtimeVault struct {
	mu     sync.Mutex
	values map[credentials.Ref][]byte
}

func (v *runtimeVault) Get(_ context.Context, ref credentials.Ref) (*credentials.Secret, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.values[ref]
	if !ok {
		return nil, credentials.ErrMissing
	}
	return credentials.NewSecret(b)
}
func (v *runtimeVault) Put(_ context.Context, ref credentials.Ref, s *credentials.Secret, replace bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.values == nil {
		v.values = map[credentials.Ref][]byte{}
	}
	if _, ok := v.values[ref]; ok && !replace {
		return credentials.ErrExists
	}
	v.values[ref] = s.Bytes()
	return nil
}
func (v *runtimeVault) Delete(_ context.Context, ref credentials.Ref) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, ref)
	return nil
}

func TestRuntimeBindsCachedTokenToClientIdentity(t *testing.T) {
	ctx := context.Background()
	scope := account.Scope{Channel: "honor", Account: "synthetic"}
	vault := &runtimeVault{}
	secret := runtimeSecret(t, "same synthetic client secret")
	if err := vault.Put(ctx, credentials.Ref{Scope: scope, Name: "api"}, secret, false); err != nil {
		t.Fatal(err)
	}
	manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
	calls := 0
	run := Runtime{Manager: manager, sender: func(lease *account.Lease, got account.Scope) Sender {
		if !lease.Valid(got) {
			t.Fatal("credential use outside lease")
		}
		return runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
			calls++
			body, _ := io.ReadAll(q.Body)
			if !bytes.Contains(body, []byte("client_secret=")) {
				t.Fatal("missing token form")
			}
			return []byte(`{"access_token":"synthetic-token","expires_in":3600,"token_type":"Bearer"}`), nil
		})
	}}
	target := Target{Account: scope, Credential: CredentialConfig{ID: "first-client", SecretName: "api"}}
	var escaped Materials
	use := func(c *Client, store Materials) error {
		if string(c.Auth.Token.Bytes()) != "synthetic-token" {
			t.Fatal("wrong token")
		}
		escaped = store
		return nil
	}
	if err := run.WithClient(ctx, target, use); err != nil {
		t.Fatal(err)
	}
	if err := run.WithClient(ctx, target, use); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("same client refreshed %d times", calls)
	}
	target.Credential.ID = "second-client"
	if err := run.WithClient(ctx, target, use); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("different client reused previous token, calls %d", calls)
	}
	if _, err := escaped.Get(ctx, "material-after-lease"); !errors.Is(err, credentials.ErrInvalid) {
		t.Fatalf("material vault escaped account lease: %v", err)
	}
}

func TestAdapterObservationRequiresKnownSubmitAcknowledgement(t *testing.T) {
	target := runtimeTarget(t, "vivo")
	calls := 0
	run := &StaticRuntime{Wire: runtimeSender(func(context.Context, transport.Request) ([]byte, error) {
		calls++
		return []byte(`{"code":0,"subCode":"0","data":{"packageName":"test.synthetic","versionCode":2,"versionName":"2.0","status":3,"saleStatus":1}}`), nil
	}), Auth: map[string]Auth{"vivo": {ID: "synthetic", Secret: runtimeSecret(t, "synthetic-secret")}}, Grants: &MemoryGrants{}}
	adapter, err := NewAdapter([]Target{target}, run)
	if err != nil {
		t.Fatal(err)
	}
	known := ack(protocol.Digest([]string{target.IntentDigest, "vivo", "submit"})).Ref
	observation, err := adapter.Observe(context.Background(), target.Scope, 2, known)
	if err != nil || observation.Review != "approved" || observation.Availability != "live" || observation.Source != "vivo_api_exact_version_after_ack" {
		t.Fatalf("known accepted version observation: %#v %v", observation, err)
	}
	observation, err = adapter.Observe(context.Background(), target.Scope, 2, "arbitrary-unverified-ref")
	if err != nil || observation.Review != "unknown" || observation.Availability != "unknown" {
		t.Fatalf("unverified submission became successful: %#v %v", observation, err)
	}
	reconciled, err := adapter.Reconcile(context.Background(), runtimeRequest(target, "submit"))
	if err != nil || reconciled.Result != "inconclusive" || calls != 2 {
		t.Fatalf("observation proved ambiguous write: %#v %v", reconciled, err)
	}
}
