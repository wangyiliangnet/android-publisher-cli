package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestHonorOAuthRejectionsRequireCompleteTokenErrorEvidence(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		reject bool
	}{
		{400, `{"error":"invalid_client","error_description":"SYNTHETIC-PRIVATE"}`, true},
		{401, `{"error":"invalid_client"}`, true}, {400, `{"error":"invalid_request"}`, true},
		{200, `{"error":"invalid_client"}`, false}, {500, `{"error":"invalid_client"}`, false},
		{400, `{"error":"SYNTHETIC-PRIVATE"}`, false}, {400, `{"error":123}`, false},
		{400, `{"error":"invalid_client","access_token":null}`, false},
		{400, `{"error":"invalid_client","error":"invalid_request"}`, false},
		{400, `{"error":"invalid_client"`, false},
	} {
		err := honorOAuthRejection(tc.status, []byte(tc.body), &transport.Error{Code: "http_status", Status: tc.status, Effect: "unknown"})
		if (err != nil) != tc.reject {
			t.Fatalf("wrong classification for status %d", tc.status)
		}
		if err != nil {
			if strings.Contains(err.Error(), "SYNTHETIC-PRIVATE") {
				t.Fatal("OAuth description leaked")
			}
			var vendor *Error
			if !errors.As(err, &vendor) || vendor.Effect != "confirmed_rejected" {
				t.Fatal("rejection lost certainty")
			}
			c := m5Client(t, "honor")
			c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) { return nil, err })
			result, _ := c.HonorToken(context.Background())
			if result.Effect != "confirmed_rejected" {
				t.Fatal("token manager cannot retain rejection proof")
			}
		}
	}
}

func TestHonorAuditedReplacementUsesOnlyTokenAndBoundQueries(t *testing.T) {
	q := AccountQuery{Channel: "honor", Account: "synthetic-main", ClientID: "synthetic-id", SecretName: "client-secret", PackageName: "com.example.app"}
	scope := q.target().Account
	vault := inspectionVault{credentials.Ref{Scope: scope, Name: q.SecretName}: []byte("SYNTHETIC-PRIVATE")}
	manager := &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}
	pending := []byte(`{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	sum := sha256.Sum256(pending)
	proof := hex.EncodeToString(sum[:])
	key := account.Key(q.SecretName + "\x00publishing-" + account.Key(q.ClientID))
	if err := manager.WithAccount(context.Background(), scope, func(s *credentials.Session) error { return s.Lease().Write("refresh-"+key, pending) }); err != nil {
		t.Fatal(err)
	}
	r := Runtime{Manager: manager}
	calls := 0
	r.sender = func(lease *account.Lease, _ account.Scope) Sender {
		return m5Sender(func(_ context.Context, req transport.Request) ([]byte, error) {
			calls++
			if !lease.Valid(scope) || req.Class == transport.Write || req.OperationID != "" || req.Grant != nil {
				t.Fatal("replacement escaped read-only account scope")
			}
			u, _ := url.Parse(req.URL)
			switch u.Path {
			case "/auth/token":
				m5Body(t, req)
				if _, err := lease.Read("token-replacement-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
					t.Fatal("token request has no replacement intent")
				}
				return []byte(`{"access_token":"SYNTHETIC-NEW-TOKEN","token_type":"Bearer","expires_in":3600}`), nil
			case "/openapi/v1/publish/get-app-id":
				return []byte(`{"code":0,"data":[{"appId":123,"packageName":"com.example.app"}]}`), nil
			case "/openapi/v1/publish/get-app-detail":
				return []byte(`{"code":0,"data":{"basicInfo":{"appId":123,"packageName":"com.example.app"},"releaseInfo":{"versionCode":247}}}`), nil
			}
			t.Fatal("unexpected request")
			return nil, nil
		})
	}
	report, err := r.InspectHonorWithTokenReplacement(context.Background(), q, proof)
	if err != nil || calls != 3 || report.RemoteVersion != 247 {
		t.Fatalf("replacement account query: %v calls=%d", err, calls)
	}
	responseRef := credentials.Ref{Scope: scope, Name: materialName("honor-token-response-" + proof)}
	if !strings.Contains(string(vault[responseRef]), "SYNTHETIC-NEW-TOKEN") {
		t.Fatal("response was not sealed in Keychain")
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "SYNTHETIC") {
		t.Fatal("credential response leaked")
	}
}
