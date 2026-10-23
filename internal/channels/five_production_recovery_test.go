package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestFiveProductionSealedResponsesRecoverEveryStepWithoutReplay(t *testing.T) {
	for channel, steps := range map[string][]string{"huawei": {"authorize", "upload", "bind", "metadata", "submit"}, "honor": {"authorize", "upload", "bind", "metadata", "submit"}, "xiaomi": {"push"}, "vivo": {"upload", "submit"}, "tencent": {"authorize", "upload", "submit"}} {
		for _, kind := range steps {
			t.Run(channel+"/"+kind, func(t *testing.T) {
				v := newFiveFixture(t, channel)
				p := v.prepared(t, "", false)
				e := v.engine(p)
				ctx := context.Background()
				id := v.input.Snapshot.Manifest.ReleaseID
				if _, err := e.Preflight(ctx, id, nil); err != nil {
					t.Fatal(err)
				}
				opID := protocol.Digest([]string{v.input.Snapshot.Digest, channel, kind})
				original := v.runtime.Manager.Vault
				v.runtime.Manager.Vault = fileCheckpointFailVault{original, materialName("material-" + account.Key(fiveReceiptKey(opID)))}
				p = v.prepared(t, p.BaselineRef(), true)
				e = v.engine(p)
				result, err := e.Submit(ctx, id, nil)
				if err == nil || len(result.Operations) == 0 || result.Operations[len(result.Operations)-1].Effect != "unknown" {
					t.Fatalf("receipt failure not unknown: %v %+v", err, result.Operations)
				}
				n := len(v.writes)
				v.runtime.Manager.Vault = original
				// A new executor recovers only the exact sealed response. Credential
				// access and token lifecycle are not prerequisites for local recovery.
				ref := p.BaselineRef()
				scope := v.input.Config.target().Account
				secretRef := credentials.Ref{Scope: scope, Name: "credential"}
				secret, _ := original.Get(ctx, secretRef)
				original.Delete(ctx, secretRef)
				p = v.prepared(t, ref, false)
				e = v.engine(p)
				result, err = e.Reconcile(ctx, id, nil)
				if err != nil || len(v.writes) != n || result.Operations[len(result.Operations)-1].Effect != "confirmed_applied" {
					t.Fatalf("sealed recovery failed: %v %+v diag=%+v", err, result.Operations, p.LastWriteDiagnostic())
				}
				if secret != nil {
					original.Put(ctx, secretRef, secret, false)
					secret.Destroy()
				}
				p = v.prepared(t, ref, true)
				e = v.engine(p)
				result, err = e.Submit(ctx, id, nil)
				if err != nil || !result.AllAccepted {
					t.Fatalf("recovered chain failed: %v %+v", err, result.Errors)
				}
				if !reflect.DeepEqual(v.writes, steps) {
					t.Fatalf("recovery repeated requests: %v", v.writes)
				}
			})
		}
	}
}

func TestFiveProductionSealedMaterialWindowsRecoverWithoutReallocation(t *testing.T) {
	for channel, kinds := range map[string][]string{"huawei": {"authorize", "bind"}, "honor": {"authorize"}, "vivo": {"upload"}, "tencent": {"authorize"}} {
		for _, kind := range kinds {
			t.Run(channel+"/"+kind, func(t *testing.T) {
				v := newFiveFixture(t, channel)
				p := v.prepared(t, "", false)
				e := v.engine(p)
				ctx := context.Background()
				id := v.input.Snapshot.Manifest.ReleaseID
				if _, err := e.Preflight(ctx, id, nil); err != nil {
					t.Fatal(err)
				}
				opID := protocol.Digest([]string{v.input.Snapshot.Digest, channel, kind})
				original := v.runtime.Manager.Vault
				v.runtime.Manager.Vault = fileCheckpointFailVault{original, materialName("material-" + account.Key(opID))}
				p = v.prepared(t, p.BaselineRef(), true)
				e = v.engine(p)
				if _, err := e.Submit(ctx, id, nil); err == nil {
					t.Fatal("material save failure accepted")
				}
				n := len(v.writes)
				v.runtime.Manager.Vault = original
				p = v.prepared(t, p.BaselineRef(), false)
				e = v.engine(p)
				if _, err := e.Reconcile(ctx, id, nil); err != nil || len(v.writes) != n {
					t.Fatalf("missing cache recovery: %v", err)
				}
			})
		}
	}
}

func TestFiveProductionInvalidSealedEvidenceCannotSettle(t *testing.T) {
	mutations := map[string]func(*fiveResponseEnvelope){"missing_code": func(e *fiveResponseEnvelope) { e.Body = []byte(`{}`) }, "string_code": func(e *fiveResponseEnvelope) { e.Body = []byte(`{"result":"0"}`) }, "duplicate_code": func(e *fiveResponseEnvelope) { e.Body = []byte(`{"result":0,"result":1}`) }, "incomplete": func(e *fiveResponseEnvelope) { e.Complete = false }, "http_error": func(e *fiveResponseEnvelope) { e.HTTPStatus = 503 }, "foreign_request": func(e *fiveResponseEnvelope) { e.Request.VersionCode++ }, "foreign_scope": func(e *fiveResponseEnvelope) { e.Scope = "foreign" }, "missing_time": func(e *fiveResponseEnvelope) { e.StartedAt = time.Time{} }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			v := newFiveFixture(t, "xiaomi")
			p := v.prepared(t, "", false)
			e := v.engine(p)
			ctx := context.Background()
			id := v.input.Snapshot.Manifest.ReleaseID
			if _, err := e.Preflight(ctx, id, nil); err != nil {
				t.Fatal(err)
			}
			opID := protocol.Digest([]string{v.input.Snapshot.Digest, "xiaomi", "push"})
			original := v.runtime.Manager.Vault
			v.runtime.Manager.Vault = fileCheckpointFailVault{original, materialName("material-" + account.Key(fiveReceiptKey(opID)))}
			p = v.prepared(t, p.BaselineRef(), true)
			e = v.engine(p)
			if _, err := e.Submit(ctx, id, nil); err == nil {
				t.Fatal("receipt fault accepted")
			}
			v.runtime.Manager.Vault = original
			scope := v.input.Config.target().Account
			err := v.runtime.Manager.WithAccount(ctx, scope, func(session *credentials.Session) error {
				b, err := session.Lease().Read("five-response-" + opID)
				if err != nil {
					return err
				}
				var diagnostic FiveWriteDiagnostic
				if json.Unmarshal(b, &diagnostic) != nil {
					t.Fatal("bad synthetic diagnostic")
				}
				ref := credentials.Ref{Scope: scope, Name: materialName(diagnostic.ResponseRef)}
				secret, err := original.Get(ctx, ref)
				if err != nil {
					return err
				}
				defer secret.Destroy()
				var envelope fiveResponseEnvelope
				if json.Unmarshal(secret.Bytes(), &envelope) != nil {
					t.Fatal("bad synthetic response")
				}
				mutate(&envelope)
				value, _ := credentials.NewSecret(raw(envelope))
				defer value.Destroy()
				return original.Put(ctx, ref, value, true)
			})
			if err != nil {
				t.Fatal(err)
			}
			p = v.prepared(t, p.BaselineRef(), false)
			e = v.engine(p)
			result, err := e.Reconcile(ctx, id, nil)
			if err == nil || result.AllAccepted || len(v.writes) != 1 {
				t.Fatal("invalid evidence settled or repeated operation")
			}
		})
	}
}

func TestFiveProductionMaterialComparisonKeepsLargeIntegersExact(t *testing.T) {
	m := &MemoryGrants{}
	ctx := context.Background()
	key := "synthetic-large-identity"
	if _, err := recoverFiveMaterial(ctx, m, key, struct{ ID int64 }{9007199254740992}); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverFiveMaterial(ctx, m, key, struct{ ID int64 }{9007199254740993}); err == nil {
		t.Fatal("different int64 identities compared equal")
	}
}

func TestFiveProductionHuaweiPreservesLaterSavedProcessingDeadline(t *testing.T) {
	m := &MemoryGrants{}
	ctx := context.Background()
	now := time.Now().UTC()
	g := UploadGrant{URL: "https://synthetic.dbankcdn.com/object", Ref: "object", Method: "PUT", ExpiresAt: now.Add(4 * time.Minute), ReadyAt: now.Add(2 * time.Minute), Headers: http.Header{"Authorization": {"SYNTHETIC"}}}
	saved := g
	saved.ReadyAt = saved.ReadyAt.Add(15 * time.Second)
	if _, err := saveGrant(ctx, m, "synthetic-bind", saved); err != nil {
		t.Fatal(err)
	}
	ref, err := recoverFiveGrant(ctx, m, "synthetic-bind", g, now, true)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := loadGrant(ctx, m, ref)
	if err != nil || !recovered.ReadyAt.Equal(saved.ReadyAt) {
		t.Fatal("later confirmed deadline was shortened or rejected")
	}
}

func TestFiveProductionTencentSenderRejectsExtraMetadataAndDuplicateValues(t *testing.T) {
	v := newFiveFixture(t, "tencent")
	p := v.prepared(t, "", false)
	target := p.target
	count := 0
	sender := fiveProductionSender{target: target, writable: true, underlying: m5Sender(func(context.Context, transport.Request) ([]byte, error) { count++; return []byte(`{"ret":0}`), nil })}
	file, _ := NewFile(v.input.APKPath)
	base := url.Values{"pkg_name": {target.PackageName}, "app_id": {target.AppID}, "user_id": {target.Credential.ID}, "timestamp": {"1"}, "sign": {"synthetic"}, "feature": {target.Notes}, "deploy_type": {"1"}, "apk32_flag": {"2"}, "apk64_flag": {"1"}, "apk64_file_md5": {file.MD5()}, "apk64_file_serial_number": {"synthetic"}}
	for _, mutation := range []func(url.Values){func(f url.Values) { f.Del("apk32_flag"); f.Set("app_name", "unauthorized title") }, func(f url.Values) { f.Add("timestamp", "2") }, func(f url.Values) { f.Set("apk32_flag", "1") }} {
		f := url.Values{}
		for k, values := range base {
			f[k] = append([]string{}, values...)
		}
		mutation(f)
		b := []byte(f.Encode())
		_, err := sender.Send(context.Background(), transport.Request{URL: origins["tencent"] + "/open_file/developer_api/update_app", Method: "POST", Class: transport.Write, OperationID: protocol.Digest([]string{target.IntentDigest, "tencent", "submit"}), Headers: http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}, Body: io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b))})
		if err == nil || count != 0 {
			t.Fatal("unauthorized form reached transport")
		}
	}
}
