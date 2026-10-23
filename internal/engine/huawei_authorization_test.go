package engine_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

type huaweiRenewalKernelAdapter struct {
	*fiveKernelAdapter
	expired      bool
	blockUpload  bool
	checkFailure bool
	expiryChecks int
}

type huaweiKnownUnsent struct{}

func (huaweiKnownUnsent) Error() string       { return "synthetic expired grant before upload" }
func (huaweiKnownUnsent) WriteEffect() string { return "not_sent" }

func (a *huaweiRenewalKernelAdapter) ExecuteOne(ctx context.Context, q protocol.Request) (protocol.Receipt, error) {
	if q.Kind == "upload" && a.blockUpload {
		op, err := a.store.Operation(q.TargetID, q.Kind)
		if err != nil || op == nil || op.Effect != "intent" || protocol.Digest(op.Request) != protocol.Digest(q) {
			a.t.Fatal("unsent upload did not retain its exact durable intent")
		}
		return protocol.Receipt{}, huaweiKnownUnsent{}
	}
	receipt, err := a.fiveKernelAdapter.ExecuteOne(ctx, q)
	if err == nil && q.Kind == "authorize" {
		receipt.RemoteRef = "material-" + account.Key(q.MaterialOperationKey())
		a.remote.proofs[q.OperationID] = receipt
	}
	return receipt, err
}

func (a *huaweiRenewalKernelAdapter) CheckExpiredUnusedHuaweiAuthorization(_ context.Context, q protocol.Request) error {
	a.expiryChecks++
	proof, ok := a.remote.proofs[q.OperationID]
	op, err := a.store.Operation(q.TargetID, "authorize")
	if a.checkFailure || !a.expired || !ok || err != nil || op == nil || op.Effect != "confirmed_applied" || protocol.Digest(op.Request) != protocol.Digest(q) || proof.Validate(q) != nil || proof.RemoteRef != op.RemoteRef || proof.RemoteRef != "material-"+account.Key(q.MaterialOperationKey()) {
		return errors.New("synthetic private expiry or receipt proof unavailable")
	}
	return nil
}

func newHuaweiRenewalKernel(t *testing.T) (*fiveKernelFixture, *huaweiRenewalKernelAdapter) {
	t.Helper()
	v := newFiveKernelFixture(t, "huawei")
	v.adapter.steps = []string{"authorize", "upload", "bind", "metadata", "submit"}
	a := &huaweiRenewalKernelAdapter{fiveKernelAdapter: v.adapter, expired: true, blockUpload: true}
	v.engine.Adapter = a
	v.preflight(t)
	result, err := v.engine.Submit(context.Background(), v.id, nil)
	if err == nil || result.AllAccepted || !reflect.DeepEqual(a.remote.calls, []string{"authorize"}) {
		t.Fatal("synthetic expired grant did not stop before sending upload bytes")
	}
	upload, err := v.engine.Store.Operation(v.id+"/huawei", "upload")
	must(t, err)
	if upload == nil || upload.Effect != "confirmed_not_applied" {
		t.Fatal("expiry did not preserve a known-unsent upload")
	}
	return v, a
}

func huaweiRenewalKernelID(v *fiveKernelFixture) string {
	return protocol.Digest([]string{v.adapter.intent, "huawei", "authorize"})
}

func TestHuaweiAuthorizationRenewalKernelAllocatesOnceThenResumesFromNewGrant(t *testing.T) {
	v, a := newHuaweiRenewalKernel(t)
	original, err := v.engine.Store.Operation(v.id+"/huawei", "authorize")
	must(t, err)
	target, err := v.engine.Store.Target(v.id + "/huawei")
	must(t, err)
	must(t, v.engine.RenewExpiredHuaweiAuthorization(context.Background(), v.id, huaweiRenewalKernelID(v)))
	if a.expiryChecks != 1 || !reflect.DeepEqual(a.remote.calls, []string{"authorize", "authorize"}) {
		t.Fatal("renewal sent more than one allocation or changed another business resource")
	}
	renewed, err := v.engine.Store.Operation(v.id+"/huawei", "authorize")
	must(t, err)
	if renewed.Effect != "confirmed_applied" || renewed.Attempt != 2 || renewed.Request.AuthorizationGeneration != 1 || renewed.RemoteRef == original.RemoteRef || renewed.Request.OperationID != original.Request.OperationID {
		t.Fatal("renewed grant was not bound to its own persisted generation")
	}
	a.blockUpload = false
	result, err := v.engine.Submit(context.Background(), v.id, nil)
	must(t, err)
	if !result.AllAccepted || result.AllApproved || result.AllLive || !reflect.DeepEqual(a.remote.calls, []string{"authorize", "authorize", "upload", "bind", "metadata", "submit"}) {
		t.Fatal("resume replayed old upload or confused acceptance with review")
	}
	upload, err := v.engine.Store.Operation(v.id+"/huawei", "upload")
	must(t, err)
	if upload.Request.Checkpoints["authorize"] != renewed.RemoteRef || upload.Request.PreviousRef != renewed.RemoteRef {
		t.Fatal("upload reused the expired authorization checkpoint")
	}
	current, err := v.engine.Store.Target(v.id + "/huawei")
	must(t, err)
	if current.Preflight != target.Preflight {
		t.Fatal("renewal replaced the immutable preflight")
	}
	before := append([]string(nil), a.remote.calls...)
	if err = v.engine.RenewExpiredHuaweiAuthorization(context.Background(), v.id, huaweiRenewalKernelID(v)); err == nil {
		t.Fatal("completed upload chain accepted another grant renewal")
	}
	if !reflect.DeepEqual(a.remote.calls, before) {
		t.Fatal("denied post-upload renewal sent another allocation")
	}
}

func TestHuaweiAuthorizationRenewalKernelRequiresPrivateProofAndUnchangedInputs(t *testing.T) {
	for _, variant := range []string{"not-expired", "missing-proof", "artifact", "remote-drift", "active-intent", "read-only", "wrong-operation", "missing-interface", "database-failure"} {
		t.Run(variant, func(t *testing.T) {
			v, a := newHuaweiRenewalKernel(t)
			expected := huaweiRenewalKernelID(v)
			switch variant {
			case "not-expired":
				a.expired = false
			case "missing-proof":
				a.checkFailure = true
			case "artifact":
				snapshot, err := v.engine.Store.Load(v.id)
				must(t, err)
				for _, f := range snapshot.Manifest.Artifacts {
					must(t, os.WriteFile(artifact.Path(v.root, f.SHA256), []byte("replaced synthetic APK"), 0600))
				}
			case "remote-drift":
				a.remote.metadata["external"] = "changed by another actor"
			case "active-intent":
				v.engine.Adapter = huaweiRenewalActiveIntent{a}
			case "read-only":
				a.writable = false
			case "wrong-operation":
				expected = protocol.Digest("wrong synthetic operation")
			case "missing-interface":
				v.engine.Adapter = a.fiveKernelAdapter
			case "database-failure":
				v.engine.Hook = func(point string, _ protocol.Request) error {
					if point == "before_intent" {
						_, err := v.engine.Store.DB.Exec("PRAGMA query_only=ON")
						return err
					}
					return nil
				}
			}
			if err := v.engine.RenewExpiredHuaweiAuthorization(context.Background(), v.id, expected); err == nil {
				t.Fatal("renewal bypassed private proof or frozen-input validation")
			}
			if !reflect.DeepEqual(a.remote.calls, []string{"authorize"}) {
				t.Fatal("failed precondition allocated a new remote grant")
			}
			current, err := v.engine.Store.Operation(v.id+"/huawei", "authorize")
			must(t, err)
			if current.Attempt != 1 || current.Request.AuthorizationGeneration != 0 || current.Effect != "confirmed_applied" {
				t.Fatal("failed precondition reset the original authorization")
			}
			var history int
			must(t, v.engine.Store.DB.QueryRow("SELECT COUNT(*) FROM authorization_attempts").Scan(&history))
			if history != 0 {
				t.Fatal("failed precondition persisted an authorization replacement")
			}
		})
	}
}

type huaweiRenewalActiveIntent struct{ *huaweiRenewalKernelAdapter }

func (a huaweiRenewalActiveIntent) Inspect(ctx context.Context, scope string) (protocol.RemoteSnapshot, error) {
	r, err := a.huaweiRenewalKernelAdapter.Inspect(ctx, scope)
	r.ActiveIntent = protocol.Digest("another remote intent")
	return r, err
}

func TestHuaweiAuthorizationRenewalKernelInterruptedIntentNeverAllocatesAgain(t *testing.T) {
	for _, point := range []string{"after_intent", "after_execute"} {
		t.Run(point, func(t *testing.T) {
			v, a := newHuaweiRenewalKernel(t)
			v.engine.Hook = func(got string, q protocol.Request) error {
				if got == point && q.Kind == "authorize" && q.AuthorizationGeneration == 1 {
					return errors.New("synthetic renewal interruption")
				}
				return nil
			}
			if err := v.engine.RenewExpiredHuaweiAuthorization(context.Background(), v.id, huaweiRenewalKernelID(v)); err == nil {
				t.Fatal("renewal interruption did not stop")
			}
			current, err := v.engine.Store.Operation(v.id+"/huawei", "authorize")
			must(t, err)
			if current.Effect != "intent" || current.Attempt != 2 || current.Request.AuthorizationGeneration != 1 {
				t.Fatal("interrupted renewal lost the durable next intent")
			}
			before := append([]string(nil), a.remote.calls...)
			// Reconstructing the executor discards hooks and transient proof
			// checks. The persisted unresolved intent still prevents allocation.
			v.reopen(t)
			a.fiveKernelAdapter = v.adapter
			v.engine.Adapter = a
			if err = v.engine.RenewExpiredHuaweiAuthorization(context.Background(), v.id, huaweiRenewalKernelID(v)); err == nil {
				t.Fatal("unknown renewal was replaced again")
			}
			if _, err = v.engine.Submit(context.Background(), v.id, nil); err == nil {
				t.Fatal("resume replayed unresolved authorization")
			}
			if !reflect.DeepEqual(a.remote.calls, before) {
				t.Fatal("interrupted renewal caused another business request")
			}
			current, err = v.engine.Store.Operation(v.id+"/huawei", "authorize")
			must(t, err)
			if current.Effect != "intent" || current.Attempt != 2 {
				t.Fatal("recovery silently settled the unresolved renewal")
			}
			history, err := v.engine.Store.OPPOAuthorizationHistory(v.id)
			must(t, err)
			if len(history) != 1 || history[0].Effect != "confirmed_applied" {
				t.Fatal("original known outcome did not survive recovery")
			}
		})
	}
}
