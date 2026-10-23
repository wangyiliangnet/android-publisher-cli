package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type fiveBinding struct {
	SchemaVersion         int    `json:"schema_version"`
	IntentDigest          string `json:"intent_digest"`
	Scope                 string `json:"scope"`
	BaselineRef           string `json:"baseline_ref"`
	AdapterBinding        string `json:"adapter_binding"`
	BusinessWritesEnabled bool   `json:"business_writes_enabled"`
}

type fivePrepared interface {
	protocol.Adapter
	protocol.Planner
	protocol.BindingProvider
	protocol.ProductionBindingProvider
	BaselineRef() string
}

type fiveFactory func(context.Context, channels.PreparationInput, string, bool) (fivePrepared, error)

func runFiveProduction(ctx context.Context, command, root, id, configFile, approvedIntent, approvedPreflight string, interactive bool, subset []string, inspector artifact.Inspector, out io.Writer, renewal ...string) int {
	vault := credentials.NewKeychain()
	if interactive {
		var err error
		vault, err = credentials.NewInteractiveKeychain()
		if err != nil {
			return productionFailure(out, err)
		}
	}
	runtime := channels.Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Default()}}
	return fiveProductionCommandObserved(ctx, command, root, id, configFile, approvedIntent, approvedPreflight, subset, inspector, func(ctx context.Context, in channels.PreparationInput, ref string, writable bool) (fivePrepared, error) {
		return runtime.NewFiveProduction(ctx, in, ref, writable)
	}, channels.TencentPublicContext, out, renewal...)
}

// decodeFiveConfiguration accepts exactly one flat object with canonical JSON
// field spelling. encoding/json also accepts case-insensitive field aliases;
// reject them before decoding so an identity or digest cannot be shadowed.
func decodeFiveConfiguration(data []byte, value any) error {
	var allowed []string
	switch value.(type) {
	case *channels.PreparationConfig:
		allowed = []string{"channel", "account", "client_id", "credential_name", "public_key_name", "package_name", "app_id", "language_id", "icon_path", "privacy_url"}
	case *fiveBinding:
		allowed = []string{"schema_version", "intent_digest", "scope", "baseline_ref", "adapter_binding", "business_writes_enabled"}
	default:
		return fmt.Errorf("invalid private configuration")
	}
	check := json.NewDecoder(bytes.NewReader(data))
	open, err := check.Token()
	if err != nil || open != json.Delim('{') {
		return fmt.Errorf("invalid private configuration")
	}
	seen := map[string]bool{}
	for check.More() {
		token, err := check.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(allowed, key) || seen[key] {
			return fmt.Errorf("invalid private configuration")
		}
		seen[key] = true
		var scalar any
		if check.Decode(&scalar) != nil {
			return fmt.Errorf("invalid private configuration")
		}
		switch scalar.(type) {
		case string, bool, float64:
		default:
			return fmt.Errorf("invalid private configuration")
		}
	}
	if end, err := check.Token(); err != nil || end != json.Delim('}') {
		return fmt.Errorf("invalid private configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid private configuration")
	}
	return nil
}

func fiveProductionCommand(ctx context.Context, command, root, id, configFile, approvedIntent, approvedPreflight string, subset []string, inspector artifact.Inspector, factory fiveFactory, out io.Writer, renewal ...string) int {
	return fiveProductionCommandObserved(ctx, command, root, id, configFile, approvedIntent, approvedPreflight, subset, inspector, factory, nil, out, renewal...)
}

func fiveProductionCommandObserved(ctx context.Context, command, root, id, configFile, approvedIntent, approvedPreflight string, subset []string, inspector artifact.Inspector, factory fiveFactory, publicReader publicContextReader, out io.Writer, renewal ...string) int {
	bad := func(message string) int {
		return failure(out, manifest.Production, "fix_config", 2, fmt.Errorf("%s", message))
	}
	write := command == "submit" || command == "resume"
	renew := ""
	if len(renewal) > 1 {
		return bad("invalid authorization renewal")
	}
	if len(renewal) == 1 {
		renew = renewal[0]
	}
	if renew != "" && (command != "resume" || !manifest.ValidHash(renew)) {
		return bad("authorization renewal requires resume and an exact operation ID")
	}
	if !manifest.ValidID(id) || !slices.Contains([]string{"preflight", "submit", "resume", "reconcile", "status"}, command) || (!write && (approvedIntent != "" || approvedPreflight != "")) {
		return bad("production supports one frozen release; approval flags are for submit/resume")
	}
	data, err := privatefs.Read(configFile, 65536)
	if err != nil {
		return bad("invalid private account configuration or frozen release")
	}
	defer clear(data)
	var config channels.PreparationConfig
	if decodeFiveConfiguration(data, &config) != nil {
		return bad("invalid private account configuration or frozen release")
	}
	q := config.AccountQuery
	if renew != "" && q.Channel != "huawei" {
		return bad("authorization renewal supports one frozen Huawei release only")
	}
	if !slices.Contains([]string{"huawei", "honor", "xiaomi", "vivo", "tencent"}, q.Channel) || !account.ValidName(q.Account) || !credentials.ValidCredentialName(q.SecretName) || (len(subset) > 0 && (len(subset) != 1 || subset[0] != q.Channel)) {
		return bad("invalid private account configuration or frozen release")
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		return productionFailure(out, err)
	}
	defer s.Close()
	snapshot, err := s.Load(id)
	if err != nil {
		return bad("invalid private account configuration or frozen release")
	}
	m := snapshot.Manifest
	t, ok := m.Targets[q.Channel]
	if !ok || len(m.Targets) != 1 || t.AccountRef != q.Account || t.AppRef != m.PackageName || m.PackageName != q.PackageName || len(m.Metadata) != 0 || m.ReleasePolicy.Mode != "after_approval" || m.ReleasePolicy.At != "" {
		return bad("invalid private account configuration or frozen release")
	}
	if q.Channel == "tencent" && q.AppID == "" {
		return fivePreparationFailure(out, &channels.PreparationError{Missing: []string{"app_id"}})
	}
	targets, err := s.Targets(id)
	if err != nil || len(targets) != 1 {
		return bad("invalid frozen target inventory")
	}
	target := targets[0]
	if command == "preflight" || write {
		if err = s.CheckWritable(target.Scope, target.ID); err != nil {
			return productionFailure(out, err)
		}
	}
	if command == "preflight" {
		status, err := (&engine.Engine{Store: s}).Status(id)
		if err != nil || len(status.Operations) != 0 {
			return bad("preflight cannot replace a baseline after a business intent; use reconcile/status")
		}
	}
	path := filepath.Join(root, "production", q.Channel, id+".json")
	var binding fiveBinding
	stored, readErr := privatefs.Read(path, 8192)
	if readErr == nil {
		defer clear(stored)
		if decodeFiveConfiguration(stored, &binding) != nil || binding.SchemaVersion != 1 || binding.IntentDigest != snapshot.Digest || binding.Scope != target.Scope || !manifest.ValidHash(binding.AdapterBinding) || !strings.HasPrefix(binding.BaselineRef, "material-") || !manifest.ValidHash(strings.TrimPrefix(binding.BaselineRef, "material-")) {
			return bad("invalid saved production preflight binding")
		}
	} else if !os.IsNotExist(readErr) || command != "preflight" {
		return bad("production preflight is required before this command")
	}
	// The legacy business_writes_enabled field describes the command that
	// saved the preflight. It cannot grant execution or replace approvals.
	if write && (approvedIntent != snapshot.Digest || approvedPreflight != binding.AdapterBinding) {
		return bad("submit/resume requires --approve-intent and --approve-preflight matching the reviewed preflight")
	}
	if artifact.VerifyFrozen(ctx, root, snapshot, inspector) != nil {
		return bad("frozen APK verification failed")
	}
	ref := binding.BaselineRef
	if command == "preflight" {
		ref = ""
	}
	prepared, err := factory(ctx, channels.PreparationInput{Snapshot: snapshot, Scope: target.Scope, APKPath: artifact.Path(root, m.Artifacts[t.Artifact].SHA256), Config: config}, ref, write)
	if err != nil {
		if command == "status" && q.Channel == "tencent" && publicReader != nil && target.RemoteRef != "" {
			return tencentPublicFallback(ctx, s, snapshot, target, binding.AdapterBinding, publicReader, out)
		}
		return fivePreparationFailure(out, err)
	}
	adapterBinding, err := prepared.AdapterBinding(target.Scope)
	if err != nil {
		return productionFailure(out, err)
	}
	if !manifest.ValidHash(adapterBinding) || (command != "preflight" && adapterBinding != binding.AdapterBinding) {
		return bad("saved preflight does not match the current adapter configuration")
	}
	e := engine.Engine{Store: s, Inspector: inspector, Adapter: prepared}
	var result engine.Summary
	switch command {
	case "preflight":
		result, err = e.Preflight(ctx, id, subset)
		if err == nil {
			binding = fiveBinding{SchemaVersion: 1, IntentDigest: snapshot.Digest, Scope: target.Scope, BaselineRef: prepared.BaselineRef(), AdapterBinding: adapterBinding}
			b, _ := json.Marshal(binding)
			if err = privatefs.Dir(filepath.Join(root, "production")); err == nil {
				err = privatefs.Write(path, b)
			}
			if err != nil {
				return productionFailure(out, err)
			}
		}
	case "submit", "resume":
		if renew != "" {
			err = e.RenewExpiredHuaweiAuthorization(ctx, id, renew)
		}
		if err == nil {
			result, err = e.Submit(ctx, id, subset)
		} else {
			result, _ = e.Status(id)
			result.Errors = map[string]string{q.Channel: "upload authorization renewal did not complete; inspect operation history and private response diagnostics"}
		}
	case "reconcile":
		result, err = e.Reconcile(ctx, id, subset)
	case "status":
		result, err = e.Refresh(ctx, id, subset)
	}
	if result.ReleaseID == "" {
		return productionFailure(out, err)
	}
	response := struct {
		engine.Summary
		PublishingPermission     string                             `json:"publishing_permission"`
		BusinessWritesEnabled    bool                               `json:"business_writes_enabled"`
		ApprovalPreflight        string                             `json:"approval_preflight"`
		LastWriteDiagnostic      *channels.FiveWriteDiagnostic      `json:"last_write_diagnostic,omitempty"`
		LastInspectionDiagnostic *channels.FiveInspectionDiagnostic `json:"last_inspection_diagnostic,omitempty"`
	}{result, "not_verified", write, adapterBinding, nil, nil}
	if diagnostic, ok := prepared.(interface {
		LastWriteDiagnostic() *channels.FiveWriteDiagnostic
	}); ok {
		response.LastWriteDiagnostic = diagnostic.LastWriteDiagnostic()
	}
	if diagnostic, ok := prepared.(interface {
		LastInspectionDiagnostic() *channels.FiveInspectionDiagnostic
	}); ok {
		response.LastInspectionDiagnostic = diagnostic.LastInspectionDiagnostic()
	}
	if result.AllAccepted {
		response.PublishingPermission = "verified_for_this_release"
	}
	if encode(out, response) != nil {
		return 5
	}
	if err != nil {
		if ctx.Err() != nil {
			return 130
		}
		return 3
	}
	return 0
}

func fivePreparationFailure(out io.Writer, err error) int {
	var missing *channels.PreparationError
	if errors.As(err, &missing) {
		if encode(out, map[string]any{"schema_version": 1, "mode": manifest.Production, "effect": "not_sent", "error": "preparation_inputs_missing", "missing_fields": missing.Missing}) != nil {
			return 5
		}
		return 2
	}
	return productionFailure(out, err)
}
