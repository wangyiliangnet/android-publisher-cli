package channels

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

type oppoProductionFixture struct {
	store             *state.Store
	input             OPPOProductionInput
	runtime           Runtime
	details           Details
	wantCopyright     string
	writes            []string
	drop              bool
	authorizeFailures int
	uploadHost        string
	fileURLScheme     string
	uploadCorruption  string
}

func productionFixture(t *testing.T) *oppoProductionFixture {
	t.Helper()
	return productionFixtureRoot(t, t.TempDir())
}
func productionFixtureRoot(t *testing.T, root string) *oppoProductionFixture {
	t.Helper()
	file, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "synthetic.apk"), artifact.Info{PackageName: "com.example.synthetic", VersionCode: 43, VersionName: "4.3", ABI: []string{"arm64-v8a"}})
	if err != nil {
		t.Fatal(err)
	}
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "oppo-production-test", Mode: manifest.Production, PackageName: "com.example.synthetic", VersionCode: 43, VersionName: "4.3", Artifacts: map[string]manifest.Artifact{"oppo": file}, NotesFile: "notes", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{"oppo": {AccountRef: "synthetic-id", AppRef: "9007199254740993", Artifact: "oppo"}}}
	snapshot := manifest.Snapshot{Manifest: m, Notes: "合成更新说明，保留换行\n- 修复测试问题"}
	snapshot.Digest = snapshot.CalculateDigest()
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Save(snapshot); err != nil {
		t.Fatal(err)
	}
	targets, _ := s.Targets(m.ReleaseID)
	v := &oppoProductionFixture{store: s, details: m5OPPOUpdate(File{}).Existing, uploadHost: "upload.heytapmobi.com", fileURLScheme: "https"}
	v.wantCopyright = v.details.Metadata["copyright_url"]
	v.details.Metadata["test_desc"] = "SYNTHETIC-PRIVATE-TEST-ACCOUNT"
	v.details.Metadata["icon_url"] = "https://example.invalid/icon?token=SYNTHETIC-SIGNED-URL"
	v.details.Metadata["business_qq"] = ""
	v.details.Metadata["landscape_pic_url"] = ""
	v.input = OPPOProductionInput{Snapshot: snapshot, Scope: targets[0].Scope, APKPath: artifact.Path(root, file.SHA256), CredentialName: "client-secret"}
	vault := inspectionVault{credentials.Ref{Scope: account.Scope{Channel: "oppo", Account: "synthetic-id"}, Name: "client-secret"}: []byte("synthetic-secret")}
	v.runtime = Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: t.TempDir()}}}
	v.runtime.sender = func(lease *account.Lease, scope account.Scope) Sender {
		return m5Sender(func(ctx context.Context, q transport.Request) ([]byte, error) {
			if !lease.Valid(scope) {
				t.Fatal("account lease missing")
			}
			u, _ := url.Parse(q.URL)
			if q.Class == transport.Write {
				kind := map[string]string{"/resource/v1/upload/get-upload-url": "authorize", "/upload": "upload", "/resource/v1/app/upd": "submit"}[u.Path]
				op, err := v.store.Operation(m.ReleaseID+"/oppo", kind)
				if err != nil || op == nil || op.Effect != "intent" || op.Request.OperationID != q.OperationID {
					t.Fatal("business HTTP preceded durable intent")
				}
				v.writes = append(v.writes, kind)
			}
			switch u.Path {
			case "/developer/v1/token":
				return raw(map[string]any{"errno": 0, "data": map[string]any{"access_token": "synthetic-token", "expire_in": time.Now().Add(time.Hour).Unix()}}), nil
			case "/resource/v1/app/info":
				fields := map[string]any{"pkg_name": v.details.PackageName, "app_id": v.details.AppID, "version_code": v.details.VersionCode}
				for k, value := range v.details.Metadata {
					fields[k] = value
				}
				return raw(map[string]any{"errno": 0, "data": fields}), nil
			case "/resource/v1/upload/get-upload-url":
				if v.authorizeFailures > 0 {
					v.authorizeFailures--
					return []byte("SYNTHETIC-PRIVATE-GATEWAY-TEXT"), &transport.Error{Code: "http_status", Status: 504, Effect: "unknown"}
				}
				return raw(map[string]any{"errno": 0, "data": map[string]any{"upload_url": "https://" + v.uploadHost + "/upload", "sign": "SYNTHETIC-ONETIME-SIGN"}}), nil
			case "/upload":
				m5Body(t, q)
				f, err := NewNamedFile(v.input.APKPath, "oppo.apk")
				if err != nil {
					t.Fatal(err)
				}
				data := map[string]any{"url": v.fileURLScheme + "://assets.heytapmobi.com/app.apk?signature=SYNTHETIC-UPLOADED-URL", "id": "file-id", "md5": f.MD5(), "file_size": f.Size(), "file_extension": "apk"}
				switch v.uploadCorruption {
				case "md5":
					data["md5"] = strings.Repeat("0", 32)
				case "size":
					data["file_size"] = f.Size() + 1
				case "extension":
					data["file_extension"] = "exe"
				case "id":
					data["id"] = ""
				case "url":
					data["url"] = "javascript:secret"
				}
				return raw(map[string]any{"errno": 0, "data": data}), nil
			case "/resource/v1/app/upd":
				fields := m5Fields(t, q)
				if fields["online_type"] != "1" || fields["update_desc"] != snapshot.Notes || fields["test_desc"] != v.details.Metadata["test_desc"] || fields["business_email"] != v.details.Metadata["business_email"] || fields["copyright_url"] != v.wantCopyright {
					t.Fatal("original details, policy or notes changed")
				}
				if v.drop {
					return nil, &transport.Error{Code: "synthetic_response_lost", Effect: "unknown"}
				}
				return []byte(`{"errno":0,"data":{"success":true}}`), nil
			default:
				t.Fatalf("unexpected production request: %s", u.Path)
				return nil, nil
			}
		})
	}
	return v
}

func TestOPPOProductionJournalAndSensitiveBaseline(t *testing.T) {
	v := productionFixture(t)
	ctx := context.Background()
	p, err := v.runtime.NewOPPOProduction(ctx, v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p}
	before, err := e.Preflight(ctx, v.input.Snapshot.Manifest.ReleaseID, nil)
	if err != nil || len(v.writes) != 0 {
		t.Fatalf("preflight wrote: %v", err)
	}
	if _, err = e.Submit(ctx, before.ReleaseID, nil); err == nil || len(v.writes) != 0 {
		t.Fatal("read-only adapter wrote")
	}
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, p.BaselineRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p
	after, err := e.Submit(ctx, before.ReleaseID, nil)
	if err != nil || !after.AllAccepted || after.AllApproved || after.AllLive || strings.Join(v.writes, ",") != "authorize,upload,submit" {
		t.Fatalf("incorrect production outcome: %v %#v %v", err, after, v.writes)
	}
	if _, err = e.Submit(ctx, before.ReleaseID, nil); err != nil || len(v.writes) != 3 {
		t.Fatal("repeated submission wrote again")
	}
	encoded, _ := json.Marshal([]engine.Summary{before, after})
	var preflight string
	if err = v.store.DB.QueryRow("SELECT preflight FROM targets WHERE id=?", before.ReleaseID+"/oppo").Scan(&preflight); err != nil {
		t.Fatal(err)
	}
	rows, err := v.store.DB.Query("SELECT request FROM operations")
	if err != nil {
		t.Fatal(err)
	}
	var journal strings.Builder
	for rows.Next() {
		var request string
		if err = rows.Scan(&request); err != nil {
			t.Fatal(err)
		}
		journal.WriteString(request)
	}
	rows.Close()
	for _, secret := range []string{"SYNTHETIC-PRIVATE", "SYNTHETIC-SIGNED", "SYNTHETIC-ONETIME", "SYNTHETIC-UPLOADED", "test@example.invalid", "https://"} {
		if bytes.Contains(encoded, []byte(secret)) || strings.Contains(preflight, secret) || strings.Contains(journal.String(), secret) {
			t.Fatal("sensitive baseline or grant escaped Keychain")
		}
	}
}

func TestOPPOProductionUnknownResponseCannotReplayAfterReopen(t *testing.T) {
	v := productionFixture(t)
	ctx := context.Background()
	p, err := v.runtime.NewOPPOProduction(ctx, v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p}
	if _, err = e.Preflight(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err != nil {
		t.Fatal(err)
	}
	ref := p.BaselineRef()
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter, v.drop = p, true
	if _, err = e.Submit(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err == nil {
		t.Fatal("lost response accepted")
	}
	root := v.store.Root
	v.store.Close()
	v.store, err = state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.store.Close() })
	e.Store = v.store
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, ref, true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p
	if _, err = e.Submit(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err == nil || len(v.writes) != 3 {
		t.Fatal("unresolved submit replayed")
	}
	if _, err = e.Reconcile(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err == nil || len(v.writes) != 3 {
		t.Fatal("reconciliation guessed or wrote")
	}
}

func TestOPPOProductionMissingMetadataAndChangedRemoteStopWrites(t *testing.T) {
	v := productionFixture(t)
	delete(v.details.Metadata, "copyright_url")
	_, err := v.runtime.NewOPPOProduction(context.Background(), v.input, "", false)
	var metadata *OPPOMetadataError
	if !errors.As(err, &metadata) || strings.Join(metadata.Missing, ",") != "copyright_url" || len(v.writes) != 0 {
		t.Fatalf("missing metadata not actionable: %v", err)
	}
	v.details.Metadata["electronic_cert_url"] = "invalid-proof?signature=DO-NOT-PRINT"
	_, err = v.runtime.NewOPPOProduction(context.Background(), v.input, "", false)
	if !errors.As(err, &metadata) || strings.Join(metadata.Available, ",") != "electronic_cert_url" || strings.Contains(err.Error(), "DO-NOT-PRINT") {
		t.Fatal("alternative material diagnostic lost field presence or leaked its value")
	}
	v.details.Metadata["copyright_url"] = "https://example.invalid/proof"
	p, err := v.runtime.NewOPPOProduction(context.Background(), v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	v.details.Metadata["summary"] = "后台已改动"
	if _, err = p.Inspect(context.Background(), v.input.Scope); err == nil || len(v.writes) != 0 {
		t.Fatal("changed baseline accepted")
	}
	v.details.VersionCode = 43
	if _, err = p.Inspect(context.Background(), v.input.Scope); err == nil {
		t.Fatal("equal remote version accepted")
	}
}

func TestOPPOProductionExistingElectronicCopyright(t *testing.T) {
	v := productionFixture(t)
	const proof = "https://fixture.invalid/electronic-proof.pdf?signature=SYNTHETIC-PRIVATE-PROOF"
	delete(v.details.Metadata, "copyright_url")
	v.details.Metadata["electronic_cert_url"] = proof
	v.wantCopyright = proof
	ctx := context.Background()
	p, err := v.runtime.NewOPPOProduction(ctx, v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p}
	before, err := e.Preflight(ctx, v.input.Snapshot.Manifest.ReleaseID, nil)
	if err != nil || len(v.writes) != 0 || p.CopyrightSource() != "electronic_cert_url" || p.target.Existing.Metadata["copyright_url"] != "" {
		t.Fatal("electronic proof was not preserved as an explicit read-only baseline")
	}
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, p.BaselineRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p
	after, err := e.Submit(ctx, before.ReleaseID, nil)
	if err != nil || !after.AllAccepted || len(v.writes) != 3 {
		t.Fatalf("electronic proof submission fixture failed: %v", err)
	}
	output, _ := json.Marshal([]engine.Summary{before, after})
	if bytes.Contains(output, []byte(proof)) || bytes.Contains(output, []byte("SYNTHETIC-PRIVATE-PROOF")) {
		t.Fatal("electronic proof escaped private materials")
	}
	v.details.Metadata["electronic_cert_url"] = "https://fixture.invalid/changed-proof.pdf"
	if _, err = p.Inspect(ctx, v.input.Scope); err == nil || len(v.writes) != 3 {
		t.Fatal("changed electronic proof was accepted or written")
	}
}

func TestOPPOProductionRequestBoundary(t *testing.T) {
	target := runtimeTarget(t, "oppo")
	target.Credential.ID = "synthetic-id"
	calls := 0
	sender := oppoProductionSender{target: target, underlying: m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte("ok"), nil })}
	origin := origins["oppo"]
	authorize := transport.Request{URL: origin + "/resource/v1/upload/get-upload-url?access_token=t&timestamp=1&api_sign=s", Method: "GET", Class: transport.Write, OperationID: protocol.Digest([]string{target.IntentDigest, "oppo", "authorize"})}
	if _, err := sender.Send(context.Background(), authorize); err == nil || calls != 0 {
		t.Fatal("preflight acquired a grant")
	}
	sender.writable = true
	if _, err := sender.Send(context.Background(), authorize); err != nil || calls != 1 {
		t.Fatal("bound authorization rejected")
	}
	for _, change := range []func(*transport.Request){
		func(q *transport.Request) { q.Class = transport.Read },
		func(q *transport.Request) { q.Class = transport.Lifecycle },
		func(q *transport.Request) { q.OperationID = "other-intent" },
		func(q *transport.Request) { q.URL += "&api_sign=duplicate" },
		func(q *transport.Request) { q.URL = strings.Replace(q.URL, "heytapmobi.com", "example.invalid", 1) },
		func(q *transport.Request) { q.Headers = http.Header{"Authorization": {"unexpected"}} },
	} {
		q := authorize
		change(&q)
		if _, err := sender.Send(context.Background(), q); err == nil || calls != 1 {
			t.Fatal("unbound authorization sent")
		}
	}
	base := url.Values{"pkg_name": {target.PackageName}, "version_code": {decimal(target.VersionCode)}, "update_desc": {target.Notes}, "online_type": {"1"}, "apk_url": {"[]"}, "access_token": {"t"}, "timestamp": {"1"}, "api_sign": {"s"}}
	for _, change := range []func(url.Values){
		func(q url.Values) { q.Set("pkg_name", "com.other.app") },
		func(q url.Values) { q.Set("version_code", "99") },
		func(q url.Values) { q.Set("online_type", "2") },
		func(q url.Values) { q.Set("sche_online_time", "future") },
		func(q url.Values) { q.Set("update_desc", "changed notes") },
		func(q url.Values) { q.Set("unexpected", "field") },
		func(q url.Values) { q.Add("api_sign", "duplicate") },
	} {
		values, _ := url.ParseQuery(base.Encode())
		change(values)
		b := values.Encode()
		q := transport.Request{URL: origin + "/resource/v1/app/upd", Method: "POST", Class: transport.Write, OperationID: protocol.Digest([]string{target.IntentDigest, "oppo", "submit"}), Headers: http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}, Body: io.NopCloser(strings.NewReader(b)), ContentLength: int64(len(b))}
		if _, err := sender.Send(context.Background(), q); err == nil || calls != 1 {
			t.Fatal("changed publishing scope or policy sent")
		}
	}
}

type oppoCrashFixture struct {
	Input       OPPOProductionInput
	BaselineRef string
	Details     Details
	AccountRoot string
	Secrets     []struct {
		Ref   credentials.Ref
		Value []byte
	}
}

func TestOPPOProductionCrashHelper(t *testing.T) {
	dir := os.Getenv("PUBLISHER_OPPO_KILL_FIXTURE")
	if dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "synthetic-fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture oppoCrashFixture
	if json.Unmarshal(b, &fixture) != nil {
		t.Fatal("invalid synthetic fixture")
	}
	s, err := state.Open(dir, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	vault := inspectionVault{}
	for _, item := range fixture.Secrets {
		vault[item.Ref] = item.Value
	}
	r := Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Coordinator{Root: fixture.AccountRoot}}}
	var calls []string
	r.sender = func(_ *account.Lease, _ account.Scope) Sender {
		return m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
			u, _ := url.Parse(q.URL)
			if q.Class == transport.Write {
				kind := map[string]string{"/resource/v1/upload/get-upload-url": "authorize", "/upload": "upload", "/resource/v1/app/upd": "submit"}[u.Path]
				op, err := s.Operation(fixture.Input.Snapshot.Manifest.ReleaseID+"/oppo", kind)
				if err != nil || op == nil || op.Effect != "intent" {
					t.Fatal("child request preceded durable intent")
				}
				calls = append(calls, kind)
				b, _ := json.Marshal(calls)
				if err = privatefs.Write(filepath.Join(dir, "synthetic-server-applied.json"), b); err != nil {
					t.Fatal(err)
				}
			}
			switch u.Path {
			case "/resource/v1/app/info":
				fields := map[string]any{"pkg_name": fixture.Details.PackageName, "app_id": fixture.Details.AppID, "version_code": fixture.Details.VersionCode}
				for k, value := range fixture.Details.Metadata {
					fields[k] = value
				}
				return raw(map[string]any{"errno": 0, "data": fields}), nil
			case "/resource/v1/upload/get-upload-url":
				return []byte(`{"errno":0,"data":{"upload_url":"https://upload.heytapmobi.com/upload","sign":"SYNTHETIC-ONETIME-SIGN"}}`), nil
			case "/upload":
				m5Body(t, q)
				file, err := NewNamedFile(fixture.Input.APKPath, "oppo.apk")
				if err != nil {
					t.Fatal(err)
				}
				return raw(map[string]any{"errno": 0, "data": map[string]any{"url": "https://assets.heytapmobi.com/synthetic.apk", "id": "synthetic-file", "md5": file.MD5(), "file_size": file.Size(), "file_extension": "apk"}}), nil
			case "/resource/v1/app/upd":
				m5Body(t, q)
				fmt.Println("OPPO_SYNTHETIC_REMOTE_APPLIED")
				select {} // Parent sends a real SIGKILL after the simulated store applies.
			default:
				t.Fatal("unexpected child request")
				return nil, errors.New("unexpected child request")
			}
		})
	}
	p, err := r.NewOPPOProduction(context.Background(), fixture.Input, fixture.BaselineRef, true)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: p}
	_, err = e.Submit(context.Background(), fixture.Input.Snapshot.Manifest.ReleaseID, nil)
	t.Fatalf("child was not killed: %v", err)
}

func TestOPPOProductionRealProcessKillKeepsSubmissionUnknown(t *testing.T) {
	v := productionFixture(t)
	ctx := context.Background()
	p, err := v.runtime.NewOPPOProduction(ctx, v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p}
	if _, err = e.Preflight(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err != nil {
		t.Fatal(err)
	}
	fixture := oppoCrashFixture{Input: v.input, BaselineRef: p.BaselineRef(), Details: v.details, AccountRoot: v.runtime.Manager.Accounts.Root}
	for ref, value := range v.runtime.Manager.Vault.(inspectionVault) {
		fixture.Secrets = append(fixture.Secrets, struct {
			Ref   credentials.Ref
			Value []byte
		}{ref, value})
	}
	b, _ := json.Marshal(fixture)
	root := v.store.Root
	if err = privatefs.Write(filepath.Join(root, "synthetic-fixture.json"), b); err != nil {
		t.Fatal(err)
	}
	v.store.Close()
	executable, _ := os.Executable()
	childCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, executable, "-test.run=^TestOPPOProductionCrashHelper$")
	cmd.Env = append(os.Environ(), "PUBLISHER_OPPO_KILL_FIXTURE="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "OPPO_SYNTHETIC_REMOTE_APPLIED" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case applied := <-ready:
		if !applied {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("child exited before remote application")
		}
	case <-childCtx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("child did not reach kill boundary")
	}
	cmd.Process.Kill()
	if err = cmd.Wait(); err == nil {
		t.Fatal("child was not terminated")
	}
	v.store, err = state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.store.Close() })
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, fixture.BaselineRef, true)
	if err != nil {
		t.Fatal(err)
	}
	e.Store, e.Adapter = v.store, p
	result, err := e.Submit(ctx, v.input.Snapshot.Manifest.ReleaseID, nil)
	if err == nil || result.AllAccepted || len(v.writes) != 0 {
		t.Fatal("killed submission was accepted or resent")
	}
	b, err = os.ReadFile(filepath.Join(root, "synthetic-server-applied.json"))
	if err != nil || string(b) != `["authorize","upload","submit"]` {
		t.Fatal("unexpected remote business count")
	}
}
