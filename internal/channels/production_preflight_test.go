package channels

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestPreparedProductionFiveChannelsCannotWriteOrExposeMetadata(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	private, _ := x509.MarshalPKCS8PrivateKey(key)
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			root := t.TempDir()
			f, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "fixture.apk"), artifact.Info{PackageName: "com.example.app", VersionCode: 248, VersionName: "3.2.7", ABI: []string{"arm64-v8a"}})
			if err != nil {
				t.Fatal(err)
			}
			m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "fixture-release", Mode: manifest.Production, PackageName: "com.example.app", VersionCode: 248, VersionName: "3.2.7", NotesFile: "notes", Artifacts: map[string]manifest.Artifact{"apk": f}, ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{channel: {AccountRef: "fixture", AppRef: "com.example.app", Artifact: "apk"}}}
			snapshot := manifest.Snapshot{Manifest: m, Notes: "修复问题并改进体验"}
			snapshot.Digest = snapshot.CalculateDigest()
			if err = artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}); err != nil {
				t.Fatal(err)
			}
			store, err := state.Open(root, manifest.Production, false)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if _, err = store.Save(snapshot); err != nil {
				t.Fatal(err)
			}
			targets, _ := store.Targets(m.ReleaseID)
			cfg := PreparationConfig{AccountQuery: AccountQuery{Channel: channel, Account: "fixture", ClientID: "fixture-id", SecretName: "credential", PublicKeyName: "public", PackageName: m.PackageName}}
			if channel == "huawei" {
				cfg.ClientID = ""
			}
			if channel == "tencent" {
				cfg.AppID = "123"
			}
			icon := filepath.Join(t.TempDir(), "icon.png")
			os.WriteFile(icon, []byte("synthetic image bytes"), 0600)
			cfg.IconPath = icon
			cfg.PrivacyURL = "https://example.invalid/privacy"
			scope := cfg.target().Account
			vault := inspectionVault{credentials.Ref{Scope: scope, Name: "credential"}: []byte("SYNTHETIC-PRIVATE")}
			if channel == "huawei" {
				vault[credentials.Ref{Scope: scope, Name: "credential"}] = raw(map[string]string{"key_id": "fixture", "sub_account": "fixture", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))})
			}
			if channel == "xiaomi" {
				vault[credentials.Ref{Scope: scope, Name: "public"}] = pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
			}
			r := Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}}
			changed := false
			calls := 0
			r.sender = func(lease *account.Lease, _ account.Scope) Sender {
				return m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
					calls++
					if !lease.Valid(scope) || q.Class == transport.Write {
						t.Fatal("preflight made a business write")
					}
					u, _ := url.Parse(q.URL)
					if q.Body != nil {
						m5Body(t, q)
					}
					switch u.Path {
					case "/api/publish/v2/appid-list":
						return []byte(`{"ret":{"code":0},"appids":[{"value":"123"}]}`), nil
					case "/api/publish/v2/app-info":
						return raw(map[string]any{"ret": map[string]any{"code": 0}, "appInfo": map[string]any{"versionCode": 247}, "languages": []any{map[string]string{"lang": "zh-CN", "appName": "合成应用", "appDesc": "SYNTHETIC-PRIVATE", "briefInfo": "简介", "newFeatures": "旧版说明"}}}), nil
					case "/auth/token":
						return []byte(`{"access_token":"synthetic-token","token_type":"Bearer","expires_in":3600}`), nil
					case "/openapi/v1/publish/get-app-id":
						return []byte(`{"code":0,"data":[{"appId":123,"packageName":"com.example.app"}]}`), nil
					case "/openapi/v1/publish/get-app-detail":
						return raw(map[string]any{"code": 0, "data": map[string]any{"basicInfo": map[string]any{"appId": 123, "packageName": "com.example.app"}, "releaseInfo": map[string]any{"versionCode": 247}, "languageInfo": []any{map[string]string{"languageId": "zh_CN", "appName": "合成应用", "intro": "SYNTHETIC-PRIVATE", "briefIntro": "简介", "newFeature": "旧版说明"}}}}), nil
					case "/devupload/dev/query":
						return []byte(`{"result":0,"updateVersion":true,"packageInfo":{"packageName":"com.example.app","versionCode":247,"appName":"SYNTHETIC-PRIVATE"}}`), nil
					case "/router/rest":
						return raw(map[string]any{"code": 0, "subCode": "0", "data": map[string]any{"packageName": "com.example.app", "versionCode": "247", "mainTitle": "合成应用", "detailDesc": strings.Repeat("简介", 30), "copyrightList": "https://example.invalid/SYNTHETIC-PRIVATE", "compatibleDevice": 1}}), nil
					case "/open_file/developer_api/query_app_detail":
						version := 247
						if changed {
							version = 248
						}
						return raw(map[string]any{"ret": 0, "pkg_name": "com.example.app", "version_code": version, "app_name": "SYNTHETIC-PRIVATE"}), nil
					}
					t.Fatal("unexpected read route")
					return nil, nil
				})
			}
			p, err := r.NewPreparedProduction(context.Background(), PreparationInput{Snapshot: snapshot, Scope: targets[0].Scope, APKPath: artifact.Path(root, f.SHA256), Config: cfg}, "")
			if err != nil {
				t.Fatal(err)
			}
			e := engine.Engine{Store: store, Inspector: artifact.SimulationInspector{}, Adapter: p}
			summary, err := e.Preflight(context.Background(), m.ReleaseID, nil)
			if err != nil || len(summary.Operations) != 0 || summary.Targets[0].Phase != "ready" {
				t.Fatalf("preflight failed: %v", err)
			}
			encoded, _ := json.Marshal(summary)
			if strings.Contains(string(encoded), "SYNTHETIC-PRIVATE") || strings.Contains(string(encoded), "https://") {
				t.Fatal("private baseline leaked")
			}
			before := calls
			if _, err = e.Submit(context.Background(), m.ReleaseID, nil); err == nil || calls != before {
				t.Fatal("read-only adapter reached a write")
			}
			if channel == "tencent" {
				changed = true
				if _, err = p.Inspect(context.Background(), targets[0].Scope); err == nil {
					t.Fatal("changed remote version accepted")
				}
			}
		})
	}
}
