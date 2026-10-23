package channels

import (
	"context"
	"net/url"
	"regexp"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// AccountInspection proves a successful application query only. It does not
// prove publishing permission, artifact association, review or availability.
// Only public identifiers and bounded status codes leave the local executor.
type AccountInspection struct {
	Channel              string    `json:"channel"`
	Account              string    `json:"account"`
	PackageName          string    `json:"package_name"`
	AppID                string    `json:"app_id"`
	RemoteVersion        int64     `json:"remote_version_code"`
	RemoteVersionID      string    `json:"remote_version_id,omitempty"`
	RemoteAuditCode      string    `json:"remote_audit_code,omitempty"`
	RemoteAppStateCode   string    `json:"remote_app_state_code,omitempty"`
	QueryPermission      string    `json:"query_permission"`
	PublishingPermission string    `json:"publishing_permission"`
	Review               string    `json:"review_state"`
	Availability         string    `json:"availability_state"`
	CollectedAt          time.Time `json:"collected_at"`
}

var inspectionPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var inspectionCode = regexp.MustCompile(`^-?[0-9]{1,12}$`)

func ValidInspectionPackage(pkg string) bool {
	return len(pkg) <= 256 && inspectionPackage.MatchString(pkg)
}

// InspectOPPO is a separate, explicitly read-only entry point. It does not
// construct a publisher or release, and cannot authorize/upload/submit anything.
// The client ID is also the canonical local account key, shared with token use.
func (r Runtime) InspectOPPO(ctx context.Context, clientID, secretName, pkg string) (AccountInspection, error) {
	if !account.ValidName(clientID) || !credentials.ValidCredentialName(secretName) || !ValidInspectionPackage(pkg) || r.Manager == nil {
		return AccountInspection{}, fail("invalid_account_inspection", "not_sent")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	scope := account.Scope{Channel: "oppo", Account: clientID}
	base := r.sender
	r.sender = func(lease *account.Lease, scope account.Scope) Sender {
		var underlying Sender = Controlled{Lease: lease, Scope: scope}
		if base != nil {
			underlying = base(lease, scope)
		}
		return inspectionSender{underlying: underlying, clientID: clientID, pkg: pkg}
	}
	var report AccountInspection
	err := r.WithClient(ctx, Target{Account: scope, Credential: CredentialConfig{ID: clientID, SecretName: secretName}}, func(c *Client, _ Materials) error {
		d, err := c.OPPODetails(ctx, pkg, 0)
		if err != nil {
			return err
		}
		if !account.ValidName(d.AppID) {
			return unknown()
		}
		report = AccountInspection{Channel: "oppo", Account: clientID, PackageName: d.PackageName, AppID: d.AppID, RemoteVersion: d.VersionCode, QueryPermission: "verified", PublishingPermission: "not_verified", Review: "unknown", Availability: "unknown", CollectedAt: time.Now().UTC()}
		if ref := d.Metadata["version_id"]; account.ValidName(ref) {
			report.RemoteVersionID = ref
		}
		if code := d.Metadata["audit_status"]; inspectionCode.MatchString(code) {
			report.RemoteAuditCode = code
		}
		if code := d.Metadata["state"]; inspectionCode.MatchString(code) {
			report.RemoteAppStateCode = code
		}
		return nil
	})
	if err != nil {
		return AccountInspection{}, err
	}
	return report, nil
}

// Pin exact GET routes, request classes and target identities before forwarding
// anything, including the token lifecycle request made by WithClient itself.
type inspectionSender struct {
	underlying    Sender
	clientID, pkg string
}

func (s inspectionSender) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	reject := func() ([]byte, error) {
		if q.Body != nil {
			q.Body.Close()
		}
		return nil, fail("account_inspection_read_only", "not_sent")
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.Host != "oop-openapi-cn.heytapmobi.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || q.Method != "GET" || q.Body != nil || q.ContentLength != 0 || q.OperationID != "" || q.Grant != nil || len(q.Headers) != 0 || s.underlying == nil {
		return reject()
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return reject()
	}
	for _, v := range values {
		if len(v) != 1 || v[0] == "" {
			return reject()
		}
	}
	switch u.Path {
	case "/developer/v1/token":
		if q.Class != transport.Lifecycle || len(values) != 2 || values.Get("client_id") != s.clientID || values.Get("client_secret") == "" {
			return reject()
		}
	case "/resource/v1/app/info":
		if q.Class != transport.Read || len(values) != 4 || values.Get("pkg_name") != s.pkg || values.Get("access_token") == "" || values.Get("timestamp") == "" || values.Get("api_sign") == "" {
			return reject()
		}
	default:
		return reject()
	}
	return s.underlying.Send(ctx, q)
}
