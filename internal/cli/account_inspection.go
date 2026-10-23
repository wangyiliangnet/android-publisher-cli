package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

type accountInspector interface {
	InspectOPPO(context.Context, string, string, string) (channels.AccountInspection, error)
}

func runAccount(ctx context.Context, mode string, args []string, tools artifact.AndroidTools, out, stderr io.Writer) int {
	runtime := channels.Runtime{Manager: &credentials.Manager{Vault: credentials.NewKeychain(), Accounts: account.Default()}}
	return accountCommand(ctx, mode, args, tools, runtime, out, stderr)
}

func accountCommand(ctx context.Context, mode string, args []string, inspector artifact.Inspector, remote accountInspector, out, stderr io.Writer) int {
	bad := func() int {
		return failure(out, mode, "fix_config", 2, fmt.Errorf("account inspect requires a supported channel, account and exactly one of --package or --apk; non-OPPO channels require their public client identity"))
	}
	if mode != manifest.Production || len(args) == 0 || args[0] != "inspect" {
		return bad()
	}
	f := flags("account inspect", stderr)
	channel := f.String("channel", "", "supported channel; no publishing data writes")
	clientID := f.String("account", "", "canonical local account key; OPPO uses its client ID")
	name := f.String("credential-name", "client-secret", "Keychain credential reference")
	pkg := f.String("package", "", "exact existing application package name")
	apk := f.String("apk", "", "local APK to verify and derive the package name; never uploaded")
	apiID := f.String("client-id", "", "public API client ID, user name, access key or uid; not a secret")
	publicKey := f.String("public-key-name", "", "Xiaomi public certificate Keychain reference")
	appID := f.String("app-id", "", "exact app ID; required for Tencent")
	interactive := f.Bool("keychain-interactive", false, "allow foreground macOS item authorization")
	replaceToken := f.String("replace-unknown-token", "", "Honor only: exact reviewed pending-token SHA-256 for one audited new grant")
	if parse(f, args[1:]) != nil || !account.ValidName(*clientID) || !credentials.ValidCredentialName(*name) || (*pkg == "") == (*apk == "") || (*channel != "oppo" && *channel != "huawei" && *channel != "honor" && *channel != "xiaomi" && *channel != "vivo" && *channel != "tencent") || (*channel != "oppo" && *channel != "huawei" && *apiID == "") || (*channel == "xiaomi" && !credentials.ValidCredentialName(*publicKey)) || (*channel == "tencent" && !account.ValidName(*appID)) || (*channel == "oppo" && (*apiID != "" || *publicKey != "" || *appID != "")) {
		return bad()
	}
	if *replaceToken != "" && (*channel != "honor" || !manifest.ValidHash(*replaceToken)) {
		return bad()
	}
	var local *artifact.Info
	var hash string
	if *apk != "" {
		before, _, err := privatefs.Hash(*apk)
		if err != nil {
			return failure(out, mode, "verify_local_apk", 2, fmt.Errorf("local APK unavailable"))
		}
		info, err := inspector.Inspect(ctx, *apk)
		if err != nil {
			return failure(out, mode, "verify_local_apk", 2, fmt.Errorf("local APK verification failed"))
		}
		after, _, err := privatefs.Hash(*apk)
		if err != nil || before != after {
			return failure(out, mode, "verify_local_apk", 2, fmt.Errorf("local APK changed during verification"))
		}
		*pkg, hash, local = info.PackageName, after, &info
	}
	if !channels.ValidInspectionPackage(*pkg) {
		return bad()
	}
	if *interactive {
		r, ok := remote.(channels.Runtime)
		if !ok {
			return bad()
		}
		vault, err := credentials.NewInteractiveKeychain()
		if err != nil {
			return productionFailure(out, err)
		}
		manager := *r.Manager
		manager.Vault = vault
		r.Manager = &manager
		remote = r
	}
	var report channels.AccountInspection
	var err error
	if *channel == "oppo" {
		report, err = remote.InspectOPPO(ctx, *clientID, *name, *pkg)
	} else {
		r, ok := remote.(interface {
			InspectAccount(context.Context, channels.AccountQuery) (channels.AccountInspection, error)
		})
		if !ok {
			return bad()
		}
		query := channels.AccountQuery{Channel: *channel, Account: *clientID, ClientID: *apiID, SecretName: *name, PublicKeyName: *publicKey, PackageName: *pkg, AppID: *appID}
		if *replaceToken != "" {
			recovery, ok := remote.(interface {
				InspectHonorWithTokenReplacement(context.Context, channels.AccountQuery, string) (channels.AccountInspection, error)
			})
			if !ok {
				return bad()
			}
			report, err = recovery.InspectHonorWithTokenReplacement(ctx, query, *replaceToken)
		} else {
			report, err = r.InspectAccount(ctx, query)
		}
	}
	if err != nil {
		code, action := 3, "review_account_query"
		var credential *credentials.Error
		if errors.As(err, &credential) {
			code, action = 4, "review_keychain_access"
		}
		if errors.Is(err, credentials.ErrRefreshUnknown) {
			code, action = 3, "reconcile_account_token"
		}
		if errors.Is(err, credentials.ErrReplacementConflict) {
			code, action = 3, "review_token_replacement"
		}
		if credential != nil && credential.Code == "token_refresh_rejected" {
			code, action = 4, "verify_api_credentials"
		}
		// Only fixed public error types cross this boundary; an unexpected error
		// cannot reveal a vendor response, credential or signed request URL.
		public := fmt.Errorf("account inspection failed")
		var vendor *channels.Error
		if credential != nil {
			public = fmt.Errorf("account inspection: %s", credential.Code)
		} else if errors.As(err, &vendor) {
			public = fmt.Errorf("account inspection: %s", vendor.Code)
		}
		if ctx.Err() != nil {
			public = ctx.Err()
		}
		var diagnostic *channels.AccountQueryError
		if errors.As(err, &diagnostic) {
			if encode(out, map[string]any{"schema_version": 1, "mode": mode, "effect": "not_sent", "error": public.Error(), "next_action": action, "diagnostic": diagnostic.Diagnostic}) != nil {
				return 5
			}
			return code
		}
		return failure(out, mode, action, code, public)
	}
	result := struct {
		SchemaVersion    int                        `json:"schema_version"`
		Mode             string                     `json:"mode"`
		PublishingEffect string                     `json:"publishing_effect"`
		TokenLifecycle   string                     `json:"token_lifecycle"`
		Inspection       channels.AccountInspection `json:"inspection"`
		Artifact         *artifact.Info             `json:"artifact,omitempty"`
		ArtifactSHA256   string                     `json:"artifact_sha256,omitempty"`
	}{1, manifest.Production, "not_sent", "account_coordinated", report, local, hash}
	if encode(out, result) != nil {
		return 5
	}
	return 0
}
