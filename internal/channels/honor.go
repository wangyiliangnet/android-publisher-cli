package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func m4ID(s string) (json.Number, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || decimal(n) != s {
		return "", fail("invalid_id", "not_sent")
	}
	return json.Number(s), nil
}

func (c *Client) HonorToken(ctx context.Context) (credentials.FetchResult, error) {
	if err := c.channel("honor"); err != nil {
		return credentials.FetchResult{Effect: "not_sent"}, err
	}
	if c.Auth.Secret == nil || required(c.Auth.ID) != nil {
		return credentials.FetchResult{Effect: "not_sent"}, fail("missing_credential", "not_sent")
	}
	secret := c.Auth.Secret.Bytes()
	defer clear(secret)
	if len(secret) == 0 {
		return credentials.FetchResult{Effect: "not_sent"}, fail("missing_credential", "not_sent")
	}
	b := []byte(url.Values{"grant_type": {"client_credentials"}, "client_id": {c.Auth.ID}, "client_secret": {string(secret)}}.Encode())
	defer clear(b)
	started := c.now()
	o, err := c.send(ctx, "https://iam.developer.honor.com/auth/token", "POST", transport.Lifecycle, "", http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}, io.NopCloser(bytes.NewReader(b)), int64(len(b)), nil)
	if err != nil {
		effect := "unknown"
		var local *Error
		var network *transport.Error
		if errors.As(err, &local) && local.Effect == "not_sent" || errors.As(err, &network) && network.Effect == "not_sent" {
			effect = "not_sent"
		}
		if errors.As(err, &local) && local.Effect == "confirmed_rejected" {
			effect = "confirmed_rejected"
		}
		return credentials.FetchResult{Effect: effect}, err
	}
	seconds, ok := o.num("expires_in")
	if !ok || seconds <= 30 || seconds > 86400 || o.str("token_type") != "Bearer" || o.str("access_token") == "" {
		return credentials.FetchResult{Effect: "unknown"}, unknown()
	}
	token, err := credentials.NewSecret([]byte(o.str("access_token")))
	if err != nil {
		return credentials.FetchResult{Effect: "unknown"}, unknown()
	}
	return credentials.FetchResult{Value: token, ExpiresAt: started.Add(time.Duration(seconds) * time.Second), Effect: "confirmed_applied"}, nil
}

func (c *Client) honorJSON(ctx context.Context, path, method string, class transport.Class, op string, v any) (object, error) {
	if err := c.channel("honor"); err != nil {
		return nil, err
	}
	if c.Auth.Token == nil || len(c.Auth.Token.Bytes()) == 0 {
		return nil, fail("missing_credential", "not_sent")
	}
	o, err := c.json(ctx, path, method, class, op, v)
	if err != nil {
		return nil, err
	}
	if err = check(o, "code"); err != nil {
		return nil, err
	}
	return o, nil
}

func (c *Client) HonorDetails(ctx context.Context, packageName string) (Details, error) {
	if required(packageName) != nil || strings.Contains(packageName, ",") {
		return Details{}, fail("invalid_package", "not_sent")
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/get-app-id?"+url.Values{"pkgName": {packageName}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return Details{}, err
	}
	rows := o.list("data")
	if len(rows) != 1 || rows[0].str("packageName") != packageName {
		return Details{}, unknown()
	}
	appID := rows[0].id("appId")
	if _, err = m4ID(appID); err != nil {
		return Details{}, unknown()
	}
	o, err = c.honorJSON(ctx, "/openapi/v1/publish/get-app-detail?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return Details{}, err
	}
	data := o.obj("data")
	a := data.obj("basicInfo")
	if a.id("appId") != appID || a.str("packageName") != packageName {
		return Details{}, unknown()
	}
	d := Details{AppID: appID, PackageName: packageName, Metadata: map[string]string{}}
	if v, ok := data.obj("releaseInfo").num("versionCode"); ok && v > 0 {
		d.Metadata["live_version_code"] = decimal(v)
	}
	return d, nil
}

// HonorCurrentReleaseInfo contains only the documented application release
// identity and state. The endpoint does not document release notes or a hash of
// them; audit messages and attachment URLs are deliberately not retained.
type HonorCurrentReleaseInfo struct {
	ReleaseID   string
	VersionCode int64
	VersionName string
	AuditResult int64
}

// HonorCurrentRelease queries the latest publishing application, which is
// distinct from get-app-detail's releaseInfo for the already available version.
// The caller must compare the exact target version and require state 4 before
// treating this evidence as a bound draft that has not been submitted.
func (c *Client) HonorCurrentRelease(ctx context.Context, appID string) (HonorCurrentReleaseInfo, error) {
	if _, err := m4ID(appID); err != nil {
		return HonorCurrentReleaseInfo{}, err
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/get-app-current-release?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return HonorCurrentReleaseInfo{}, err
	}
	return parseHonorCurrentRelease(o, appID)
}

func parseHonorCurrentRelease(o object, appID string) (HonorCurrentReleaseInfo, error) {
	if _, err := m4ID(appID); err != nil {
		return HonorCurrentReleaseInfo{}, err
	}
	if err := check(o, "code"); err != nil {
		return HonorCurrentReleaseInfo{}, err
	}
	d := o.obj("data")
	if _, present := d["appId"]; present && d.id("appId") != appID {
		return HonorCurrentReleaseInfo{}, unknown()
	}
	version, versionOK := d.num("versionCode")
	audit, auditOK := d.num("auditResult")
	ref, name := d.str("releaseId"), d.str("versionName")
	// The documentation permits absent version fields for an unbound editing
	// application. Such a response cannot prove which APK the draft contains.
	if !versionOK || version <= 0 || !auditOK || audit < 0 || audit > 4 || required(ref, name) != nil {
		return HonorCurrentReleaseInfo{}, unknown()
	}
	return HonorCurrentReleaseInfo{ReleaseID: ref, VersionCode: version, VersionName: name, AuditResult: audit}, nil
}

func (c *Client) HonorAuthorize(ctx context.Context, op, appID string, file File) (UploadGrant, error) {
	if required(op) != nil {
		return UploadGrant{}, fail("missing_operation_identity", "not_sent")
	}
	if _, err := m4ID(appID); err != nil {
		return UploadGrant{}, err
	}
	if required(file.Name(), file.SHA256()) != nil || file.Size() <= 0 {
		return UploadGrant{}, fail("invalid_artifact", "not_sent")
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/get-file-upload-url?"+url.Values{"appId": {appID}}.Encode(), "POST", transport.Write, op, []any{map[string]any{"fileName": file.Name(), "fileType": 100, "fileSize": file.Size(), "fileSha256": file.SHA256()}})
	if err != nil {
		return UploadGrant{}, err
	}
	return parseHonorUploadGrant(o, appID, file, c.now())
}

// parseHonorUploadGrant validates a complete allocation response independently
// of HTTP, retaining exact signed int64 object IDs and the original expiry.
func parseHonorUploadGrant(o object, appID string, file File, now time.Time) (UploadGrant, error) {
	if err := check(o, "code"); err != nil {
		return UploadGrant{}, err
	}
	if _, err := m4ID(appID); err != nil || now.IsZero() || required(file.Name(), file.SHA256()) != nil || file.Size() <= 0 {
		return UploadGrant{}, unknown()
	}
	rows := o.list("data")
	if len(rows) != 1 || rows[0].str("fileName") != file.Name() {
		return UploadGrant{}, unknown()
	}
	a := rows[0]
	ref := a.id("objectId")
	if _, err := m4ID(ref); err != nil {
		return UploadGrant{}, unknown()
	}
	secs, ok := a.num("expireTime")
	if !ok || secs <= now.Unix() {
		return UploadGrant{}, unknown()
	}
	g := UploadGrant{URL: a.str("uploadUrl"), Ref: ref, Method: "POST", ExpiresAt: time.Unix(secs, 0).UTC()}
	if !honorUploadURL(g, appID, now) {
		return UploadGrant{}, unknown()
	}
	return g, nil
}

func honorUploadURL(g UploadGrant, appID string, now time.Time) bool {
	u, err := url.Parse(g.URL)
	if err != nil || !allowedUpload("honor", g.URL) || u.Path != "/openapi/v1/publish/file-upload" || g.Method != "POST" || !now.Before(g.ExpiresAt) {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(q) == 2 && len(q["appId"]) == 1 && len(q["objectId"]) == 1 && q.Get("appId") == appID && q.Get("objectId") == g.Ref
}

func (c *Client) HonorUpload(ctx context.Context, op, appID string, grant UploadGrant, file File) (Acceptance, error) {
	if err := c.channel("honor"); err != nil {
		return Acceptance{}, err
	}
	if required(op) != nil || !honorUploadURL(grant, appID, c.now()) {
		return Acceptance{}, fail("invalid_upload_grant", "not_sent")
	}
	if _, err := m4ID(appID); err != nil {
		return Acceptance{}, err
	}
	if _, err := m4ID(grant.Ref); err != nil {
		return Acceptance{}, err
	}
	if c.Auth.Token == nil || len(c.Auth.Token.Bytes()) == 0 {
		return Acceptance{}, fail("missing_credential", "not_sent")
	}
	path := strings.TrimPrefix(grant.URL, origins["honor"])
	o, err := c.multipart(ctx, path, transport.Write, op, nil, map[string]File{"file": file}, c.bearer())
	if err != nil {
		return Acceptance{}, err
	}
	if err = check(o, "code"); err != nil {
		return Acceptance{}, err
	}
	return Acceptance{grant.Ref, "uploaded_object"}, nil
}

func (c *Client) HonorBind(ctx context.Context, op, appID string, grant UploadGrant) (Acceptance, error) {
	if required(op) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	if _, err := m4ID(appID); err != nil {
		return Acceptance{}, err
	}
	objectID, err := m4ID(grant.Ref)
	if err != nil {
		return Acceptance{}, err
	}
	_, err = c.honorJSON(ctx, "/openapi/v1/publish/update-file-info?"+url.Values{"appId": {appID}}.Encode(), "POST", transport.Write, op, map[string]any{"bindingFileList": []any{map[string]any{"objectId": objectID}}})
	if err != nil {
		return Acceptance{}, err
	}
	return Acceptance{grant.Ref, "bound_object"}, nil
}

func (c *Client) HonorSubmit(ctx context.Context, op, appID string, releaseTime *time.Time) (Acceptance, error) {
	if required(op) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	if _, err := m4ID(appID); err != nil {
		return Acceptance{}, err
	}
	v := map[string]any{"releaseType": 1}
	if releaseTime != nil {
		if !releaseTime.After(c.now()) {
			return Acceptance{}, fail("invalid_release_time", "not_sent")
		}
		v["releaseType"] = 2
		v["releaseTime"] = releaseTime.Format("2006-01-02T15:04:05-0700")
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/submit-audit?"+url.Values{"appId": {appID}}.Encode(), "POST", transport.Write, op, v)
	if err != nil {
		return Acceptance{}, err
	}
	ref := o.str("data")
	if ref == "" {
		return Acceptance{}, unknown()
	}
	return Acceptance{ref, "release_id"}, nil
}

// HonorAudit requires the persisted release ID from a verified submit response.
// Audit approval is separate from evidence that the target is available.
func (c *Client) HonorAudit(ctx context.Context, appID, releaseID string, targetVersion int64) (Status, error) {
	id, err := m4ID(appID)
	if err != nil {
		return Status{}, err
	}
	if required(releaseID) != nil || targetVersion <= 0 {
		return Status{}, fail("missing_required_input", "not_sent")
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/get-audit-result", "POST", transport.Query, "", map[string]any{"appId": []any{map[string]any{"appId": id, "releaseId": releaseID}}})
	if err != nil {
		return Status{}, err
	}
	rows := o.list("data")
	if len(rows) != 1 || rows[0].id("appId") != appID || rows[0].str("releaseId") != releaseID {
		return Status{}, unknown()
	}
	s := Status{VersionCode: targetVersion, Ref: releaseID, Review: "unknown", Availability: "unknown"}
	if result, ok := rows[0].num("auditResult"); ok {
		switch result {
		case 0:
			s.Review = "in_review"
		case 1:
			s.Review = "approved"
		case 2:
			s.Review = "rejected"
		}
	}
	return s, nil
}

func (c *Client) HonorLanguageSnapshot(ctx context.Context, appID, languageID string) (LocalizationSnapshot, error) {
	if _, err := m4ID(appID); err != nil {
		return LocalizationSnapshot{}, err
	}
	if required(languageID) != nil {
		return LocalizationSnapshot{}, fail("missing_required_input", "not_sent")
	}
	o, err := c.honorJSON(ctx, "/openapi/v1/publish/get-app-detail?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return LocalizationSnapshot{}, err
	}
	data := o.obj("data")
	if data.obj("basicInfo").id("appId") != appID {
		return LocalizationSnapshot{}, unknown()
	}
	return localizationRow("honor", appID, languageID, data.list("languageInfo"), "languageId", "appName", "intro", "briefIntro", "newFeature")
}

func (c *Client) HonorLanguagePatch(ctx context.Context, op string, snapshot LocalizationSnapshot, notes string) (Acceptance, error) {
	return c.HonorLanguagePatchFrozen(ctx, op, snapshot, notes)
}

// HonorLanguagePatchFrozen updates the one selected language from a previously
// frozen baseline. setAll:0 is essential: its default of 1 deletes other languages.
func (c *Client) HonorLanguagePatchFrozen(ctx context.Context, op string, snapshot LocalizationSnapshot, notes string) (Acceptance, error) {
	if required(op) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	if err := snapshot.validate("honor", notes); err != nil {
		return Acceptance{}, err
	}
	if _, err := m4ID(snapshot.AppID); err != nil {
		return Acceptance{}, err
	}
	rows := []map[string]string{{"languageId": snapshot.LanguageID, "appName": snapshot.AppName, "intro": snapshot.Intro, "briefIntro": snapshot.BriefIntro, "newFeature": notes}}
	_, err := c.honorJSON(ctx, "/openapi/v1/publish/update-language-info?"+url.Values{"appId": {snapshot.AppID}}.Encode(), "POST", transport.Write, op, map[string]any{"setAll": 0, "languageInfoList": rows})
	if err != nil {
		return Acceptance{}, err
	}
	return ack(op), nil
}
