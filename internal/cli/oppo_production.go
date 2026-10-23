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

type oppoBinding struct {
	SchemaVersion  int    `json:"schema_version"`
	IntentDigest   string `json:"intent_digest"`
	Scope          string `json:"scope"`
	CredentialName string `json:"credential_name"`
	BaselineRef    string `json:"baseline_ref"`
	AdapterBinding string `json:"adapter_binding"`
}

type oppoPrepared interface {
	protocol.Adapter
	protocol.Planner
	protocol.BindingProvider
	protocol.ProductionBindingProvider
	BaselineRef() string
	CopyrightSource() string
}
type oppoFactory func(context.Context, channels.OPPOProductionInput, string, bool) (oppoPrepared, error)

func productionFailure(out io.Writer, err error) int {
	code, action, public := 3, "review_production_preflight", "production command failed"
	var credential *credentials.Error
	var vendor *channels.Error
	var metadata *channels.OPPOMetadataError
	switch {
	case errors.Is(err, credentials.ErrRefreshUnknown):
		code, action, public = 3, "reconcile_account_token", "token_refresh_unknown"
	case errors.Is(err, credentials.ErrReplacementConflict):
		code, action, public = 3, "review_token_replacement", "token_replacement_conflict"
	case errors.As(err, &credential):
		code, action, public = 4, "review_keychain_access", credential.Code
		if credential.Code == "token_refresh_rejected" {
			action = "verify_api_credentials"
		}
	case errors.As(err, &vendor):
		public = vendor.Code
	case errors.As(err, &metadata):
		report := map[string]any{"schema_version": 1, "mode": manifest.Production, "effect": "not_sent", "error": "oppo_required_metadata_missing", "missing_fields": metadata.Missing, "next_action": "complete_existing_application_metadata"}
		if len(metadata.Available) != 0 {
			report["available_alternative_fields"] = metadata.Available
			report["next_action"] = "verify_existing_copyright_field_mapping"
		}
		if encode(out, report) != nil {
			return 5
		}
		return 2
	case errors.Is(err, state.ErrRecovery), errors.Is(err, state.ErrConflict), errors.Is(err, state.ErrBusy):
		public = err.Error()
	case errors.Is(err, context.Canceled):
		code, public = 130, "canceled"
	}
	return failure(out, manifest.Production, action, code, fmt.Errorf("%s", public))
}

func runOPPOProduction(ctx context.Context, command, root, id, credentialName, approvedIntent, approvedPreflight string, interactive bool, subset []string, inspector artifact.Inspector, out io.Writer, replacement ...string) int {
	vault := credentials.NewKeychain()
	if interactive {
		var err error
		vault, err = credentials.NewInteractiveKeychain()
		if err != nil {
			return productionFailure(out, err)
		}
	}
	runtime := channels.Runtime{Manager: &credentials.Manager{Vault: vault, Accounts: account.Default()}}
	return oppoProductionCommand(ctx, command, root, id, credentialName, approvedIntent, approvedPreflight, subset, inspector, func(ctx context.Context, in channels.OPPOProductionInput, ref string, writable bool) (oppoPrepared, error) {
		return runtime.NewOPPOProduction(ctx, in, ref, writable)
	}, out, replacement...)
}

func oppoProductionCommand(ctx context.Context, command, root, id, credentialName, approvedIntent, approvedPreflight string, subset []string, inspector artifact.Inspector, factory oppoFactory, out io.Writer, replacement ...string) int {
	bad := func(message string) int {
		return failure(out, manifest.Production, "fix_config", 2, fmt.Errorf("%s", message))
	}
	write := command == "submit" || command == "resume"
	replace := ""
	if len(replacement) > 1 {
		return bad("invalid authorization replacement")
	}
	if len(replacement) == 1 {
		replace = replacement[0]
	}
	if replace != "" && (command != "resume" || !manifest.ValidHash(replace)) {
		return bad("authorization replacement requires resume and an exact operation ID")
	}
	if !manifest.ValidID(id) || (command != "preflight" && command != "submit" && command != "resume" && command != "reconcile" && command != "status") || (len(subset) > 0 && (len(subset) != 1 || subset[0] != "oppo")) || (!write && (approvedIntent != "" || approvedPreflight != "")) {
		return bad("production supports one frozen OPPO release only; approval flags are for submit/resume")
	}
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		return productionFailure(out, err)
	}
	defer s.Close()
	snapshot, err := s.Load(id)
	if err != nil {
		return bad("frozen production release not found")
	}
	m := snapshot.Manifest
	t, ok := m.Targets["oppo"]
	if len(m.Targets) != 1 || !ok || len(m.Metadata) != 0 || m.ReleasePolicy.Mode != "after_approval" {
		return bad("production supports OPPO after_approval updates without additional metadata patches")
	}
	targets, err := s.Targets(id)
	if err != nil || len(targets) != 1 {
		return bad("invalid frozen target inventory")
	}
	target := targets[0]
	if command == "preflight" || write {
		if replace != "" {
			_, err = s.CheckOPPOAuthorizationReplacement(target.ID, replace)
		} else {
			err = s.CheckWritable(target.Scope, target.ID)
		}
		if err != nil {
			return productionFailure(out, err)
		}
	}
	path := filepath.Join(root, "production", "oppo", id+".json")
	var binding oppoBinding
	data, readErr := privatefs.Read(path, 8192)
	if readErr == nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF || binding.SchemaVersion != 1 || binding.IntentDigest != snapshot.Digest || binding.Scope != target.Scope || !manifest.ValidHash(binding.AdapterBinding) || !credentials.ValidCredentialName(binding.CredentialName) || len(binding.BaselineRef) != 73 {
			return bad("invalid saved OPPO preflight binding")
		}
	} else if !os.IsNotExist(readErr) || command != "preflight" {
		return bad("OPPO preflight is required before this command")
	}
	if write && (approvedIntent != snapshot.Digest || approvedPreflight != binding.AdapterBinding) {
		return bad("submit/resume requires --approve-intent and --approve-preflight matching the reviewed preflight")
	}
	if command == "preflight" {
		status, err := (&engine.Engine{Store: s}).Status(id)
		if err != nil || len(status.Operations) != 0 {
			return bad("preflight cannot replace a baseline after a business intent; use reconcile/status")
		}
	}
	if credentialName == "" {
		credentialName = binding.CredentialName
		if credentialName == "" {
			credentialName = "client-secret"
		}
	}
	if !credentials.ValidCredentialName(credentialName) {
		return bad("invalid credential reference")
	}
	if err = artifact.VerifyFrozen(ctx, root, snapshot, inspector); err != nil {
		return bad("frozen APK verification failed")
	}
	ref := binding.BaselineRef
	if command == "preflight" {
		ref = ""
	}
	prepared, err := factory(ctx, channels.OPPOProductionInput{Snapshot: snapshot, Scope: target.Scope, APKPath: artifact.Path(root, m.Artifacts[t.Artifact].SHA256), CredentialName: credentialName}, ref, write)
	if err != nil {
		return productionFailure(out, err)
	}
	adapterBinding, err := prepared.AdapterBinding(target.Scope)
	if err != nil {
		return productionFailure(out, err)
	}
	if command != "preflight" && adapterBinding != binding.AdapterBinding {
		return bad("saved preflight does not match the current adapter configuration")
	}
	e := engine.Engine{Store: s, Inspector: inspector, Adapter: prepared}
	var result engine.Summary
	switch command {
	case "preflight":
		result, err = e.Preflight(ctx, id, subset)
		if err == nil {
			binding = oppoBinding{SchemaVersion: 1, IntentDigest: snapshot.Digest, Scope: target.Scope, CredentialName: credentialName, BaselineRef: prepared.BaselineRef(), AdapterBinding: adapterBinding}
			b, _ := json.Marshal(binding)
			if err = privatefs.Dir(filepath.Join(root, "production")); err == nil {
				err = privatefs.Write(path, b)
			}
			if err != nil {
				return productionFailure(out, err)
			}
		}
	case "submit", "resume":
		if replace != "" {
			err = e.ReplaceUnusedOPPOAuthorization(ctx, id, replace)
		}
		if err == nil {
			result, err = e.Submit(ctx, id, subset)
		} else {
			result, _ = e.Status(id)
			result.Errors = map[string]string{"oppo": "authorization replacement did not complete; inspect operation history and private response diagnostics"}
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
		PublishingPermission string                        `json:"publishing_permission"`
		ApprovalPreflight    string                        `json:"approval_preflight"`
		CopyrightProof       map[string]string             `json:"copyright_proof"`
		LastWriteDiagnostic  *channels.OPPOWriteDiagnostic `json:"last_write_diagnostic,omitempty"`
	}{result, "not_verified", adapterBinding, map[string]string{"source_field": prepared.CopyrightSource(), "request_field": "copyright_url"}, nil}
	if diagnostic, ok := prepared.(interface {
		LastWriteDiagnostic() *channels.OPPOWriteDiagnostic
	}); ok {
		response.LastWriteDiagnostic = diagnostic.LastWriteDiagnostic()
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
