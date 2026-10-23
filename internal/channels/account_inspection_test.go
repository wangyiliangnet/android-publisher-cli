package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type inspectionVault map[credentials.Ref][]byte

func (v inspectionVault) Get(_ context.Context, r credentials.Ref) (*credentials.Secret, error) {
	b, ok := v[r]
	if !ok {
		return nil, credentials.ErrMissing
	}
	return credentials.NewSecret(b)
}
func (v inspectionVault) Put(_ context.Context, r credentials.Ref, s *credentials.Secret, replace bool) error {
	if _, ok := v[r]; ok && !replace {
		return credentials.ErrExists
	}
	v[r] = s.Bytes()
	return nil
}
func (v inspectionVault) Delete(_ context.Context, r credentials.Ref) error { delete(v, r); return nil }

func TestOPPOAccountInspectionUsesCoordinatedTokenAndSanitizedReadOnlyReport(t *testing.T) {
	scope := account.Scope{Channel: "oppo", Account: "synthetic-client"}
	vault := inspectionVault{credentials.Ref{Scope: scope, Name: "client-secret"}: []byte("synthetic-secret")}
	r := Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}}
	var tokenCalls, queryCalls int
	r.sender = func(lease *account.Lease, actual account.Scope) Sender {
		if !lease.Valid(scope) || actual != scope {
			t.Fatal("missing shared account lease")
		}
		return m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
			u, _ := url.Parse(q.URL)
			switch u.Path {
			case "/developer/v1/token":
				tokenCalls++
				if q.Class != transport.Lifecycle || u.Query().Get("client_id") != "synthetic-client" || u.Query().Get("client_secret") != "synthetic-secret" {
					t.Fatal("incorrect lifecycle")
				}
				return raw(map[string]any{"errno": 0, "data": map[string]any{"access_token": "synthetic-token", "expire_in": time.Now().Add(time.Hour).Unix()}}), nil
			case "/resource/v1/app/info":
				queryCalls++
				if q.Class != transport.Read || u.Query().Get("pkg_name") != "com.example.inspection" || u.Query().Get("version_code") != "" {
					t.Fatal("incorrect application query")
				}
				return []byte(`{"errno":0,"data":{"app_id":"9007199254740993","pkg_name":"com.example.inspection","version_code":"9007199254740993","version_name":"synthetic-secret","version_id":"9007199254740994","audit_status":"111","state":"1","business_email":"synthetic-secret","icon_url":"https://fixture.invalid/?token=synthetic-token"}}`), nil
			default:
				t.Fatal("unexpected request route")
				return nil, nil
			}
		})
	}
	for range 2 {
		report, err := r.InspectOPPO(context.Background(), "synthetic-client", "client-secret", "com.example.inspection")
		if err != nil {
			t.Fatal(err)
		}
		if report.AppID != "9007199254740993" || report.RemoteVersion != 9007199254740993 || report.QueryPermission != "verified" || report.PublishingPermission != "not_verified" || report.Review != "unknown" || report.Availability != "unknown" {
			t.Fatalf("incorrect capability/state boundary: %+v", report)
		}
		b, _ := json.Marshal(report)
		if strings.Contains(string(b), "synthetic-secret") || strings.Contains(string(b), "synthetic-token") || strings.Contains(string(b), "https://") {
			t.Fatal("sensitive response escaped report")
		}
	}
	if tokenCalls != 1 || queryCalls != 2 {
		t.Fatalf("token lifecycle not shared: %d/%d", tokenCalls, queryCalls)
	}
}

func TestInspectionSenderBlocksWritesAndScopeEscapesBeforeHTTP(t *testing.T) {
	const origin = "https://oop-openapi-cn.heytapmobi.com"
	token := transport.Request{URL: origin + "/developer/v1/token?client_id=synthetic&client_secret=secret", Method: "GET", Class: transport.Lifecycle}
	query := transport.Request{URL: origin + "/resource/v1/app/info?pkg_name=com.example.app&access_token=token&timestamp=1&api_sign=signature", Method: "GET", Class: transport.Read}
	calls := 0
	s := inspectionSender{clientID: "synthetic", pkg: "com.example.app", underlying: m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte("ok"), nil })}
	for _, q := range []transport.Request{token, query} {
		if _, err := s.Send(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	bad := []transport.Request{
		{URL: origin + "/resource/v1/app/upd", Method: "GET", Class: transport.Read},
		{URL: origin + "/resource/v1/upload/get-upload-url", Method: "GET", Class: transport.Lifecycle},
		{URL: token.URL, Method: "GET", Class: transport.Read},
		{URL: query.URL, Method: "POST", Class: transport.Read},
		{URL: query.URL, Method: "GET", Class: transport.Write, OperationID: "fake-operation"},
	}
	for _, modify := range []func(*transport.Request){
		func(q *transport.Request) {
			q.URL = strings.Replace(q.URL, "oop-openapi-cn.heytapmobi.com", "fixture.invalid", 1)
		},
		func(q *transport.Request) { q.URL = strings.Replace(q.URL, "com.example.app", "com.other.app", 1) },
		func(q *transport.Request) { q.URL += "&extra=secret" },
		func(q *transport.Request) { q.URL += "&pkg_name=com.example.app" },
		func(q *transport.Request) { q.URL += "#secret" },
		func(q *transport.Request) { q.Headers = map[string][]string{"Authorization": {"secret"}} },
		func(q *transport.Request) { q.Grant = &transport.Grant{} },
	} {
		q := query
		modify(&q)
		bad = append(bad, q)
	}
	for _, q := range bad {
		if _, err := s.Send(context.Background(), q); err == nil {
			t.Fatal("unsafe request passed read-only boundary")
		}
	}
	if calls != 2 {
		t.Fatal("rejected request reached network")
	}
}

func TestOPPOAccountInspectionMissingCredentialsAndInvalidTargetSendNothing(t *testing.T) {
	r := Runtime{Manager: &credentials.Manager{Vault: inspectionVault{}, Accounts: account.Coordinator{Root: t.TempDir()}}}
	r.sender = func(*account.Lease, account.Scope) Sender {
		t.Fatal("network sender created without credentials")
		return nil
	}
	for _, args := range [][3]string{{"invalid/client", "client-secret", "com.example.app"}, {"synthetic", "token-cache", "com.example.app"}, {"synthetic", "client-secret", "https://fixture.invalid"}} {
		if _, err := r.InspectOPPO(context.Background(), args[0], args[1], args[2]); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := r.InspectOPPO(context.Background(), "synthetic", "client-secret", "com.example.app"); !errors.Is(err, credentials.ErrMissing) {
		t.Fatal("missing credential must fail before HTTP")
	}
}
