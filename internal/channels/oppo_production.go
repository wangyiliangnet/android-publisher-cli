package channels

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// OPPOProductionInput refers exclusively to an already verified frozen APK.
// CredentialName is replaceable and never participates in publishing identity.
type OPPOProductionInput struct {
	Snapshot       manifest.Snapshot
	Scope          string
	APKPath        string
	CredentialName string
}

// OPPOMetadataError contains field names, never values from the vendor response.
type OPPOMetadataError struct {
	Missing   []string
	Available []string // Related field names only, never their private values.
}

func (e *OPPOMetadataError) Error() string {
	return "OPPO existing metadata is incomplete: " + strings.Join(e.Missing, ", ")
}

// OPPOProduction seals the full baseline in Keychain. The journal and ordinary
// output see only its opaque reference. All requests still pass through Adapter
// and the existing account coordinator, transport and operation journal.
type OPPOProduction struct {
	base        *Adapter
	runtime     Runtime
	target      Target
	baselineRef string
	writable    bool
	diagnostic  *OPPOWriteDiagnostic
}

func (*OPPOProduction) String() string { return "OPPO frozen production adapter" }

// ProductionScope is the kernel's explicit opt-in for a single production
// scope/intent. Other adapters, including StaticRuntime adapters, cannot opt in
// merely by being attached to an Engine with production state.
func (p *OPPOProduction) ProductionScope() (string, string, bool) {
	return p.target.Scope, p.target.IntentDigest, p.writable
}
func (p *OPPOProduction) BaselineRef() string { return p.baselineRef }
func (p *OPPOProduction) CopyrightSource() string {
	return oppoCopyrightSource(p.target.Existing.Metadata)
}

func (p *OPPOProduction) LastWriteDiagnostic() *OPPOWriteDiagnostic {
	if p.diagnostic == nil || p.diagnostic.OperationID == "" {
		return nil
	}
	d := *p.diagnostic
	return &d
}

func (p *OPPOProduction) CheckUnusedOPPOAuthorization(ctx context.Context, q protocol.Request) error {
	if !p.writable || q.Kind != "authorize" || q.OperationID != protocol.Digest([]string{p.target.IntentDigest, "oppo", "authorize"}) || q.Scope != p.target.Scope || q.IntentDigest != p.target.IntentDigest {
		return fail("authorization_replacement_outside_scope", "not_sent")
	}
	return p.runtime.WithClient(ctx, p.target, func(_ *Client, materials Materials) error {
		ref := "material-" + account.Key(q.OperationID)
		value, err := materials.Get(ctx, ref)
		if value != nil {
			value.Destroy()
		}
		if errors.Is(err, credentials.ErrMissing) {
			return nil
		}
		if err != nil {
			return credentials.PublicError(err)
		}
		return fail("authorization_checkpoint_already_exists", "not_sent")
	})
}

// NewOPPOProduction creates a baseline when ref is empty (preflight only), or
// reopens the exact saved baseline. It never acquires an upload authorization.
func (r Runtime) NewOPPOProduction(ctx context.Context, in OPPOProductionInput, ref string, writable bool) (*OPPOProduction, error) {
	s := in.Snapshot
	m := s.Manifest
	if s.Verify(manifest.Production) != nil || len(m.Targets) != 1 || len(m.Metadata) != 0 || m.ReleasePolicy.Mode != "after_approval" || in.Scope == "" || !credentials.ValidCredentialName(in.CredentialName) || (writable && ref == "") {
		return nil, fail("unsupported_oppo_production_scope", "not_sent")
	}
	target, ok := m.Targets["oppo"]
	if !ok || !account.ValidName(target.AccountRef) || !account.ValidName(target.AppRef) {
		return nil, fail("unsupported_oppo_production_scope", "not_sent")
	}
	f, err := NewNamedFile(in.APKPath, "oppo.apk")
	if err != nil {
		return nil, err
	}
	if f.SHA256() != m.Artifacts[target.Artifact].SHA256 {
		return nil, fail("frozen_artifact_mismatch", "not_sent")
	}
	t := Target{Scope: in.Scope, IntentDigest: s.Digest, Account: account.Scope{Channel: "oppo", Account: target.AccountRef}, AppID: target.AppRef, PackageName: m.PackageName, VersionCode: m.VersionCode, VersionName: m.VersionName, APK: f, ABI: m.Artifacts[target.Artifact].ABI, Notes: s.Notes, Policy: m.ReleasePolicy, Credential: CredentialConfig{ID: target.AccountRef, SecretName: in.CredentialName}}
	baseSender := r.sender
	diagnostic := &OPPOWriteDiagnostic{}
	r.sender = func(lease *account.Lease, scope account.Scope) Sender {
		observe := func(ctx context.Context, q transport.Request, status int, body []byte, err error) error {
			return recordOPPOResponse(ctx, lease, keychainMaterials{vault: r.Manager.Vault, scope: scope, lease: lease}, t, diagnostic, q, status, body, err)
		}
		var sender Sender = Controlled{Lease: lease, Scope: scope, observe: observe}
		if baseSender != nil {
			sender = observedOPPOSender{Sender: baseSender(lease, scope), observe: observe}
		}
		return oppoProductionSender{underlying: sender, target: t, writable: writable}
	}
	err = r.WithClient(ctx, t, func(c *Client, materials Materials) error {
		if ref != "" {
			return loadMaterial(ctx, materials, ref, &t.Existing)
		}
		d, err := c.OPPODetails(ctx, t.PackageName, 0)
		if err != nil {
			return err
		}
		if err = validateOPPOExisting(t, d); err != nil {
			return err
		}
		// The complete details are private material, not journal metadata.
		key := "oppo-baseline-" + protocol.Digest(struct {
			Intent, Scope string
			Details       Details
		}{t.IntentDigest, t.Scope, d})
		candidate := "material-" + account.Key(key)
		stored, getErr := materials.Get(ctx, candidate)
		if getErr == nil {
			defer stored.Destroy()
			b := stored.Bytes()
			defer clear(b)
			var prior Details
			if json.Unmarshal(b, &prior) != nil || !reflect.DeepEqual(prior, d) {
				return fail("oppo_baseline_invalid", "not_sent")
			}
			ref = candidate
		} else if errors.Is(getErr, credentials.ErrMissing) {
			ref, err = saveMaterial(ctx, materials, key, d)
			if err != nil {
				return err
			}
		} else {
			return getErr
		}
		t.Existing = d
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = validateOPPOExisting(t, t.Existing); err != nil {
		return nil, err
	}
	t.Metadata = normalizeMetadata("oppo", t.Existing.Metadata)
	a, err := NewAdapter([]Target{t}, r)
	if err != nil {
		return nil, err
	}
	p := &OPPOProduction{base: a, runtime: r, target: t, baselineRef: ref, writable: writable, diagnostic: diagnostic}
	// Validate every required existing field and the actual proposed notes/policy
	// before reporting a successful preflight or creating a business operation.
	q := protocol.Request{Scope: t.Scope, Channel: "oppo", IntentDigest: t.IntentDigest, PackageName: t.PackageName, VersionCode: t.VersionCode, VersionName: t.VersionName, ArtifactSHA256: t.APK.SHA256(), Policy: t.Policy, Metadata: desiredMetadata(t), SnapshotDigest: protocol.Digest(t.Metadata), AdapterBinding: adapterBinding(t)}
	if _, err = a.BuildPlan(q); err != nil {
		return nil, err
	}
	return p, nil
}

func validateOPPOExisting(t Target, d Details) error {
	if d.PackageName != t.PackageName || d.AppID != t.AppID || d.VersionCode <= 0 || d.VersionCode >= t.VersionCode {
		return fail("oppo_remote_identity_or_version_changed", "not_sent")
	}
	var missing []string
	for _, key := range oppoRequiredMetadata {
		if key == "copyright_url" && oppoCopyrightSource(d.Metadata) != "" {
			continue
		}
		if key != "update_desc" && d.Metadata[key] == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) != 0 {
		var available []string
		if d.Metadata["copyright_url"] == "" && d.Metadata["electronic_cert_url"] != "" {
			available = append(available, "electronic_cert_url")
		}
		return &OPPOMetadataError{Missing: missing, Available: available}
	}
	return nil
}

func (p *OPPOProduction) publicMetadata() map[string]string {
	return map[string]string{"oppo_baseline_ref": p.baselineRef}
}
func (p *OPPOProduction) AdapterBinding(scope string) (string, error) {
	return p.base.AdapterBinding(scope)
}
func (p *OPPOProduction) Inspect(ctx context.Context, scope string) (protocol.RemoteSnapshot, error) {
	if scope != p.target.Scope {
		return protocol.RemoteSnapshot{}, fail("target_outside_frozen_scope", "not_sent")
	}
	err := p.runtime.WithClient(ctx, p.target, func(c *Client, _ Materials) error {
		d, err := c.OPPODetails(ctx, p.target.PackageName, 0)
		if err != nil {
			return err
		}
		if err = validateOPPOExisting(p.target, d); err != nil {
			return err
		}
		if d.VersionCode != p.target.Existing.VersionCode || !reflect.DeepEqual(normalizeMetadata("oppo", d.Metadata), p.target.Metadata) {
			return fail("oppo_preflight_changed", "not_sent")
		}
		return nil
	})
	if err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	return protocol.RemoteSnapshot{Scope: scope, AdapterBinding: adapterBinding(p.target), Metadata: p.publicMetadata()}, nil
}
func (p *OPPOProduction) translate(q protocol.Request) (protocol.Request, error) {
	expected := p.publicMetadata()
	expected["release_notes"] = p.target.Notes
	if !reflect.DeepEqual(q.Metadata, expected) || q.SnapshotDigest != protocol.Digest(p.publicMetadata()) {
		return protocol.Request{}, fail("frozen_oppo_baseline_mismatch", "not_sent")
	}
	q.Metadata = desiredMetadata(p.target)
	q.SnapshotDigest = protocol.Digest(p.target.Metadata)
	return q, nil
}
func (p *OPPOProduction) BuildPlan(q protocol.Request) ([]string, error) {
	translated, err := p.translate(q)
	if err != nil {
		return nil, err
	}
	return p.base.BuildPlan(translated)
}
func (p *OPPOProduction) ExecuteOne(ctx context.Context, q protocol.Request) (protocol.Receipt, error) {
	if !p.writable {
		return protocol.Receipt{}, fail("oppo_submission_not_authorized", "not_sent")
	}
	translated, err := p.translate(q)
	if err != nil {
		return protocol.Receipt{}, err
	}
	receipt, err := p.base.ExecuteOne(ctx, translated)
	if err == nil {
		receipt.RequestDigest = protocol.Digest(q)
	}
	return receipt, err
}
func (p *OPPOProduction) Reconcile(ctx context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	translated, err := p.translate(q)
	if err != nil {
		return protocol.Reconciliation{}, err
	}
	r, err := p.base.Reconcile(ctx, translated)
	if err != nil {
		return protocol.Reconciliation{}, err
	}
	if err = p.readWriteDiagnostic(ctx, q); err != nil {
		return protocol.Reconciliation{}, err
	}
	if q.Kind == "upload" {
		receipt, e := p.reconcileSealedUpload(ctx, q)
		if e != nil {
			return protocol.Reconciliation{}, e
		}
		if receipt != nil {
			r.Result = "confirmed_applied"
			r.Receipt = receipt
		}
	}
	r.RequestDigest = protocol.Digest(q)
	return r, err
}
func (p *OPPOProduction) Observe(ctx context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	return p.base.Observe(ctx, scope, version, ref)
}

var _ protocol.Adapter = (*OPPOProduction)(nil)
var _ protocol.Planner = (*OPPOProduction)(nil)
