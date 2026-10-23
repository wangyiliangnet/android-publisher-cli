package channels

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func expiredHuaweiFixture(t *testing.T, notSentUpload bool) (*fiveFixture, *PreparedProduction, *engine.Engine) {
	t.Helper()
	ctx := context.Background()
	v := newFiveFixture(t, "huawei")
	p := v.prepared(t, "", false)
	e := v.engine(p)
	id := v.input.Snapshot.Manifest.ReleaseID
	if _, err := e.Preflight(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	p = v.prepared(t, p.BaselineRef(), true)
	e = v.engine(p)
	e.Hook = func(point string, q protocol.Request) error {
		if point == "after_receipt" && q.Kind == "authorize" {
			return errors.New("synthetic checkpoint pause")
		}
		return nil
	}
	if _, err := e.Submit(ctx, id, nil); err == nil {
		t.Fatal("checkpoint pause did not stop")
	}
	op, err := v.store.Operation(id+"/huawei", "authorize")
	if err != nil || op == nil {
		t.Fatal("missing allocation")
	}
	scope := v.input.Config.target().Account
	err = v.runtime.Manager.WithAccount(ctx, scope, func(session *credentials.Session) error {
		m := keychainMaterials{vault: v.runtime.Manager.Vault, scope: scope, lease: session.Lease()}
		g, err := loadGrant(ctx, m, op.RemoteRef)
		if err != nil {
			return err
		}
		g.ExpiresAt = time.Now().UTC().Add(-time.Second)
		secret, _ := credentials.NewSecret(raw(grantPayload{URL: g.URL, Ref: g.Ref, Method: g.Method, ExpiresAt: g.ExpiresAt, ReadyAt: g.ReadyAt, Headers: g.Headers}))
		defer secret.Destroy()
		return v.runtime.Manager.Vault.Put(ctx, credentials.Ref{Scope: scope, Name: materialName(op.RemoteRef)}, secret, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	e = v.engine(p)
	if notSentUpload {
		result, err := e.Submit(ctx, id, nil)
		if err == nil || len(v.writes) != 1 || len(result.Operations) != 2 || result.Operations[1].Effect != "confirmed_not_applied" {
			t.Fatal("expired grant reached upload HTTP")
		}
	}
	return v, p, e
}

func TestHuaweiExpiredUnusedRenewalArchivesAndCompletes(t *testing.T) {
	for _, upload := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocated_only", true: "known_not_sent_upload"}[upload], func(t *testing.T) {
			v, p, e := expiredHuaweiFixture(t, upload)
			ctx := context.Background()
			id := v.input.Snapshot.Manifest.ReleaseID
			old, _ := v.store.Operation(id+"/huawei", "authorize")
			oldVault := v.runtime.Manager.Vault.(inspectionVault)
			oldMaterial := append([]byte{}, oldVault[credentials.Ref{Scope: p.target.Account, Name: materialName(old.RemoteRef)}]...)
			if err := e.RenewExpiredHuaweiAuthorization(ctx, id, old.Request.OperationID); err != nil {
				t.Fatal(err)
			}
			current, _ := v.store.Operation(id+"/huawei", "authorize")
			if current.Attempt != 2 || current.Request.AuthorizationGeneration != 1 || current.Effect != "confirmed_applied" || current.RemoteRef == old.RemoteRef {
				t.Fatal("new generation did not retain distinct allocation")
			}
			if !reflect.DeepEqual(oldMaterial, oldVault[credentials.Ref{Scope: p.target.Account, Name: materialName(old.RemoteRef)}]) {
				t.Fatal("old Keychain grant overwritten")
			}
			if op, _ := v.store.Operation(id+"/huawei", "upload"); op != nil {
				t.Fatal("known not-sent upload was not archived")
			}
			p = v.prepared(t, p.BaselineRef(), true)
			e = v.engine(p)
			result, err := e.Submit(ctx, id, nil)
			if err != nil || !result.AllAccepted {
				t.Fatalf("renewed submit failed: %v %+v", err, result.Errors)
			}
			if !reflect.DeepEqual(v.writes, []string{"authorize", "authorize", "upload", "bind", "metadata", "submit"}) {
				t.Fatalf("unexpected requests %v", v.writes)
			}
			history, err := v.store.OPPOAuthorizationHistory(id)
			if err != nil || len(history) != 1 || history[0].Effect != "confirmed_applied" {
				t.Fatal("allocation history lost truthful result")
			}
		})
	}
}

func TestHuaweiRenewalOldGenerationEvidenceCannotSettleNextIntent(t *testing.T) {
	v, p, e := expiredHuaweiFixture(t, true)
	ctx := context.Background()
	id := v.input.Snapshot.Manifest.ReleaseID
	old, _ := v.store.Operation(id+"/huawei", "authorize")
	e.Hook = func(point string, q protocol.Request) error {
		if point == "after_intent" {
			return errors.New("synthetic crash before renewal HTTP")
		}
		return nil
	}
	if err := e.RenewExpiredHuaweiAuthorization(ctx, id, old.Request.OperationID); err == nil {
		t.Fatal("renewal boundary did not stop")
	}
	current, _ := v.store.Operation(id+"/huawei", "authorize")
	if current.Request.AuthorizationGeneration != 1 || current.Effect != "intent" {
		t.Fatal("next generation intent not durable")
	}
	p = v.prepared(t, p.BaselineRef(), false)
	n := len(v.writes)
	r, err := p.Reconcile(ctx, current.Request)
	if err != nil || r.Result != "inconclusive" || len(v.writes) != n {
		t.Fatal("old proof settled or replayed new generation")
	}
	if err = v.engine(v.prepared(t, p.BaselineRef(), true)).RenewExpiredHuaweiAuthorization(ctx, id, current.Request.OperationID); err == nil {
		t.Fatal("unknown renewal allowed another allocation")
	}
}

func TestHuaweiRenewalSealedNewResponseRecoversWithoutReallocation(t *testing.T) {
	v, p, e := expiredHuaweiFixture(t, true)
	ctx := context.Background()
	id := v.input.Snapshot.Manifest.ReleaseID
	old, _ := v.store.Operation(id+"/huawei", "authorize")
	renewal := old.Request
	renewal.AuthorizationGeneration = 1
	vault := v.runtime.Manager.Vault
	v.runtime.Manager.Vault = fileCheckpointFailVault{vault, materialName("material-" + account.Key(fiveReceiptKey(renewal.MaterialOperationKey())))}
	if err := e.RenewExpiredHuaweiAuthorization(ctx, id, old.Request.OperationID); err == nil {
		t.Fatal("new receipt save fault accepted")
	}
	n := len(v.writes)
	v.runtime.Manager.Vault = vault
	p = v.prepared(t, p.BaselineRef(), false)
	result, err := v.engine(p).Reconcile(ctx, id, nil)
	if err != nil || len(v.writes) != n || result.Operations[0].Effect != "confirmed_applied" {
		t.Fatalf("new response failed local recovery: %v", err)
	}
	op, _ := v.store.Operation(id+"/huawei", "authorize")
	if op.Request.AuthorizationGeneration != 1 || op.RemoteRef == old.RemoteRef {
		t.Fatal("old grant reused during recovery")
	}
}

func TestHuaweiRenewalUnknownUploadAndUnexpiredGrantAreRejected(t *testing.T) {
	for _, cause := range []string{"unknown_upload", "unexpired"} {
		t.Run(cause, func(t *testing.T) {
			v, p, e := expiredHuaweiFixture(t, true)
			ctx := context.Background()
			id := v.input.Snapshot.Manifest.ReleaseID
			op, _ := v.store.Operation(id+"/huawei", "authorize")
			if cause == "unknown_upload" {
				v.store.DB.Exec("UPDATE operations SET effect='unknown' WHERE target_id=? AND kind='upload'", id+"/huawei")
			} else {
				scope := p.target.Account
				v.runtime.Manager.WithAccount(ctx, scope, func(session *credentials.Session) error {
					m := keychainMaterials{vault: v.runtime.Manager.Vault, scope: scope, lease: session.Lease()}
					g, _ := loadGrant(ctx, m, op.RemoteRef)
					g.ExpiresAt = time.Now().UTC().Add(time.Hour)
					value, _ := credentials.NewSecret(raw(grantPayload{URL: g.URL, Ref: g.Ref, Method: g.Method, ExpiresAt: g.ExpiresAt, ReadyAt: g.ReadyAt, Headers: g.Headers}))
					defer value.Destroy()
					return v.runtime.Manager.Vault.Put(ctx, credentials.Ref{Scope: scope, Name: materialName(op.RemoteRef)}, value, true)
				})
			}
			n := len(v.writes)
			if err := e.RenewExpiredHuaweiAuthorization(ctx, id, op.Request.OperationID); err == nil || len(v.writes) != n {
				t.Fatal("ineligible renewal allocated")
			}
		})
	}
}

func TestHuaweiRenewalRealProcessKillCannotUseOldAcknowledgement(t *testing.T) {
	v, p, _ := expiredHuaweiFixture(t, true)
	ctx := context.Background()
	fixture := fiveCrashFixture{Input: v.input, BaselineRef: p.BaselineRef(), AccountRoot: v.runtime.Manager.Accounts.Root, FirstStep: "authorize"}
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
	exe, _ := os.Executable()
	childCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childCtx, exe, "-test.run=^TestFiveProductionCrashHelper$")
	cmd.Env = append(os.Environ(), "PUBLISHER_FIVE_KILL_FIXTURE="+root, "PUBLISHER_HUAWEI_RENEW_FIXTURE=1")
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
	case applied := <-ready:
		if !applied {
			cmd.Wait()
			t.Fatal("renewal helper exited early")
		}
	case <-childCtx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("renewal helper did not apply")
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
	e := v.engine(p)
	n := len(v.writes)
	result, err := e.Submit(ctx, v.input.Snapshot.Manifest.ReleaseID, nil)
	if err == nil || result.AllAccepted || len(v.writes) != n || len(result.Operations) != 1 {
		t.Fatal("killed renewal used old acknowledgement or replayed")
	}
	op, _ := v.store.Operation(v.input.Snapshot.Manifest.ReleaseID+"/huawei", "authorize")
	if op.Request.AuthorizationGeneration != 1 || op.Effect != "intent" && op.Effect != "unknown" {
		t.Fatal("renewal ambiguity lost")
	}
}
