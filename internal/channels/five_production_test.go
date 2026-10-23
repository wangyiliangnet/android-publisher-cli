package channels

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type fiveFixture struct {
	store         *state.Store
	input         PreparationInput
	runtime       Runtime
	version       int64
	notes         string
	updateAllowed bool
	writes        []string
	drop, kill    string
	now           time.Time
}

func newFiveFixture(t *testing.T, channel string) *fiveFixture {
	t.Helper()
	root := t.TempDir()
	f, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "fixture.apk"), artifact.Info{PackageName: "com.example.app", VersionCode: 248, VersionName: "3.2.7", ABI: []string{"arm64-v8a"}})
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "five-production-test", Mode: manifest.Production, PackageName: "com.example.app", VersionCode: 248, VersionName: "3.2.7", NotesFile: "notes", Artifacts: map[string]manifest.Artifact{"apk": f}, ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{channel: {AccountRef: "fixture", AppRef: "com.example.app", Artifact: "apk"}}}
	snapshot := manifest.Snapshot{Manifest: m, Notes: "修复问题并改进体验\n- 保留更新说明原文"}
	snapshot.Digest = snapshot.CalculateDigest()
	if err = artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.Targets(m.ReleaseID)
	cfg := PreparationConfig{AccountQuery: AccountQuery{Channel: channel, Account: "fixture", ClientID: "fixture-id", SecretName: "credential", PublicKeyName: "public", PackageName: m.PackageName}}
	if channel == "huawei" {
		cfg.ClientID = ""
	}
	if channel == "tencent" {
		cfg.AppID = "123"
	}
	cfg.IconPath = filepath.Join(t.TempDir(), "icon.png")
	if err = os.WriteFile(cfg.IconPath, []byte("synthetic image bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.PrivacyURL = "https://example.invalid/privacy"
	vault := inspectionVault{credentials.Ref{Scope: cfg.target().Account, Name: "credential"}: []byte("SYNTHETIC-PRIVATE")}
	if channel == "huawei" || channel == "xiaomi" {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		if channel == "huawei" {
			private, _ := x509.MarshalPKCS8PrivateKey(key)
			vault[credentials.Ref{Scope: cfg.target().Account, Name: "credential"}] = raw(map[string]string{"key_id": "fixture", "sub_account": "fixture", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))})
		} else {
			vault[credentials.Ref{Scope: cfg.target().Account, Name: "public"}] = pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
		}
	}
	v := &fiveFixture{store: s, input: PreparationInput{Snapshot: snapshot, Scope: targets[0].Scope, APKPath: artifact.Path(root, f.SHA256), Config: cfg}, version: 247, notes: "旧版说明", updateAllowed: true, now: time.Now().UTC()}
	v.runtime = Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}}
	v.installSender(t)
	return v
}

func (v *fiveFixture) installSender(t *testing.T) {
	t.Helper()
	v.runtime.sender = func(lease *account.Lease, scope account.Scope) Sender {
		return m5Sender(func(ctx context.Context, q transport.Request) ([]byte, error) {
			if !lease.Valid(scope) {
				t.Fatal("lease missing")
			}
			u, _ := url.Parse(q.URL)
			kind := ""
			if q.Class == transport.Write {
				for _, candidate := range []string{"authorize", "upload", "bind", "metadata", "submit", "push"} {
					if q.OperationID == protocol.Digest([]string{v.input.Snapshot.Digest, scope.Channel, candidate}) {
						kind = candidate
					}
				}
				op, err := v.store.Operation(v.input.Snapshot.Manifest.ReleaseID+"/"+scope.Channel, kind)
				if err != nil || op == nil || op.Effect != "intent" {
					t.Fatal("business request preceded durable intent")
				}
				v.writes = append(v.writes, kind)
			}
			if q.Body != nil {
				b, err := io.ReadAll(q.Body)
				q.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				clear(b)
			}
			if kind != "" && kind == v.kill {
				fmt.Println("FIVE_SYNTHETIC_REMOTE_APPLIED")
				select {}
			}
			if kind != "" && kind == v.drop {
				return nil, &transport.Error{Code: "network_error", Effect: "unknown"}
			}
			if kind == "bind" && scope.Channel == "huawei" {
				v.version = 248
			}
			if kind == "metadata" {
				v.notes = v.input.Snapshot.Notes
			}
			switch u.Path {
			case "/auth/token":
				return []byte(`{"access_token":"synthetic-token","token_type":"Bearer","expires_in":3600}`), nil
			case "/api/publish/v2/appid-list":
				return []byte(`{"ret":{"code":0},"appids":[{"value":"123"}]}`), nil
			case "/api/publish/v2/app-info":
				return raw(map[string]any{"ret": map[string]any{"code": 0}, "appInfo": map[string]any{"versionCode": v.version}, "languages": []any{map[string]string{"lang": "zh-CN", "appName": "合成应用", "appDesc": "SYNTHETIC-PRIVATE", "briefInfo": "简介", "newFeatures": v.notes}}}), nil
			case "/openapi/v1/publish/get-app-id":
				return []byte(`{"code":0,"data":[{"appId":123,"packageName":"com.example.app"}]}`), nil
			case "/openapi/v1/publish/get-app-detail":
				return raw(map[string]any{"code": 0, "data": map[string]any{"basicInfo": map[string]any{"appId": 123, "packageName": "com.example.app"}, "releaseInfo": map[string]any{"versionCode": v.version}, "languageInfo": []any{map[string]string{"languageId": "zh_CN", "appName": "合成应用", "intro": "SYNTHETIC-PRIVATE", "briefIntro": "简介", "newFeature": v.notes}}}}), nil
			case "/devupload/dev/query":
				return raw(map[string]any{"result": 0, "updateVersion": v.updateAllowed, "packageInfo": map[string]any{"packageName": "com.example.app", "versionCode": v.version, "appName": "SYNTHETIC-PRIVATE"}}), nil
			case "/open_file/developer_api/query_app_detail":
				return raw(map[string]any{"ret": 0, "pkg_name": "com.example.app", "version_code": v.version, "app_name": "SYNTHETIC-PRIVATE"}), nil
			case "/api/publish/v2/upload-url/for-obs":
				f, _ := NewNamedFile(v.input.APKPath, "huawei.apk")
				return raw(map[string]any{"ret": map[string]any{"code": 0}, "urlInfo": map[string]any{"url": "https://synthetic.dbankcdn.com/object.apk", "objectId": "object.apk", "method": "PUT", "headers": map[string]string{"Authorization": "AWS4-HMAC-SHA256 synthetic", "X-Amz-Content-Sha256": f.SHA256(), "X-Amz-Date": v.now.Format("20060102T150405Z"), "Host": "synthetic.dbankcdn.com", "User-Agent": "synthetic", "Content-Type": "application/octet-stream"}}}), nil
			case "/openapi/v1/publish/get-file-upload-url":
				return raw(map[string]any{"code": 0, "data": []any{map[string]any{"fileName": "honor.apk", "objectId": json.Number("9223372036854775807"), "expireTime": time.Now().Add(time.Hour).Unix(), "uploadUrl": "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=9223372036854775807"}}}), nil
			case "/open_file/developer_api/get_file_upload_info":
				return raw(map[string]any{"ret": 0, "serial_number": "synthetic-file", "pre_sign_url": "https://synthetic.cos.ap-guangzhou.myqcloud.com/object.apk?" + url.Values{"q-sign-time": {decimal(time.Now().Unix()) + ";" + decimal(time.Now().Add(time.Hour).Unix())}}.Encode()}), nil
			case "/router/rest":
				if kind == "upload" {
					f, _ := NewFile(v.input.APKPath)
					return raw(map[string]any{"code": 0, "subCode": "0", "data": map[string]any{"serialnumber": "synthetic-file", "packageName": "com.example.app", "versionName": "3.2.7", "versionCode": "248", "fileMd5": f.MD5()}}), nil
				}
				if kind == "submit" {
					return []byte(`{"code":0,"subCode":"0"}`), nil
				}
				return raw(map[string]any{"code": 0, "subCode": "0", "data": map[string]any{"packageName": "com.example.app", "versionCode": decimal(v.version), "mainTitle": "合成应用", "detailDesc": strings.Repeat("简介", 30), "copyrightList": "https://example.invalid/SYNTHETIC-PRIVATE", "compatibleDevice": 1}}), nil
			case "/openapi/v1/publish/submit-audit":
				return []byte(`{"code":0,"data":"synthetic-release"}`), nil
			}
			if scope.Channel == "huawei" {
				if kind == "upload" {
					return nil, nil
				}
				return []byte(`{"ret":{"code":0}}`), nil
			}
			if scope.Channel == "honor" {
				return []byte(`{"code":0}`), nil
			}
			if scope.Channel == "xiaomi" {
				return []byte(`{"result":0}`), nil
			}
			if scope.Channel == "tencent" {
				return []byte(`{"ret":0}`), nil
			}
			t.Fatalf("unexpected synthetic request %s", u.Path)
			return nil, nil
		})
	}
}

type fiveClockExecutor struct {
	Runtime
	now func() time.Time
}

func (c fiveClockExecutor) WithClient(ctx context.Context, t Target, use func(*Client, Materials) error) error {
	return c.Runtime.WithClient(ctx, t, func(client *Client, m Materials) error { client.Now = c.now; return use(client, m) })
}

func (v *fiveFixture) prepared(t *testing.T, ref string, write bool) *PreparedProduction {
	t.Helper()
	p, err := v.runtime.NewFiveProduction(context.Background(), v.input, ref, write)
	if err != nil {
		t.Fatal(err)
	}
	p.base.runtime = fiveClockExecutor{Runtime: p.runtime, now: func() time.Time {
		now := time.Now().UTC()
		if v.now.After(now) {
			return v.now
		}
		return now
	}}
	return p
}
func (v *fiveFixture) engine(p *PreparedProduction) *engine.Engine {
	return &engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p, Hook: func(point string, q protocol.Request) error {
		if q.Channel == "huawei" && q.Kind == "submit" && point == "before_intent" {
			v.now = v.now.Add(3 * time.Minute)
		}
		return nil
	}}
}

func TestFiveProductionAcceptanceAndCompletedResume(t *testing.T) {
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			v := newFiveFixture(t, channel)
			p := v.prepared(t, "", false)
			e := v.engine(p)
			before, err := e.Preflight(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil)
			if err != nil || len(v.writes) != 0 {
				t.Fatalf("preflight: %v", err)
			}
			if _, err = e.Submit(context.Background(), before.ReleaseID, nil); err == nil {
				t.Fatal("read-only adapter wrote")
			}
			p = v.prepared(t, p.BaselineRef(), true)
			e = v.engine(p)
			after, err := e.Submit(context.Background(), before.ReleaseID, nil)
			if err != nil || !after.AllAccepted || after.AllApproved || after.AllLive {
				t.Fatalf("submit: %v errors=%v writes=%v", err, after.Errors, v.writes)
			}
			want, _ := p.base.Capabilities(p.target.Scope)
			if strings.Join(v.writes, ",") != strings.Join(want.Steps, ",") {
				t.Fatal("wrong request sequence")
			}
			count := len(v.writes)
			p = v.prepared(t, p.BaselineRef(), true)
			e = v.engine(p)
			if _, err = e.Submit(context.Background(), before.ReleaseID, nil); err != nil || len(v.writes) != count {
				t.Fatal("completed resume repeated writes")
			}
			b, _ := json.Marshal(after)
			if strings.Contains(string(b), "SYNTHETIC-PRIVATE") || strings.Contains(string(b), "https://") {
				t.Fatal("private materials leaked")
			}
		})
	}
}

func TestFiveProductionLostResponsesNeverReplay(t *testing.T) {
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			v := newFiveFixture(t, channel)
			p := v.prepared(t, "", false)
			e := v.engine(p)
			id := v.input.Snapshot.Manifest.ReleaseID
			if _, err := e.Preflight(context.Background(), id, nil); err != nil {
				t.Fatal(err)
			}
			p = v.prepared(t, p.BaselineRef(), true)
			e = v.engine(p)
			steps, _ := p.base.Capabilities(p.target.Scope)
			v.drop = steps.Steps[len(steps.Steps)-1]
			if _, err := e.Submit(context.Background(), id, nil); err == nil {
				t.Fatal("lost response accepted")
			}
			n := len(v.writes)
			v.drop = ""
			if _, err := e.Submit(context.Background(), id, nil); err == nil || len(v.writes) != n {
				t.Fatal("unknown request replayed")
			}
		})
	}
}

func TestFiveProductionXiaomiUpdatePermissionDriftStopsBeforePush(t *testing.T) {
	v := newFiveFixture(t, "xiaomi")
	p := v.prepared(t, "", false)
	e := v.engine(p)
	id := v.input.Snapshot.Manifest.ReleaseID
	if _, err := e.Preflight(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	v.updateAllowed = false
	p = v.prepared(t, p.BaselineRef(), true)
	e = v.engine(p)
	if _, err := e.Submit(context.Background(), id, nil); err == nil || len(v.writes) != 0 {
		t.Fatal("permission drift wrote")
	}
}

type fiveCrashFixture struct {
	Input                               PreparationInput
	BaselineRef, AccountRoot, FirstStep string
	Secrets                             []struct {
		Ref   credentials.Ref
		Value []byte
	}
}

func TestFiveProductionCrashHelper(t *testing.T) {
	root := os.Getenv("PUBLISHER_FIVE_KILL_FIXTURE")
	if root == "" {
		t.Skip("subprocess only")
	}
	b, err := os.ReadFile(filepath.Join(root, "synthetic-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture fiveCrashFixture
	if json.Unmarshal(b, &fixture) != nil {
		t.Fatal("invalid synthetic fixture")
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	vault := inspectionVault{}
	for _, secret := range fixture.Secrets {
		vault[secret.Ref] = secret.Value
	}
	v := &fiveFixture{store: s, input: fixture.Input, version: 247, notes: "旧版说明", updateAllowed: true, kill: fixture.FirstStep, now: time.Now().UTC(), runtime: Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: fixture.AccountRoot}}}}
	v.installSender(t)
	p := v.prepared(t, fixture.BaselineRef, true)
	if os.Getenv("PUBLISHER_HUAWEI_RENEW_FIXTURE") == "1" {
		err = v.engine(p).RenewExpiredHuaweiAuthorization(context.Background(), v.input.Snapshot.Manifest.ReleaseID, protocol.Digest([]string{v.input.Snapshot.Digest, "huawei", "authorize"}))
	} else {
		_, err = v.engine(p).Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil)
	}
	t.Fatalf("helper not killed: %v", err)
}

func TestFiveProductionRealProcessKillBlocksUnknownRequest(t *testing.T) {
	for _, channel := range []string{"huawei", "honor", "xiaomi", "vivo", "tencent"} {
		t.Run(channel, func(t *testing.T) {
			v := newFiveFixture(t, channel)
			p := v.prepared(t, "", false)
			e := v.engine(p)
			id := v.input.Snapshot.Manifest.ReleaseID
			if _, err := e.Preflight(context.Background(), id, nil); err != nil {
				t.Fatal(err)
			}
			caps, _ := p.base.Capabilities(p.target.Scope)
			fixture := fiveCrashFixture{Input: v.input, BaselineRef: p.BaselineRef(), AccountRoot: v.runtime.Manager.Accounts.Root, FirstStep: caps.Steps[0]}
			for ref, value := range v.runtime.Manager.Vault.(inspectionVault) {
				fixture.Secrets = append(fixture.Secrets, struct {
					Ref   credentials.Ref
					Value []byte
				}{ref, value})
			}
			b, _ := json.Marshal(fixture)
			root := v.store.Root
			if err := privatefs.Write(filepath.Join(root, "synthetic-fixture.json"), b); err != nil {
				t.Fatal(err)
			}
			v.store.Close()
			executable, _ := os.Executable()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestFiveProductionCrashHelper$")
			cmd.Env = append(os.Environ(), "PUBLISHER_FIVE_KILL_FIXTURE="+root)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = io.Discard
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "FIVE_SYNTHETIC_REMOTE_APPLIED" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					cmd.Wait()
					t.Fatal("helper did not apply request")
				}
			case <-ctx.Done():
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatal("helper timeout")
			}
			cmd.Process.Kill()
			if cmd.Wait() == nil {
				t.Fatal("helper not killed")
			}
			v.store, err = state.Open(root, manifest.Production, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { v.store.Close() })
			p = v.prepared(t, fixture.BaselineRef, true)
			e = v.engine(p)
			result, err := e.Submit(context.Background(), id, nil)
			if err == nil || result.AllAccepted || len(v.writes) != 0 || len(result.Operations) != 1 || result.Operations[0].Effect != "unknown" && result.Operations[0].Effect != "intent" {
				t.Fatalf("killed request was replayed or accepted: %v %+v writes=%v", err, result.Operations, v.writes)
			}
		})
	}
}
