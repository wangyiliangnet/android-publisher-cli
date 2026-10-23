package channels

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// NewFiveProduction reopens a sealed preparation baseline for one frozen update.
// Creating a baseline remains a read-only preflight operation. Business writes
// require a preexisting baseline and an explicit writable construction.
func (r Runtime) NewFiveProduction(ctx context.Context, in PreparationInput, ref string, writable bool) (*PreparedProduction, error) {
	if writable && ref == "" {
		return nil, fail("production_preflight_required", "not_sent")
	}
	p, err := r.NewPreparedProduction(ctx, in, ref)
	if err != nil {
		return nil, err
	}
	p.enabled, p.writable = true, writable
	p.diagnostic = &FiveWriteDiagnostic{}
	if p.target.Account.Channel == "tencent" {
		// Capture the real public reader before the publishing transport wraps
		// sender; a synthetic sender remains unable to contact the public web.
		r = r.withPublicStoreReader()
	}
	baseSender := r.sender
	r.sender = func(lease *account.Lease, scope account.Scope) Sender {
		observe := func(ctx context.Context, q transport.Request, status int, body []byte, sendErr error) error {
			return recordFiveResponse(ctx, lease, keychainMaterials{vault: r.Manager.Vault, scope: scope, lease: lease}, p.target, p.diagnostic, q, status, body, sendErr)
		}
		var sender Sender = Controlled{Lease: lease, Scope: scope, observe: observe}
		if baseSender != nil {
			sender = observedOPPOSender{Sender: baseSender(lease, scope), observe: observe}
		}
		return fiveProductionSender{underlying: sender, target: p.target, writable: writable}
	}
	p.runtime = r
	p.base, err = NewAdapter([]Target{p.target}, r)
	return p, err
}

type fiveVerifiedReceipt struct {
	Request protocol.Request
	Receipt protocol.Receipt
}

type fiveOperationContext struct {
	Request   protocol.Request
	StartedAt time.Time
}
type fiveOperationContextKey struct{}

func fiveReceiptKey(operation string) string { return "five-receipt-" + operation }
func (p *PreparedProduction) progressMarker(kind string) string {
	return protocol.Digest([]string{p.target.IntentDigest, p.target.Scope, kind})
}

func (p *PreparedProduction) validateRequest(q protocol.Request) (protocol.Request, error) {
	translated, err := p.translate(q)
	if err != nil {
		return protocol.Request{}, err
	}
	if err = p.base.match(p.target, translated, true); err != nil {
		return protocol.Request{}, err
	}
	steps, err := p.base.BuildPlan(translated)
	if err != nil {
		return protocol.Request{}, err
	}
	found := false
	for _, s := range steps {
		found = found || s == q.Kind
	}
	if !found {
		return protocol.Request{}, fail("operation_not_in_plan", "not_sent")
	}
	return translated, nil
}

func (p *PreparedProduction) receiptProof(ctx context.Context, materials Materials, kind string, generations ...int) (*fiveVerifiedReceipt, error) {
	generation := 0
	if len(generations) > 1 {
		return nil, fail("unsupported_authorization_generation", "not_sent")
	}
	if len(generations) == 1 {
		generation = generations[0]
	}
	op := protocol.Digest([]string{p.target.IntentDigest, p.target.Account.Channel, kind})
	key := "material-" + account.Key(fiveReceiptKey((protocol.Request{OperationID: op, AuthorizationGeneration: generation}).MaterialOperationKey()))
	secret, err := materials.Get(ctx, key)
	if errors.Is(err, credentials.ErrMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, credentials.PublicError(err)
	}
	defer secret.Destroy()
	b := secret.Bytes()
	defer clear(b)
	var proof fiveVerifiedReceipt
	if json.Unmarshal(b, &proof) != nil || proof.Request.OperationID != op || proof.Request.Kind != kind || proof.Request.AuthorizationGeneration != generation || proof.Receipt.Validate(proof.Request) != nil || proof.Receipt.Effect != "confirmed_applied" {
		return nil, fail("production_receipt_evidence_invalid", "not_sent")
	}
	if _, err = p.validateRequest(proof.Request); err != nil {
		return nil, err
	}
	return &proof, nil
}

func (p *PreparedProduction) confirmedProgress(ctx context.Context, materials Materials) (map[string]bool, error) {
	progress := map[string]bool{}
	if !p.enabled || (p.target.Account.Channel != "huawei" && p.target.Account.Channel != "honor") {
		return progress, nil
	}
	proofs := map[string]*fiveVerifiedReceipt{}
	for _, kind := range []string{"bind", "metadata"} {
		proof, err := p.receiptProof(ctx, materials, kind)
		if err != nil {
			return nil, err
		}
		progress[kind] = proof != nil
		proofs[kind] = proof
	}
	if progress["metadata"] && !progress["bind"] {
		return nil, fail("production_progress_incomplete", "not_sent")
	}
	if progress["metadata"] && proofs["metadata"].Request.Checkpoints["bind"] != proofs["bind"].Receipt.RemoteRef {
		return nil, fail("production_progress_incomplete", "not_sent")
	}
	return progress, nil
}

func (p *PreparedProduction) ExecuteOne(ctx context.Context, q protocol.Request) (protocol.Receipt, error) {
	if !p.enabled || !p.writable {
		return protocol.Receipt{}, fail("production_preflight_only", "not_sent")
	}
	translated, err := p.validateRequest(q)
	if err != nil {
		return protocol.Receipt{}, err
	}
	ctx = context.WithValue(ctx, fiveOperationContextKey{}, fiveOperationContext{q, time.Now().UTC()})
	receipt, err := p.base.ExecuteOne(ctx, translated)
	if err != nil {
		return protocol.Receipt{}, err
	}
	receipt.RequestDigest = protocol.Digest(q)
	if err = receipt.Validate(q); err != nil {
		return protocol.Receipt{}, unknown()
	}
	// Seal the already verified response acknowledgement before returning it to
	// the journal. A crash between these two durable saves can be reconciled
	// locally without issuing the business request again.
	err = p.runtime.Manager.WithAccount(ctx, p.target.Account, func(s *credentials.Session) error {
		materials := keychainMaterials{vault: p.runtime.Manager.Vault, scope: p.target.Account, lease: s.Lease()}
		_, err := saveMaterial(ctx, materials, fiveReceiptKey(q.MaterialOperationKey()), fiveVerifiedReceipt{q, receipt})
		return err
	})
	if err != nil {
		return protocol.Receipt{}, fail("production_receipt_evidence_unavailable", "unknown")
	}
	return receipt, nil
}

func (p *PreparedProduction) Reconcile(ctx context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	if !p.enabled {
		return protocol.Reconciliation{}, fail("production_preflight_only", "not_sent")
	}
	if _, err := p.validateRequest(q); err != nil {
		return protocol.Reconciliation{}, err
	}
	r := protocol.Reconciliation{OperationID: q.OperationID, RequestDigest: protocol.Digest(q), Result: "inconclusive"}
	err := p.runtime.Manager.WithAccount(ctx, p.target.Account, func(s *credentials.Session) error {
		materials := keychainMaterials{vault: p.runtime.Manager.Vault, scope: p.target.Account, lease: s.Lease()}
		proof, err := p.receiptProof(ctx, materials, q.Kind, q.AuthorizationGeneration)
		if err != nil {
			return err
		}
		if proof != nil {
			if !reflect.DeepEqual(proof.Request, q) {
				return fail("production_receipt_request_conflict", "not_sent")
			}
			r.Result, r.Receipt = "confirmed_applied", &proof.Receipt
		}
		env, err := p.readFiveResponse(ctx, s.Lease(), materials, q)
		if err != nil {
			return err
		}
		if env != nil {
			defer clear(env.Body)
		}
		if proof == nil && env != nil {
			receipt, err := p.recoverFiveResponse(ctx, materials, q, *env)
			if err != nil {
				return err
			}
			if receipt != nil {
				r.Result, r.Receipt = "confirmed_applied", receipt
			}
		}
		return nil
	})
	return r, err
}

func (p *PreparedProduction) Observe(ctx context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	if !p.enabled {
		return protocol.Observation{}, fail("production_preflight_only", "not_sent")
	}
	return p.base.Observe(ctx, scope, version, ref)
}

func (p *PreparedProduction) ExpectedSnapshot(q protocol.Request, before protocol.RemoteSnapshot) (protocol.RemoteSnapshot, error) {
	if _, err := p.validateRequest(q); err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	if before.Scope != p.target.Scope || before.AdapterBinding != adapterBinding(p.target) || before.ActiveIntent != "" || before.Metadata["existing_baseline_ref"] != p.ref {
		return protocol.RemoteSnapshot{}, fail("unexpected_snapshot_transition", "not_sent")
	}
	allowed := p.publicMetadata()
	if before.Metadata["confirmed_bind"] == p.progressMarker("bind") {
		allowed["confirmed_bind"] = p.progressMarker("bind")
	}
	if before.Metadata["confirmed_metadata"] == p.progressMarker("metadata") {
		allowed["confirmed_metadata"] = p.progressMarker("metadata")
	}
	if !reflect.DeepEqual(allowed, before.Metadata) {
		return protocol.RemoteSnapshot{}, fail("unexpected_snapshot_transition", "not_sent")
	}
	out := before
	out.Metadata = copyFields(before.Metadata)
	if p.target.Account.Channel == "huawei" || p.target.Account.Channel == "honor" {
		if q.Kind == "bind" {
			out.Metadata["confirmed_bind"] = p.progressMarker("bind")
		}
		if q.Kind == "metadata" {
			if out.Metadata["confirmed_bind"] != p.progressMarker("bind") {
				return protocol.RemoteSnapshot{}, fail("unexpected_snapshot_transition", "not_sent")
			}
			out.Metadata["confirmed_metadata"] = p.progressMarker("metadata")
		}
	}
	return out, nil
}

var _ protocol.PreflightAdvancer = (*PreparedProduction)(nil)
