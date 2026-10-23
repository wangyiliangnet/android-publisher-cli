package channels

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// Target binds one frozen publishing intent to its already frozen files and
// preflight data. Construction deep-copies all maps and slices. Secret material
// is resolved solely by the Executor inside an account lease.
type Target struct {
	Scope, IntentDigest             string
	Account                         account.Scope
	AppID, PackageName, VersionName string
	VersionCode                     int64
	APK, Icon                       File
	ABI                             []string
	Notes                           string
	Policy                          manifest.Policy
	Metadata                        map[string]string
	Patch                           map[string]manifest.Patch
	Existing                        Details
	Credential                      CredentialConfig
	LanguageID                      string
	CompatibleDevice                int
	Mainland                        *bool
}

func (Target) String() string               { return "frozen channel target" }
func (t Target) GoString() string           { return t.String() }
func (Target) MarshalJSON() ([]byte, error) { return []byte(`"frozen channel target"`), nil }

type Capabilities struct {
	Steps           []string `json:"steps"`
	Implemented     bool     `json:"implemented"`
	AccountVerified bool     `json:"account_verified"`
	LiveVerified    bool     `json:"live_verified"`
	Reconciliation  string   `json:"reconciliation"`
}

type Adapter struct {
	targets map[string]Target
	runtime Executor
}

func NewAdapter(targets []Target, runtime Executor) (*Adapter, error) {
	if runtime == nil || len(targets) == 0 {
		return nil, fail("invalid_adapter", "not_sent")
	}
	a := &Adapter{targets: map[string]Target{}, runtime: runtime}
	for _, t := range targets {
		if t.Scope == "" || !t.Account.Valid() || !manifest.ValidHash(t.IntentDigest) || t.PackageName == "" || t.VersionCode <= 0 || t.VersionName == "" || !strings.HasSuffix(t.APK.Name(), ".apk") || t.APK.SHA256() == "" || t.APK.Size() <= 0 || a.targets[t.Scope].Scope != "" {
			return nil, fail("invalid_target", "not_sent")
		}
		if t.Policy.Mode != "after_approval" && t.Policy.Mode != "scheduled" {
			return nil, fail("unsupported_release_policy", "not_sent")
		}
		if (t.Policy.Mode == "after_approval" && t.Policy.At != "") || (t.Policy.Mode == "scheduled" && scheduledAt(t.Policy) == nil) {
			return nil, fail("unsupported_release_policy", "not_sent")
		}
		if t.Account.Channel == "oppo" || t.Account.Channel == "vivo" {
			if t.Existing.PackageName != t.PackageName || t.Existing.VersionCode <= 0 || t.Existing.VersionCode >= t.VersionCode || !reflect.DeepEqual(normalizeMetadata(t.Account.Channel, t.Existing.Metadata), t.Metadata) {
				return nil, fail("frozen_existing_metadata_mismatch", "not_sent")
			}
			if t.Account.Channel == "oppo" && t.Existing.AppID != t.AppID {
				return nil, fail("frozen_app_mismatch", "not_sent")
			}
		}
		a.targets[t.Scope] = cloneTarget(t)
	}
	return a, nil
}

func (a *Adapter) target(scope string) (Target, error) {
	if a == nil || a.runtime == nil {
		return Target{}, fail("invalid_adapter", "not_sent")
	}
	t, ok := a.targets[scope]
	if !ok {
		return Target{}, fail("target_outside_frozen_scope", "not_sent")
	}
	return cloneTarget(t), nil
}
func cloneTarget(t Target) Target {
	t.Metadata = copyFields(t.Metadata)
	t.Existing.Metadata = copyFields(t.Existing.Metadata)
	t.ABI = slices.Clone(t.ABI)
	patch := map[string]manifest.Patch{}
	for k, v := range t.Patch {
		patch[k] = v
	}
	t.Patch = patch
	if t.Mainland != nil {
		b := *t.Mainland
		t.Mainland = &b
	}
	return t
}
func (a *Adapter) Capabilities(scope string) (Capabilities, error) {
	t, err := a.target(scope)
	if err != nil {
		return Capabilities{}, err
	}
	var steps []string
	switch t.Account.Channel {
	case "xiaomi":
		steps = []string{"push"}
	case "tencent", "oppo":
		steps = []string{"authorize", "upload", "submit"}
	case "huawei", "honor":
		steps = []string{"authorize", "upload", "bind", "metadata", "submit"}
	case "vivo":
		steps = []string{"upload", "submit"}
	default:
		return Capabilities{}, fail("unsupported_channel", "not_sent")
	}
	return Capabilities{Steps: steps, Implemented: true, Reconciliation: "inconclusive_without_operation_evidence"}, nil
}
func (a *Adapter) BuildPlan(q protocol.Request) ([]string, error) {
	t, err := a.target(q.Scope)
	if err != nil {
		return nil, err
	}
	if err = a.match(t, q, false); err != nil {
		return nil, err
	}
	if _, _, err = policyFields(t.Policy, time.Now().UTC()); err != nil {
		return nil, err
	}
	if t.Notes == "" {
		return nil, fail("missing_release_notes", "not_sent")
	}
	if t.Account.Channel == "xiaomi" && t.Icon.SHA256() == "" {
		return nil, fail("missing_icon", "not_sent")
	}
	if t.Account.Channel == "xiaomi" {
		if t.Existing.PackageName != t.PackageName || t.Existing.VersionCode <= 0 || t.Existing.VersionCode >= t.VersionCode || !t.Existing.UpdateAllowed {
			return nil, fail("existing_update_not_verified", "not_sent")
		}
		metadata := desiredMetadata(t)
		if err := required(metadata["app_name"], metadata["privacy_url"]); err != nil {
			return nil, err
		}
	}
	if (t.Account.Channel == "huawei" || t.Account.Channel == "honor") && t.LanguageID == "" {
		return nil, fail("missing_language_mapping", "not_sent")
	}
	if t.Account.Channel == "huawei" || t.Account.Channel == "honor" {
		snapshot, e := boundLanguage(t)
		if e != nil {
			return nil, e
		}
		if err = snapshot.validate(t.Account.Channel, t.Notes); err != nil {
			return nil, err
		}
	}
	if len(t.Patch) > 0 && (t.Account.Channel == "huawei" || t.Account.Channel == "honor") {
		return nil, fail("unsupported_metadata_patch", "not_sent")
	}
	for key, p := range t.Patch {
		if p.Action != "set" || p.Value == "" {
			return nil, fail("unverified_clear_semantics", "not_sent")
		}
		if !supportedPatch(t.Account.Channel, key) {
			return nil, fail("unsupported_metadata_patch", "not_sent")
		}
	}
	if t.Account.Channel == "tencent" {
		if _, err := tencentSlot(t.ABI); err != nil {
			return nil, err
		}
	}
	if t.Account.Channel == "oppo" || t.Account.Channel == "vivo" {
		patch := map[string]string{}
		for key, p := range t.Patch {
			patch[key] = p.Value
		}
		typ, at := onlinePolicy(t.Policy)
		if t.Account.Channel == "oppo" {
			patch["update_desc"] = t.Notes
			_, err = oppoUpdateFields(OPPOUpdateInput{PackageName: t.PackageName, VersionCode: t.VersionCode, Existing: t.Existing, Patch: patch, OnlineType: typ, ScheduledAt: at}, time.Now().UTC())
		} else {
			patch["updateDesc"] = t.Notes
			_, err = vivoUpdateFields(VivoUpdateInput{PackageName: t.PackageName, VersionCode: t.VersionCode, Existing: t.Existing, Patch: patch, OnlineType: typ, ScheduledAt: at, CompatibleDevice: t.CompatibleDevice}, time.Now().UTC())
		}
		if err != nil {
			return nil, err
		}
	}
	caps, err := a.Capabilities(q.Scope)
	return caps.Steps, err
}

func supportedPatch(channel, key string) bool {
	switch channel {
	case "xiaomi":
		return key == "app_name" || key == "privacy_url"
	case "tencent":
		return key == "app_name" || key == "introduce" || key == "one_word_summary"
	case "oppo":
		return slices.Contains(oppoMetadataFields[:len(oppoMetadataFields)-5], key) && key != "update_desc" && key != "online_type" && key != "sche_online_time"
	case "vivo":
		return slices.Contains(vivoMetadataFields, key) && key != "updateDesc" && key != "onlineType" && key != "status" && key != "saleStatus" && key != "compatibleDevice"
	}
	return false
}
func desiredMetadata(t Target) map[string]string {
	v := manifest.ApplyPatch(t.Metadata, t.Patch)
	v["release_notes"] = t.Notes
	return v
}
func (a *Adapter) match(t Target, q protocol.Request, operation bool) error {
	if !q.ValidAuthorizationGeneration() {
		return fail("unsupported_authorization_generation", "not_sent")
	}
	if q.AdapterBinding != adapterBinding(t) {
		return fail("frozen_adapter_configuration_mismatch", "not_sent")
	}
	if q.Scope != t.Scope || q.Channel != t.Account.Channel || q.PackageName != t.PackageName || q.VersionCode != t.VersionCode || q.VersionName != t.VersionName || q.ArtifactSHA256 != t.APK.SHA256() || q.Policy != t.Policy || (t.IntentDigest != "" && q.IntentDigest != t.IntentDigest) {
		return fail("frozen_target_mismatch", "not_sent")
	}
	if operation && (q.OperationID != protocol.Digest([]string{q.IntentDigest, q.Channel, q.Kind}) || q.TargetID == "" || q.IntentDigest == "") {
		return fail("missing_operation_identity", "not_sent")
	}
	if !reflect.DeepEqual(q.Metadata, desiredMetadata(t)) {
		return fail("frozen_metadata_mismatch", "not_sent")
	}
	if operation && q.SnapshotDigest != protocol.Digest(t.Metadata) {
		return fail("frozen_snapshot_mismatch", "not_sent")
	}
	return nil
}

func (a *Adapter) Inspect(ctx context.Context, scope string) (protocol.RemoteSnapshot, error) {
	t, err := a.target(scope)
	if err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	var d Details
	err = a.runtime.WithClient(ctx, t, func(c *Client, _ Materials) error {
		var err error
		switch t.Account.Channel {
		case "xiaomi":
			d, err = c.XiaomiDetails(ctx, t.PackageName)
		case "tencent":
			d, err = c.TencentDetails(ctx, t.PackageName, t.AppID)
		case "huawei":
			if err = c.HuaweiVerifyApp(ctx, t.AppID, t.PackageName); err != nil {
				return err
			}
			d, err = c.HuaweiDetails(ctx, t.AppID)
		case "honor":
			d, err = c.HonorDetails(ctx, t.PackageName)
		case "oppo":
			d, err = c.OPPODetails(ctx, t.PackageName, 0)
		case "vivo":
			d, err = c.VivoDetails(ctx, t.PackageName)
		default:
			return fail("unsupported_channel", "not_sent")
		}
		if err != nil {
			return err
		}
		if t.Account.Channel == "xiaomi" && !d.UpdateAllowed {
			return fail("existing_update_not_allowed", "not_sent")
		}
		if d.PackageName != "" && d.PackageName != t.PackageName {
			return unknown()
		}
		if d.AppID != "" && t.AppID != "" && d.AppID != t.AppID {
			return unknown()
		}
		if t.Account.Channel == "huawei" || t.Account.Channel == "honor" {
			var local LocalizationSnapshot
			if t.Account.Channel == "huawei" {
				local, err = c.HuaweiLanguageSnapshot(ctx, t.AppID, t.LanguageID)
			} else {
				local, err = c.HonorLanguageSnapshot(ctx, t.AppID, t.LanguageID)
			}
			if err != nil {
				return err
			}
			if d.Metadata == nil {
				d.Metadata = map[string]string{}
			}
			flattenLanguage(d.Metadata, local)
		}
		return nil
	})
	if err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	return protocol.RemoteSnapshot{Scope: scope, AdapterBinding: adapterBinding(t), Metadata: normalizeMetadata(t.Account.Channel, d.Metadata)}, nil
}

func normalizeMetadata(channel string, metadata map[string]string) map[string]string {
	out := copyFields(metadata)
	var dynamic []string
	switch channel {
	case "huawei":
		dynamic = []string{"version_id"}
	case "oppo":
		dynamic = []string{"audit_status", "audit_status_name", "state", "apk_md5", "version_id"}
	case "vivo":
		dynamic = []string{"status", "saleStatus"}
	}
	for _, key := range dynamic {
		delete(out, key)
	}
	return out
}

// ExecuteOne issues exactly one explicit business operation. Its request intent
// must already be durable; this adapter never retries or reconciles inline.
func (a *Adapter) ExecuteOne(ctx context.Context, q protocol.Request) (protocol.Receipt, error) {
	t, err := a.target(q.Scope)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if err = a.match(t, q, true); err != nil {
		return protocol.Receipt{}, err
	}
	steps, err := a.BuildPlan(q)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if !slices.Contains(steps, q.Kind) {
		return protocol.Receipt{}, fail("operation_not_in_plan", "not_sent")
	}
	var ref string
	err = a.runtime.WithClient(ctx, t, func(c *Client, materials Materials) error {
		var err error
		switch q.Kind {
		case "authorize":
			var g UploadGrant
			switch t.Account.Channel {
			case "tencent":
				g, err = c.TencentAuthorize(ctx, q.OperationID, t.PackageName, t.AppID, t.APK)
			case "huawei":
				g, err = c.HuaweiAuthorize(ctx, q.OperationID, t.AppID, t.APK, t.Mainland)
			case "honor":
				g, err = c.HonorAuthorize(ctx, q.OperationID, t.AppID, t.APK)
			case "oppo":
				g, err = c.OPPOAuthorize(ctx, q.OperationID)
			}
			if err != nil {
				return err
			}
			ref, err = saveGrant(ctx, materials, q.MaterialOperationKey(), g)
			return err
		case "upload":
			if t.Account.Channel == "vivo" {
				f, err := c.VivoUpload(ctx, q.OperationID, t.PackageName, t.APK)
				if err != nil {
					return err
				}
				if f.PackageName != t.PackageName || f.VersionCode != t.VersionCode || f.VersionName != t.VersionName || f.MD5 != t.APK.MD5() {
					return unknown()
				}
				ref, err = saveMaterial(ctx, materials, q.OperationID, struct {
					SerialNumber, PackageName, VersionName, MD5 string
					VersionCode                                 int64
				}{f.SerialNumber, f.PackageName, f.VersionName, f.MD5, f.VersionCode})
				return err
			}
			key, err := materialCheckpoint(q, "authorize")
			if err != nil {
				return err
			}
			g, err := loadGrant(ctx, materials, key)
			if err != nil {
				return err
			}
			var accepted Acceptance
			switch t.Account.Channel {
			case "tencent":
				accepted, err = c.TencentUpload(ctx, q.OperationID, g, t.APK)
			case "huawei":
				accepted, err = c.HuaweiUpload(ctx, q.OperationID, g, t.APK)
			case "honor":
				accepted, err = c.HonorUpload(ctx, q.OperationID, t.AppID, g, t.APK)
			case "oppo":
				f, err := c.OPPOUpload(ctx, q.OperationID, g, t.APK)
				if err != nil {
					return err
				}
				ref, err = saveMaterial(ctx, materials, q.OperationID, oppoFileCheckpoint{f.URL, f.MD5, f.Ref, f.Size})
				return err
			}
			if err != nil {
				return err
			}
			if accepted.Ref == "" {
				return unknown()
			}
			ref = accepted.Ref
			return nil
		case "bind":
			key, err := materialCheckpoint(q, "authorize")
			if err != nil {
				return err
			}
			g, err := loadGrant(ctx, materials, key)
			if err != nil {
				return err
			}
			if q.Checkpoints["upload"] != g.Ref {
				return fail("checkpoint_mismatch", "not_sent")
			}
			var accepted Acceptance
			if t.Account.Channel == "huawei" {
				accepted, err = c.HuaweiBind(ctx, q.OperationID, t.AppID, t.APK, g)
			} else {
				accepted, err = c.HonorBind(ctx, q.OperationID, t.AppID, g)
			}
			if err != nil {
				return err
			}
			if accepted.Ref == "" {
				return unknown()
			}
			if t.Account.Channel == "huawei" {
				g.ReadyAt = c.now().Add(2 * time.Minute)
				ref, err = saveGrant(ctx, materials, q.OperationID, g)
				return err
			}
			ref = accepted.Ref
			return nil
		case "push":
			accepted, err := c.XiaomiPush(ctx, q.OperationID, XiaomiUpdateInput{PackageName: t.PackageName, AppName: q.Metadata["app_name"], PrivacyURL: q.Metadata["privacy_url"], Notes: t.Notes, VersionName: t.VersionName, APK: t.APK, Icon: t.Icon, OnlineAt: scheduledAt(t.Policy)})
			ref = accepted.Ref
			return err
		case "metadata":
			if q.Checkpoints["bind"] == "" {
				return fail("checkpoint_missing", "not_sent")
			}
			snapshot, err := boundLanguage(t)
			if err != nil {
				return err
			}
			var accepted Acceptance
			if t.Account.Channel == "huawei" {
				accepted, err = c.HuaweiLanguagePatch(ctx, q.OperationID, snapshot, t.Notes)
			} else {
				accepted, err = c.HonorLanguagePatchFrozen(ctx, q.OperationID, snapshot, t.Notes)
			}
			if err != nil {
				return err
			}
			ref = accepted.Ref
			return nil
		case "submit":
			var accepted Acceptance
			switch t.Account.Channel {
			case "tencent":
				key, err := materialCheckpoint(q, "authorize")
				if err != nil {
					return err
				}
				g, err := loadGrant(ctx, materials, key)
				if err != nil {
					return err
				}
				if q.Checkpoints["upload"] != g.Ref {
					return fail("checkpoint_mismatch", "not_sent")
				}
				accepted, err = c.TencentUpdate(ctx, q.OperationID, TencentUpdateInput{PackageName: t.PackageName, AppID: t.AppID, Serial: g.Ref, MD5: t.APK.MD5(), Notes: t.Notes, ABI: t.ABI, Policy: t.Policy, Patch: t.Patch})
				if err != nil {
					return err
				}
			case "huawei":
				key, err := materialCheckpoint(q, "bind")
				if err != nil {
					return err
				}
				g, err := loadGrant(ctx, materials, key)
				if err != nil {
					return err
				}
				if g.ReadyAt.IsZero() || c.now().Before(g.ReadyAt) {
					return fail("package_processing_pending", "not_sent")
				}
				if q.Checkpoints["metadata"] != ack(protocol.Digest([]string{q.IntentDigest, q.Channel, "metadata"})).Ref {
					return fail("checkpoint_missing", "not_sent")
				}
				accepted, err = c.HuaweiSubmit(ctx, q.OperationID, t.AppID, scheduledAt(t.Policy))
				if err != nil {
					return err
				}
			case "honor":
				if q.Checkpoints["bind"] == "" || q.Checkpoints["metadata"] != ack(protocol.Digest([]string{q.IntentDigest, q.Channel, "metadata"})).Ref {
					return fail("checkpoint_missing", "not_sent")
				}
				accepted, err = c.HonorSubmit(ctx, q.OperationID, t.AppID, scheduledAt(t.Policy))
				if err != nil {
					return err
				}
			case "oppo":
				key, err := materialCheckpoint(q, "upload")
				if err != nil {
					return err
				}
				var f OPPOFile
				if err = loadMaterial(ctx, materials, key, &f); err != nil {
					return err
				}
				patch := map[string]string{"update_desc": t.Notes}
				for k, p := range t.Patch {
					patch[k] = p.Value
				}
				typ, at := onlinePolicy(t.Policy)
				accepted, err = c.OPPOUpdate(ctx, q.OperationID, OPPOUpdateInput{PackageName: t.PackageName, VersionCode: t.VersionCode, Existing: t.Existing, File: f, Patch: patch, OnlineType: typ, ScheduledAt: at})
				if err != nil {
					return err
				}
			case "vivo":
				key, err := materialCheckpoint(q, "upload")
				if err != nil {
					return err
				}
				var f VivoFile
				if err = loadMaterial(ctx, materials, key, &f); err != nil {
					return err
				}
				patch := map[string]string{"updateDesc": t.Notes}
				for k, p := range t.Patch {
					patch[k] = p.Value
				}
				typ, at := onlinePolicy(t.Policy)
				accepted, err = c.VivoUpdate(ctx, q.OperationID, VivoUpdateInput{PackageName: t.PackageName, VersionCode: t.VersionCode, Existing: t.Existing, File: f, Patch: patch, OnlineType: typ, ScheduledAt: at, CompatibleDevice: t.CompatibleDevice})
				if err != nil {
					return err
				}
			}
			ref = accepted.Ref
			return nil
		}
		return fail("unsupported_operation", "not_sent")
	})
	if err != nil {
		return protocol.Receipt{}, err
	}
	if ref == "" {
		return protocol.Receipt{}, unknown()
	}
	return protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: ref}, nil
}

func scheduledAt(p manifest.Policy) *time.Time {
	if p.Mode != "scheduled" {
		return nil
	}
	at, err := time.Parse(time.RFC3339, p.At)
	if err != nil {
		return nil
	}
	return &at
}
func onlinePolicy(p manifest.Policy) (int, time.Time) {
	if at := scheduledAt(p); at != nil {
		return 2, *at
	}
	return 1, time.Time{}
}

func materialCheckpoint(q protocol.Request, kind string) (string, error) {
	operation := protocol.Digest([]string{q.IntentDigest, q.Channel, kind})
	expected := "material-" + account.Key(operation)
	if q.Channel == "huawei" && kind == "authorize" {
		for generation := 0; generation < 3; generation++ {
			candidate := protocol.Request{OperationID: operation, AuthorizationGeneration: generation}.MaterialOperationKey()
			if q.Checkpoints[kind] == "material-"+account.Key(candidate) {
				return q.Checkpoints[kind], nil
			}
		}
	}
	if q.Checkpoints[kind] != expected {
		return "", fail("checkpoint_mismatch", "not_sent")
	}
	return expected, nil
}
func flattenLanguage(metadata map[string]string, s LocalizationSnapshot) {
	metadata["lang_id"] = s.LanguageID
	metadata["lang_app_name"] = s.AppName
	metadata["lang_intro"] = s.Intro
	metadata["lang_brief"] = s.BriefIntro
	metadata["lang_notes"] = s.Notes
}
func boundLanguage(t Target) (LocalizationSnapshot, error) {
	if t.Metadata["lang_id"] != t.LanguageID {
		return LocalizationSnapshot{}, fail("frozen_language_mismatch", "not_sent")
	}
	for _, field := range []string{"lang_app_name", "lang_intro", "lang_brief", "lang_notes"} {
		if _, ok := t.Metadata[field]; !ok {
			return LocalizationSnapshot{}, fail("language_snapshot_incomplete", "not_sent")
		}
	}
	return NewLocalizationSnapshot(t.Account.Channel, t.AppID, t.LanguageID, t.Metadata["lang_app_name"], t.Metadata["lang_intro"], t.Metadata["lang_brief"], t.Metadata["lang_notes"])
}

// ExpectedSnapshot is used only after a verified operation receipt. It cannot
// query a newer baseline or rewrite the immutable metadata used by the request.
func (a *Adapter) ExpectedSnapshot(q protocol.Request, before protocol.RemoteSnapshot) (protocol.RemoteSnapshot, error) {
	t, err := a.target(q.Scope)
	if err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	if err = a.match(t, q, true); err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	if before.Scope != q.Scope || before.AdapterBinding != adapterBinding(t) {
		return protocol.RemoteSnapshot{}, fail("unexpected_snapshot_transition", "not_sent")
	}
	if q.Kind != "metadata" {
		return before, nil
	}
	if (t.Account.Channel != "huawei" && t.Account.Channel != "honor") || protocol.Digest(before.Metadata) != protocol.Digest(t.Metadata) {
		return protocol.RemoteSnapshot{}, fail("unexpected_snapshot_transition", "not_sent")
	}
	out := protocol.RemoteSnapshot{Scope: before.Scope, AdapterBinding: before.AdapterBinding, Metadata: copyFields(before.Metadata), ActiveIntent: before.ActiveIntent}
	out.Metadata["lang_notes"] = t.Notes
	return out, nil
}

// No vendor API currently proves the exact immutable request after a lost
// response. Version equality, APK MD5 and current audit slots are insufficient.
func (a *Adapter) Reconcile(_ context.Context, q protocol.Request) (protocol.Reconciliation, error) {
	t, err := a.target(q.Scope)
	if err != nil {
		return protocol.Reconciliation{}, err
	}
	if err = a.match(t, q, true); err != nil {
		return protocol.Reconciliation{}, err
	}
	return protocol.Reconciliation{OperationID: q.OperationID, RequestDigest: protocol.Digest(q), Result: "inconclusive"}, nil
}
func (a *Adapter) Observe(ctx context.Context, scope string, version int64, ref string) (protocol.Observation, error) {
	t, err := a.target(scope)
	if err != nil {
		return protocol.Observation{}, err
	}
	if version != t.VersionCode || ref == "" {
		return protocol.Observation{}, fail("target_mismatch", "not_sent")
	}
	if t.Account.Channel == "xiaomi" || t.Account.Channel == "tencent" {
		return a.observeAutomaticContext(ctx, t, ref)
	}
	s := Status{Review: "unknown", Availability: "unknown"}
	at := time.Now().UTC()
	err = a.runtime.WithClient(ctx, t, func(c *Client, _ Materials) error {
		at = c.now()
		var err error
		switch t.Account.Channel {
		case "huawei":
			s, err = c.HuaweiStatus(ctx, t.AppID, version)
		case "honor":
			s, err = c.HonorAudit(ctx, t.AppID, ref, version)
		case "oppo":
			s, err = c.OPPOAudit(ctx, t.PackageName, version, t.APK.MD5())
		case "vivo":
			s, err = c.VivoStatus(ctx, t.PackageName, version)
		}
		return err
	})
	if err != nil {
		return protocol.Observation{}, err
	}
	// A local acknowledgement is only a known-submit checkpoint. It can bind
	// subsequent exact-version observations, but cannot settle a lost request.
	knownAck := manifest.ValidHash(t.IntentDigest) && ref == ack(protocol.Digest([]string{t.IntentDigest, t.Account.Channel, "submit"})).Ref
	if s.VersionCode != version || (s.Ref != ref && !knownAck) {
		s.Review = "unknown"
		s.Availability = "unknown"
	}
	if !slices.Contains([]string{"unknown", "pending", "in_review", "rejected", "approved", "withdrawn"}, s.Review) {
		s.Review = "unknown"
	}
	if !slices.Contains([]string{"unknown", "not_observed_live", "scheduled", "live", "superseded"}, s.Availability) {
		s.Availability = "unknown"
	}
	if s.Availability == "live" && s.Review != "approved" {
		s.Availability = "unknown"
	}
	source := t.Account.Channel + "_api"
	if knownAck && s.Ref != ref {
		source += "_exact_version_after_ack"
	}
	return protocol.Observation{Scope: scope, AdapterBinding: adapterBinding(t), VersionCode: version, RemoteRef: ref, Review: s.Review, Availability: s.Availability, Source: source, CollectedAt: at}, nil
}

// An adapter rebuilt after restart must use the same public publishing inputs.
// Secret values and replaceable credential references are intentionally absent.
func adapterBinding(t Target) string {
	t = cloneTarget(t)
	file := func(f File) any {
		return struct {
			Name, SHA256, MD5 string
			Size              int64
		}{f.Name(), f.SHA256(), f.MD5(), f.Size()}
	}
	abis := slices.Clone(t.ABI)
	slices.Sort(abis)
	abis = slices.Compact(abis)
	binding := map[string]any{"scope": t.Scope, "intent": t.IntentDigest, "account": t.Account, "credential_id": t.Credential.ID,
		"app_id": t.AppID, "package": t.PackageName, "version_code": t.VersionCode, "version_name": t.VersionName,
		"apk": file(t.APK), "icon": file(t.Icon), "abi": abis, "notes": t.Notes, "policy": t.Policy, "metadata": t.Metadata, "patch": t.Patch, "existing": t.Existing,
		"language": t.LanguageID, "compatible_device": t.CompatibleDevice, "mainland": t.Mainland}
	if t.Account.Channel == "oppo" {
		binding["copyright_contract"] = "existing-or-electronic-v1"
	}
	return protocol.Digest(binding)
}

var _ protocol.Adapter = (*Adapter)(nil)

func (a *Adapter) AdapterBinding(scope string) (string, error) {
	t, err := a.target(scope)
	if err != nil {
		return "", err
	}
	return adapterBinding(t), nil
}

var _ protocol.Planner = (*Adapter)(nil)
var _ protocol.PreflightAdvancer = (*Adapter)(nil)
