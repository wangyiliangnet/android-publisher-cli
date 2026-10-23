package channels

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// AccountQuery contains only public identity and Keychain references.
type AccountQuery struct {
	Channel       string `json:"channel"`
	Account       string `json:"account"`
	ClientID      string `json:"client_id"`
	SecretName    string `json:"credential_name"`
	PublicKeyName string `json:"public_key_name,omitempty"`
	PackageName   string `json:"package_name"`
	AppID         string `json:"app_id,omitempty"`
	LanguageID    string `json:"language_id,omitempty"`
	diagnostic    *QueryDiagnostic
}

// Diagnostics contain fixed schema paths, JSON types and numeric result codes.
// Response values such as credentials, URLs and account details never appear.
type QueryDiagnostic struct {
	HTTPStatus int               `json:"http_status,omitempty"`
	OAuthError string            `json:"oauth_error,omitempty"`
	Shape      map[string]string `json:"response_shape,omitempty"`
	Codes      map[string]string `json:"vendor_codes,omitempty"`
}

func (r Runtime) InspectHonorWithTokenReplacement(ctx context.Context, q AccountQuery, pendingDigest string) (AccountInspection, error) {
	if q.Channel != "honor" || !manifest.ValidHash(pendingDigest) {
		return AccountInspection{}, fail("invalid_token_replacement_scope", "not_sent")
	}
	r.honorReplacement = pendingDigest
	return r.InspectAccount(ctx, q)
}

type AccountQueryError struct {
	Cause      error
	Diagnostic QueryDiagnostic
}

func (e *AccountQueryError) Error() string { return e.Cause.Error() }
func (e *AccountQueryError) Unwrap() error { return e.Cause }

func (q AccountQuery) valid() bool {
	if !account.ValidName(q.Account) || !ValidInspectionPackage(q.PackageName) || !credentials.ValidCredentialName(q.SecretName) || len(q.ClientID) > 256 || strings.ContainsAny(q.ClientID, "\r\n\x00") {
		return false
	}
	switch q.Channel {
	case "huawei":
		return q.ClientID == ""
	case "xiaomi":
		return q.ClientID != "" && credentials.ValidCredentialName(q.PublicKeyName)
	case "honor", "vivo":
		return q.ClientID != ""
	case "tencent":
		return q.ClientID != "" && account.ValidName(q.AppID)
	}
	return false
}

func (q AccountQuery) target() Target {
	return Target{Account: account.Scope{Channel: q.Channel, Account: q.Account}, PackageName: q.PackageName, AppID: q.AppID, LanguageID: q.LanguageID, Credential: CredentialConfig{ID: q.ClientID, SecretName: q.SecretName, PublicKeyName: q.PublicKeyName}}
}

func readAccount(ctx context.Context, c *Client, q AccountQuery) (Details, error) {
	switch q.Channel {
	case "huawei":
		o, err := c.huaweiJSON(ctx, "/api/publish/v2/appid-list?"+url.Values{"packageName": {q.PackageName}, "packageTypes": {"1"}}.Encode(), "GET", transport.Read, "", nil)
		if err != nil {
			return Details{}, err
		}
		rows := o.list("appids")
		if len(rows) != 1 || !account.ValidName(rows[0].id("value")) {
			return Details{}, fail("application_binding_not_verified", "not_sent")
		}
		id := rows[0].id("value")
		if q.AppID != "" && id != q.AppID {
			return Details{}, fail("application_binding_not_verified", "not_sent")
		}
		d, err := c.HuaweiDetails(ctx, id)
		d.PackageName = q.PackageName
		return d, err
	case "honor":
		d, err := c.HonorDetails(ctx, q.PackageName)
		if q.AppID != "" && d.AppID != q.AppID && err == nil {
			return Details{}, fail("application_binding_not_verified", "not_sent")
		}
		if n, e := strconv.ParseInt(d.Metadata["live_version_code"], 10, 64); e == nil {
			d.VersionCode = n
		}
		return d, err
	case "xiaomi":
		d, err := c.XiaomiDetails(ctx, q.PackageName)
		d.AppID = q.PackageName
		return d, err
	case "vivo":
		d, err := c.VivoDetails(ctx, q.PackageName)
		d.AppID = q.PackageName
		return d, err
	case "tencent":
		return c.TencentDetails(ctx, q.PackageName, q.AppID)
	}
	return Details{}, fail("unsupported_account_query", "not_sent")
}

func (r Runtime) InspectAccount(ctx context.Context, q AccountQuery) (AccountInspection, error) {
	if !q.valid() || r.Manager == nil {
		return AccountInspection{}, fail("invalid_account_query", "not_sent")
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	q.diagnostic = &QueryDiagnostic{}
	r = r.readOnly(q)
	var report AccountInspection
	err := r.WithClient(ctx, q.target(), func(c *Client, _ Materials) error {
		d, err := readAccount(ctx, c, q)
		if err != nil {
			return err
		}
		report = AccountInspection{Channel: q.Channel, Account: q.Account, PackageName: d.PackageName, AppID: d.AppID, RemoteVersion: d.VersionCode, QueryPermission: "verified", PublishingPermission: "not_verified", Review: "unknown", Availability: "unknown", CollectedAt: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return report, &AccountQueryError{Cause: err, Diagnostic: *q.diagnostic}
	}
	return report, nil
}

func (r Runtime) readOnly(q AccountQuery) Runtime {
	base := r.sender
	// A single pinned app identity is shared across client sessions.
	guard := &accountReadSender{query: q}
	r.sender = func(lease *account.Lease, scope account.Scope) Sender {
		var sender Sender = Controlled{Lease: lease, Scope: scope}
		if base != nil {
			sender = base(lease, scope)
		}
		return readSenderBound{guard: guard, underlying: sender}
	}
	return r
}
