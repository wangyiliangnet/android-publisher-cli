package channels

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestFiveAccountQueriesKeepSecretsAndWritesInsideBoundary(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	service := raw(map[string]string{"key_id": "synthetic-key", "sub_account": "synthetic-sub", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			q := AccountQuery{Channel: channel, Account: "synthetic-main", ClientID: "synthetic-client", SecretName: "credential", PublicKeyName: "public", PackageName: "com.example.app"}
			scope := q.target().Account
			vault := inspectionVault{credentials.Ref{Scope: scope, Name: q.SecretName}: []byte("SYNTHETIC-PRIVATE")}
			if channel == "huawei" {
				q.ClientID = ""
				vault[credentials.Ref{Scope: scope, Name: q.SecretName}] = service
			}
			if channel == "xiaomi" {
				vault[credentials.Ref{Scope: scope, Name: q.PublicKeyName}] = pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
			}
			if channel == "tencent" {
				q.AppID = "123"
			}
			calls := 0
			runtime := Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}}
			runtime.sender = func(lease *account.Lease, actual account.Scope) Sender {
				return m5Sender(func(_ context.Context, request transport.Request) ([]byte, error) {
					if !lease.Valid(scope) || request.Class == transport.Write || request.OperationID != "" || request.Grant != nil {
						t.Fatal("uncoordinated or writing account query")
					}
					calls++
					u, _ := url.Parse(request.URL)
					switch u.Path {
					case "/api/publish/v2/appid-list":
						return []byte(`{"ret":{"code":0},"appids":[{"key":"SYNTHETIC-PRIVATE","value":"123"}]}`), nil
					case "/api/publish/v2/app-info":
						return []byte(`{"ret":{"code":0},"appInfo":{"versionCode":247,"versionNumber":"SYNTHETIC-PRIVATE"}}`), nil
					case "/auth/token":
						m5Body(t, request)
						return []byte(`{"token_type":"Bearer","access_token":"SYNTHETIC-PRIVATE","expires_in":3600}`), nil
					case "/openapi/v1/publish/get-app-id":
						return []byte(`{"code":0,"data":[{"packageName":"com.example.app","appId":123}]}`), nil
					case "/openapi/v1/publish/get-app-detail":
						return []byte(`{"code":0,"data":{"basicInfo":{"appId":123,"packageName":"com.example.app","secretKey":"SYNTHETIC-PRIVATE"},"releaseInfo":{"versionCode":247}}}`), nil
					case "/devupload/dev/query":
						m5Body(t, request)
						return []byte(`{"result":0,"updateVersion":true,"packageInfo":{"packageName":"com.example.app","versionCode":247,"appName":"SYNTHETIC-PRIVATE"}}`), nil
					case "/router/rest":
						m5Body(t, request)
						return []byte(`{"code":0,"subCode":"0","data":{"packageName":"com.example.app","versionCode":"247","remark":"SYNTHETIC-PRIVATE"}}`), nil
					case "/open_file/developer_api/query_app_detail":
						m5Body(t, request)
						return []byte(`{"ret":0,"pkg_name":"com.example.app","version_code":247,"introduce":"SYNTHETIC-PRIVATE"}`), nil
					}
					t.Fatal("unexpected route")
					return nil, nil
				})
			}
			report, err := runtime.InspectAccount(context.Background(), q)
			if err != nil || calls == 0 || report.RemoteVersion != 247 || report.QueryPermission != "verified" || report.PublishingPermission != "not_verified" {
				t.Fatalf("query failed: %v", err)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "SYNTHETIC-PRIVATE") {
				t.Fatal("private metadata leaked")
			}
		})
	}
}

func TestAccountQueryGuardRejectsWriteClassificationAndScopeChanges(t *testing.T) {
	q := AccountQuery{Channel: "huawei", Account: "fixture", PackageName: "com.example.app"}
	guard := &accountReadSender{query: q}
	calls := 0
	sender := m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte(`{}`), nil })
	for _, class := range []transport.Class{transport.Read, transport.Query, transport.Lifecycle, transport.Write} {
		request := transport.Request{URL: origins["huawei"] + "/api/publish/v2/upload-url/for-obs?appId=123", Method: "GET", Class: class}
		if _, err := guard.send(context.Background(), sender, request); err == nil {
			t.Fatal("upload allocation passed read-only guard")
		}
	}
	if calls != 0 {
		t.Fatal("rejected request reached HTTP")
	}
}

func TestXiaomiLegacyRSAEncryptionAndDERCertificate(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := credentials.NewSecret(der)
	defer secret.Destroy()
	public, err := parsePublicRSA(secret, 1024)
	if err != nil || public.N.BitLen() != 1024 {
		t.Fatal("legacy DER certificate rejected")
	}
	if _, err = publicRSA(secret); err == nil {
		t.Fatal("general RSA minimum was lowered")
	}
	password := m5Secret(t, strings.Repeat("synthetic", 40))
	cipher, err := XiaomiSIG(map[string]string{"RequestData": "中文 & +"}, nil, password, public)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := hex.DecodeString(cipher)
	if err != nil || len(encrypted) < 256 || len(encrypted)%128 != 0 {
		t.Fatal("incorrect legacy chunk sizes")
	}
	var plain []byte
	for len(encrypted) > 0 {
		part, e := rsa.DecryptPKCS1v15(rand.Reader, key, encrypted[:128])
		if e != nil {
			t.Fatal(e)
		}
		plain = append(plain, part...)
		encrypted = encrypted[128:]
	}
	var envelope struct {
		Password string `json:"password"`
	}
	if json.Unmarshal(plain, &envelope) != nil || envelope.Password != string(password.Bytes()) {
		t.Fatal("legacy RSA envelope changed")
	}
}

func TestAccountQueryGuardBindsOriginPackageAndLearnedAppID(t *testing.T) {
	q := AccountQuery{Channel: "huawei", Account: "fixture", PackageName: "com.example.app"}
	guard := &accountReadSender{query: q}
	calls := 0
	sender := m5Sender(func(context.Context, transport.Request) ([]byte, error) {
		calls++
		return []byte(`{"ret":{"code":0},"appids":[{"value":"123"}]}`), nil
	})
	makeRequest := func(path string) transport.Request {
		return transport.Request{URL: origins["huawei"] + path, Method: "GET", Class: transport.Read, Headers: map[string][]string{"Authorization": {"Bearer synthetic"}, "Content-Type": {"application/json; charset=utf-8"}}}
	}
	for _, path := range []string{"/api/publish/v2/app-info?appId=123", "/api/publish/v2/appid-list?packageName=com.other.app&packageTypes=1", "/api/publish/v2/appid-list?packageName=com.example.app&packageName=com.other.app&packageTypes=1"} {
		if _, err := guard.send(context.Background(), sender, makeRequest(path)); err == nil {
			t.Fatal("unbound identity reached HTTP")
		}
	}
	request := makeRequest("/api/publish/v2/appid-list?packageName=com.example.app&packageTypes=1")
	wrong := request
	wrong.URL = strings.Replace(request.URL, "connect-api.cloud.huawei.com", "example.invalid", 1)
	if wrong.URL == request.URL {
		wrong.URL = "https://example.invalid/api/publish/v2/appid-list?packageName=com.example.app&packageTypes=1"
	}
	if _, err := guard.send(context.Background(), sender, wrong); err == nil || calls != 0 {
		t.Fatal("wrong origin reached HTTP")
	}
	if _, err := guard.send(context.Background(), sender, request); err != nil || calls != 1 {
		t.Fatal("exact lookup failed")
	}
	if _, err := guard.send(context.Background(), sender, makeRequest("/api/publish/v2/app-info?appId=456")); err == nil || calls != 1 {
		t.Fatal("learned app ID was bypassed")
	}
	if _, err := guard.send(context.Background(), sender, makeRequest("/api/publish/v2/app-info?appId=123")); err != nil || calls != 2 {
		t.Fatal("bound details failed")
	}
}
