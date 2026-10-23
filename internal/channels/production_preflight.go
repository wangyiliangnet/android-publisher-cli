package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// PreparationConfig is private executor configuration. It carries references
// and public IDs, never API secrets, private keys or server response material.
type PreparationConfig struct {
	AccountQuery
	IconPath   string `json:"icon_path,omitempty"`
	PrivacyURL string `json:"privacy_url,omitempty"`
}
type PreparationInput struct {
	Snapshot       manifest.Snapshot
	Scope, APKPath string
	Config         PreparationConfig
}
type PreparationError struct{ Missing []string }

func (e *PreparationError) Error() string {
	return "required preparation input missing: " + strings.Join(e.Missing, ", ")
}

// PreparedProduction holds a private, immutable preparation baseline. The
// preparation constructor remains read-only; NewFiveProduction explicitly opts
// one reviewed frozen scope into the production executor.
type PreparedProduction struct {
	base                 *Adapter
	runtime              Runtime
	reader               Runtime
	target               Target
	config               PreparationConfig
	ref                  string
	enabled              bool
	writable             bool
	diagnostic           *FiveWriteDiagnostic
	inspectionDiagnostic *FiveInspectionDiagnostic
}

func (p *PreparedProduction) ProductionScope() (string, string, bool) {
	return p.target.Scope, p.target.IntentDigest, p.enabled && p.writable
}
func (p *PreparedProduction) BaselineRef() string { return p.ref }
func (p *PreparedProduction) AdapterBinding(scope string) (string, error) {
	return p.base.AdapterBinding(scope)
}
func (p *PreparedProduction) publicMetadata() map[string]string {
	return map[string]string{"existing_baseline_ref": p.ref}
}

func readPreparation(ctx context.Context, c *Client, q AccountQuery) (Details, error) {
	d, err := readAccount(ctx, c, q)
	if err != nil {
		return d, err
	}
	if q.Channel == "huawei" || q.Channel == "honor" {
		var o object
		var rows []object
		var langKey, nameKey, introKey, briefKey, notesKey string
		if q.Channel == "huawei" {
			o, err = c.huaweiJSON(ctx, "/api/publish/v2/app-info?"+url.Values{"appId": {d.AppID}}.Encode(), "GET", transport.Read, "", nil)
			rows = o.list("languages")
			langKey, nameKey, introKey, briefKey, notesKey = "lang", "appName", "appDesc", "briefInfo", "newFeatures"
		} else {
			o, err = c.honorJSON(ctx, "/openapi/v1/publish/get-app-detail?"+url.Values{"appId": {d.AppID}}.Encode(), "GET", transport.Read, "", nil)
			rows = o.obj("data").list("languageInfo")
			langKey, nameKey, introKey, briefKey, notesKey = "languageId", "appName", "intro", "briefIntro", "newFeature"
		}
		if err != nil {
			return d, err
		}
		lang := q.LanguageID
		if lang == "" {
			for _, row := range rows {
				candidate := row.str(langKey)
				if strings.EqualFold(strings.ReplaceAll(candidate, "_", "-"), "zh-CN") {
					if lang != "" {
						return d, fail("chinese_language_ambiguous", "not_sent")
					}
					lang = candidate
				}
			}
		}
		if lang == "" {
			return d, &PreparationError{Missing: []string{"language_id"}}
		}
		localization, err := localizationRow(q.Channel, d.AppID, lang, rows, langKey, nameKey, introKey, briefKey, notesKey)
		if err != nil {
			return d, err
		}
		flattenLanguage(d.Metadata, localization)
	}
	return d, nil
}

func preparationTarget(in PreparationInput, d Details, file File) (Target, error) {
	m := in.Snapshot.Manifest
	q := in.Config.AccountQuery
	if d.PackageName != m.PackageName || (q.AppID != "" && d.AppID != q.AppID) || !account.ValidName(d.AppID) || d.VersionCode <= 0 || d.VersionCode >= m.VersionCode {
		return Target{}, fail("remote_identity_or_version_not_verified", "not_sent")
	}
	t := q.target()
	t.Scope = in.Scope
	t.IntentDigest = in.Snapshot.Digest
	t.VersionCode = m.VersionCode
	t.VersionName = m.VersionName
	t.APK = file
	t.ABI = m.Artifacts[m.Targets[q.Channel].Artifact].ABI
	t.Notes = in.Snapshot.Notes
	t.Policy = m.ReleasePolicy
	t.Existing = d
	t.AppID = d.AppID
	t.Metadata = normalizeMetadata(q.Channel, d.Metadata)
	switch q.Channel {
	case "huawei", "honor":
		t.LanguageID = d.Metadata["lang_id"]
	case "vivo":
		t.CompatibleDevice, _ = strconv.Atoi(d.Metadata["compatibleDevice"])
	case "xiaomi":
		var missing []string
		if in.Config.IconPath == "" {
			missing = append(missing, "icon_path")
		}
		if in.Config.PrivacyURL == "" {
			missing = append(missing, "privacy_url")
		}
		if len(missing) > 0 {
			return Target{}, &PreparationError{Missing: missing}
		}
		icon, err := NewFile(in.Config.IconPath)
		if err != nil {
			return Target{}, fail("icon_unavailable_or_changed", "not_sent")
		}
		t.Icon = icon
		u, err := url.Parse(in.Config.PrivacyURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return Target{}, fail("invalid_privacy_url", "not_sent")
		}
		t.Metadata["privacy_url"] = in.Config.PrivacyURL
	}
	return t, nil
}

func (r Runtime) NewPreparedProduction(ctx context.Context, in PreparationInput, ref string) (*PreparedProduction, error) {
	s := in.Snapshot
	m := s.Manifest
	q := in.Config.AccountQuery
	if s.Verify(manifest.Production) != nil || len(m.Targets) != 1 || len(m.Metadata) != 0 || m.ReleasePolicy.Mode != "after_approval" || !q.valid() || in.Scope == "" {
		return nil, fail("unsupported_preparation_scope", "not_sent")
	}
	target, ok := m.Targets[q.Channel]
	if !ok || target.AccountRef != q.Account || target.AppRef != m.PackageName || q.PackageName != m.PackageName {
		return nil, fail("frozen_preparation_scope_mismatch", "not_sent")
	}
	file, err := NewNamedFile(in.APKPath, q.Channel+".apk")
	if err != nil {
		return nil, err
	}
	if file.SHA256() != m.Artifacts[target.Artifact].SHA256 {
		return nil, fail("frozen_artifact_mismatch", "not_sent")
	}
	r = r.readOnly(q)
	var t Target
	prepare := func(c *Client, materials Materials) error {
		var d Details
		if ref != "" {
			if err := loadMaterial(ctx, materials, ref, &d); err != nil {
				return err
			}
		} else {
			var err error
			d, err = readPreparation(ctx, c, q)
			if err != nil {
				return err
			}
		}
		var err error
		t, err = preparationTarget(in, d, file)
		if err != nil {
			return err
		}
		// Pure validation happens before sealing and before reporting readiness.
		a, err := NewAdapter([]Target{t}, r)
		if err != nil {
			return err
		}
		request := protocol.Request{Scope: t.Scope, Channel: q.Channel, IntentDigest: t.IntentDigest, PackageName: t.PackageName, VersionCode: t.VersionCode, VersionName: t.VersionName, ArtifactSHA256: t.APK.SHA256(), Policy: t.Policy, Metadata: desiredMetadata(t), SnapshotDigest: protocol.Digest(t.Metadata), AdapterBinding: adapterBinding(t)}
		if _, err = a.BuildPlan(request); err != nil {
			return err
		}
		if ref != "" {
			return nil
		}
		key := "preparation-baseline-" + protocol.Digest(struct {
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
				return fail("baseline_invalid", "not_sent")
			}
			ref = candidate
			return nil
		}
		if !errors.Is(getErr, credentials.ErrMissing) {
			return getErr
		}
		ref, err = saveMaterial(ctx, materials, key, d)
		return err
	}
	if ref != "" {
		if r.Manager == nil {
			return nil, fail("invalid_runtime", "not_sent")
		}
		// Reopening a frozen private baseline is local. In particular a pending
		// or expired OAuth refresh must not prevent lost-response reconciliation.
		err = r.Manager.WithAccount(ctx, q.target().Account, func(session *credentials.Session) error {
			return prepare(nil, keychainMaterials{vault: r.Manager.Vault, scope: q.target().Account, lease: session.Lease()})
		})
	} else {
		err = r.WithClient(ctx, q.target(), prepare)
	}
	if err != nil {
		return nil, err
	}
	base, err := NewAdapter([]Target{t}, r)
	if err != nil {
		return nil, err
	}
	return &PreparedProduction{base: base, runtime: r, reader: r, target: t, config: in.Config, ref: ref}, nil
}

func (p *PreparedProduction) Inspect(ctx context.Context, scope string) (protocol.RemoteSnapshot, error) {
	p.inspectionDiagnostic = &FiveInspectionDiagnostic{Reason: "query_failed"}
	if scope != p.target.Scope {
		p.inspectionDiagnostic.Reason = "target_outside_frozen_scope"
		return protocol.RemoteSnapshot{}, fail("target_outside_frozen_scope", "not_sent")
	}
	metadataOut := p.publicMetadata()
	err := p.reader.WithClient(ctx, p.target, func(c *Client, materials Materials) error {
		progress, err := p.confirmedProgress(ctx, materials)
		if err != nil {
			p.inspectionDiagnostic.Reason = "progress_unavailable"
			return err
		}
		q := p.config.AccountQuery
		q.AppID, q.LanguageID = p.target.AppID, p.target.LanguageID
		d, err := readPreparation(ctx, c, q)
		if err != nil {
			return err
		}
		metadata := normalizeMetadata(p.target.Account.Channel, d.Metadata)
		if p.target.Account.Channel == "xiaomi" {
			if !d.UpdateAllowed {
				p.inspectionDiagnostic.Reason = "update_not_allowed"
				return fail("existing_update_not_allowed", "not_sent")
			}
			icon, err := NewFile(p.config.IconPath)
			if err != nil || icon.SHA256() != p.target.Icon.SHA256() {
				p.inspectionDiagnostic.Reason = "icon_unavailable_or_changed"
				return fail("icon_unavailable_or_changed", "not_sent")
			}
			metadata["privacy_url"] = p.config.PrivacyURL
		}
		expected := copyFields(p.target.Metadata)
		versionOK := d.VersionCode == p.target.Existing.VersionCode
		if progress["bind"] {
			// Huawei app-info is editable and can report the newly bound package.
			// Honor releaseInfo is the live version: own binding never permits
			// a different live version before this executor's final submission.
			if p.target.Account.Channel == "huawei" {
				versionOK = versionOK || d.VersionCode == p.target.VersionCode
			}
			if p.target.Account.Channel == "honor" {
				delete(metadata, "live_version_code")
				delete(expected, "live_version_code")
			}
			metadataOut["confirmed_bind"] = p.progressMarker("bind")
		}
		if progress["metadata"] {
			expected["lang_notes"] = p.target.Notes
			metadataOut["confirmed_metadata"] = p.progressMarker("metadata")
		}
		p.inspectionDiagnostic = &FiveInspectionDiagnostic{
			Reason:                      "preflight_changed",
			PackageMatches:              d.PackageName == p.target.PackageName,
			AppMatches:                  d.AppID == p.target.AppID,
			VersionMatches:              versionOK,
			MetadataMatches:             reflect.DeepEqual(metadata, expected),
			NotesLFMatches:              fiveInspectionNotesLFMatch(metadata, expected),
			NotesBaselineMatches:        fiveInspectionNotesBaselineMatch(metadata, p.target.Metadata),
			NotesTerminalNewlineMatches: fiveInspectionNotesTerminalNewlineMatch(metadata, expected),
			NotesEmpty:                  fiveInspectionNotesEmpty(metadata),
			ObservedVersionCode:         d.VersionCode,
			LangFields:                  fiveInspectionFields(p.target.Account.Channel, metadata, expected),
		}
		if !p.inspectionDiagnostic.PackageMatches || !p.inspectionDiagnostic.AppMatches || !p.inspectionDiagnostic.VersionMatches || !p.inspectionDiagnostic.MetadataMatches {
			verified, err := p.honorLiveBaselineDraft(ctx, c, d, metadata, progress)
			if err != nil {
				return err
			}
			if verified {
				return nil
			}
			return fail("preflight_changed", "not_sent")
		}
		p.inspectionDiagnostic.Reason = "verified"
		return nil
	})
	if err != nil {
		return protocol.RemoteSnapshot{}, err
	}
	return protocol.RemoteSnapshot{Scope: scope, AdapterBinding: adapterBinding(p.target), Metadata: metadataOut}, nil
}
func (p *PreparedProduction) translate(q protocol.Request) (protocol.Request, error) {
	expected := p.publicMetadata()
	expected["release_notes"] = p.target.Notes
	if !reflect.DeepEqual(q.Metadata, expected) || q.SnapshotDigest != protocol.Digest(p.publicMetadata()) {
		return protocol.Request{}, fail("frozen_baseline_mismatch", "not_sent")
	}
	q.Metadata = desiredMetadata(p.target)
	q.SnapshotDigest = protocol.Digest(p.target.Metadata)
	return q, nil
}
func (p *PreparedProduction) BuildPlan(q protocol.Request) ([]string, error) {
	translated, err := p.translate(q)
	if err != nil {
		return nil, err
	}
	return p.base.BuildPlan(translated)
}
