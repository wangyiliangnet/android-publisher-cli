package channels

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

const huaweiAudience = "https://oauth-login.cloud.huawei.com/oauth2/v3/token"

// ServiceAccountJWT implements the current AGC Service Account contract. The
// resulting JWT is the bearer token; aud is not a token-exchange destination.
func ServiceAccountJWT(keyID, subAccount string, pkcs8 *credentials.Secret, now time.Time) (*credentials.Secret, error) {
	if required(keyID, subAccount) != nil || pkcs8 == nil || now.IsZero() || now.Unix() < 0 {
		return nil, fail("invalid_service_account", "not_sent")
	}
	b := pkcs8.Bytes()
	defer clear(b)
	block, rest := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fail("invalid_service_account", "not_sent")
	}
	defer clear(block.Bytes)
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fail("invalid_service_account", "not_sent")
	}
	key, ok := k.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, fail("invalid_service_account", "not_sent")
	}
	header := raw(map[string]any{"alg": "PS256", "kid": keyID, "typ": "JWT"})
	payload := raw(map[string]any{"aud": huaweiAudience, "iss": subAccount, "iat": now.Unix(), "exp": now.Unix() + 3600})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, digest[:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	if err != nil {
		return nil, fail("service_account_signing_failed", "not_sent")
	}
	defer clear(signature)
	return credentials.NewSecret([]byte(input + "." + base64.RawURLEncoding.EncodeToString(signature)))
}

func (c *Client) huaweiJSON(ctx context.Context, path, method string, class transport.Class, op string, v any) (object, error) {
	if err := c.channel("huawei"); err != nil {
		return nil, err
	}
	if c.Auth.Token == nil || len(c.Auth.Token.Bytes()) == 0 {
		return nil, fail("missing_credential", "not_sent")
	}
	h := c.bearer()
	// API-client mode additionally requires client_id. Service Account mode uses
	// only Bearer and does not fabricate a client ID from the sub-account ID.
	if c.Auth.ID != "" {
		h.Set("Client_id", c.Auth.ID)
	}
	var body io.ReadCloser
	var b []byte
	if v != nil {
		b = raw(v)
		body = io.NopCloser(bytes.NewReader(b))
	}
	o, err := c.send(ctx, origins[c.Channel]+path, method, class, op, h, body, int64(len(b)), nil)
	if err != nil {
		return nil, err
	}
	if err = check(o.obj("ret"), "code"); err != nil {
		return nil, err
	}
	return o, nil
}

// HuaweiDetails reads the app's editable version; it does not assert that version
// is live. The current API does not return packageName in this response.
func (c *Client) HuaweiDetails(ctx context.Context, appID string) (Details, error) {
	if required(appID) != nil {
		return Details{}, fail("missing_required_input", "not_sent")
	}
	o, err := c.huaweiJSON(ctx, "/api/publish/v2/app-info?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return Details{}, err
	}
	a := o.obj("appInfo")
	version, ok := a.num("versionCode")
	if !ok || version <= 0 {
		return Details{}, unknown()
	}
	d := Details{AppID: appID, VersionCode: version, VersionName: a.str("versionNumber"), Metadata: map[string]string{}}
	if s := a.str("versionId"); s != "" {
		d.Metadata["version_id"] = s
	}
	return d, nil
}

// HuaweiVerifyApp binds the caller's app ID to one package name through the
// official lookup. app-info itself contains no packageName field. The Pair.key
// in this response is an app name, not the package name, so compare only the
// unique result ID from this single-package query.
func (c *Client) HuaweiVerifyApp(ctx context.Context, appID, packageName string) error {
	if required(appID, packageName) != nil || strings.Contains(packageName, ",") {
		return fail("invalid_application_binding", "not_sent")
	}
	o, err := c.huaweiJSON(ctx, "/api/publish/v2/appid-list?"+url.Values{"packageName": {packageName}, "packageTypes": {"1"}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return err
	}
	rows := o.list("appids")
	if len(rows) != 1 || rows[0].str("value") != appID {
		return fail("application_binding_not_verified", "unknown")
	}
	return nil
}

// HuaweiAuthorize follows the current /for-obs contract. Huawei documents an
// approximate five-minute lifetime, so four minutes is a conservative local
// deadline, not a server-supplied expiry. Unverified storage hosts stay blocked.
func (c *Client) HuaweiAuthorize(ctx context.Context, op, appID string, file File, mainland *bool) (UploadGrant, error) {
	if required(op, appID, file.Name(), file.SHA256()) != nil || file.Size() <= 0 {
		return UploadGrant{}, fail("missing_required_input", "not_sent")
	}
	q := url.Values{"appId": {appID}, "fileName": {file.Name()}, "sha256": {file.SHA256()}, "contentLength": {decimal(file.Size())}, "releaseType": {"1"}}
	if mainland != nil {
		q.Set("chineseMainlandFlag", "0")
		if *mainland {
			q.Set("chineseMainlandFlag", "1")
		}
	}
	started := c.now()
	o, err := c.huaweiJSON(ctx, "/api/publish/v2/upload-url/for-obs?"+q.Encode(), "GET", transport.Write, op, nil)
	if err != nil {
		return UploadGrant{}, err
	}
	g, err := parseHuaweiUploadGrant(o, file, started)
	if err != nil {
		return UploadGrant{}, err
	}
	if err = validateHuaweiGrant(g, file, c.now()); err != nil {
		return UploadGrant{}, unknown()
	}
	return g, nil
}

// parseHuaweiUploadGrant validates an allocation response independently of its
// HTTP caller so a sealed response can be recovered without another allocation.
// started is the original request time, never the recovery time.
func parseHuaweiUploadGrant(o object, file File, started time.Time) (UploadGrant, error) {
	if err := check(o.obj("ret"), "code"); err != nil {
		return UploadGrant{}, err
	}
	if started.IsZero() || required(file.Name(), file.SHA256()) != nil || file.Size() <= 0 {
		return UploadGrant{}, unknown()
	}
	info := o.obj("urlInfo")
	g := UploadGrant{URL: info.str("url"), Ref: info.str("objectId"), Method: info.str("method"), ExpiresAt: started.Add(4 * time.Minute), Headers: make(http.Header)}
	if !allowedUpload("huawei", g.URL) {
		return UploadGrant{}, fail("upload_host_not_verified_after_allocation", "unknown")
	}
	if required(g.Ref) != nil || g.Method != "PUT" {
		return UploadGrant{}, unknown()
	}
	names := map[string]bool{}
	for k, v := range info.obj("headers") {
		name := http.CanonicalHeaderKey(k)
		if names[name] {
			return UploadGrant{}, unknown()
		}
		names[name] = true
		var s string
		if json.Unmarshal(v, &s) != nil {
			return UploadGrant{}, unknown()
		}
		g.Headers.Set(k, s)
	}
	if err := validateHuaweiGrant(g, file, started); err != nil {
		return UploadGrant{}, unknown()
	}
	return g, nil
}

func validateHuaweiGrant(g UploadGrant, file File, now time.Time) error {
	u, err := url.Parse(g.URL)
	if err != nil || !allowedUpload("huawei", g.URL) || g.Method != "PUT" || g.Ref == "" || !now.Before(g.ExpiresAt) {
		return fail("invalid_upload_grant", "not_sent")
	}
	for _, k := range []string{"Authorization", "X-Amz-Content-Sha256", "X-Amz-Date", "Host", "User-Agent", "Content-Type"} {
		v := g.Headers.Get(k)
		if len(g.Headers.Values(k)) != 1 || v == "" || strings.ContainsAny(v, "\r\n") {
			return fail("invalid_upload_grant", "not_sent")
		}
	}
	auth := g.Headers.Get("Authorization")
	if !(strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || strings.HasPrefix(auth, "OBS ")) || g.Headers.Get("X-Amz-Content-Sha256") != file.SHA256() || !strings.EqualFold(g.Headers.Get("Host"), u.Host) || g.Headers.Get("Content-Type") != "application/octet-stream" {
		return fail("invalid_upload_grant", "not_sent")
	}
	if _, err = time.Parse("20060102T150405Z", g.Headers.Get("X-Amz-Date")); err != nil {
		return fail("invalid_upload_grant", "not_sent")
	}
	return nil
}

func (c *Client) HuaweiUpload(ctx context.Context, op string, grant UploadGrant, file File) (Acceptance, error) {
	if err := c.channel("huawei"); err != nil {
		return Acceptance{}, err
	}
	if required(op) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	if err := validateHuaweiGrant(grant, file, c.now()); err != nil {
		return Acceptance{}, err
	}
	if err := c.uploadRaw(ctx, op, grant, file); err != nil {
		return Acceptance{}, err
	}
	return Acceptance{grant.Ref, "uploaded_object"}, nil
}

func (c *Client) HuaweiBind(ctx context.Context, op, appID string, file File, grant UploadGrant) (Acceptance, error) {
	if required(op, appID, file.Name(), grant.Ref) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	_, err := c.huaweiJSON(ctx, "/api/publish/v2/app-file-info?"+url.Values{"appId": {appID}}.Encode(), "PUT", transport.Write, op, map[string]any{"fileType": 5, "files": []any{map[string]string{"fileName": file.Name(), "fileDestUrl": grant.Ref}}})
	if err != nil {
		return Acceptance{}, err
	}
	return Acceptance{grant.Ref, "bound_object"}, nil
}

func (c *Client) HuaweiSubmit(ctx context.Context, op, appID string, releaseTime *time.Time) (Acceptance, error) {
	if required(op, appID) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	q := url.Values{"appId": {appID}, "releaseType": {"1"}}
	if releaseTime != nil {
		if !releaseTime.After(c.now()) {
			return Acceptance{}, fail("invalid_release_time", "not_sent")
		}
		q.Set("releaseTime", releaseTime.Format("2006-01-02T15:04:05-0700"))
	}
	// The caller persists the two-minute asynchronous-parse checkpoint. A known
	// business error is never retried here; 204144660 also covers permanent errors.
	_, err := c.huaweiJSON(ctx, "/api/publish/v2/app-submit?"+q.Encode(), "POST", transport.Write, op, nil)
	if err != nil {
		return Acceptance{}, err
	}
	return ack(op), nil
}

func (c *Client) HuaweiStatus(ctx context.Context, appID string, targetVersion int64) (Status, error) {
	if required(appID) != nil || targetVersion <= 0 {
		return Status{}, fail("missing_required_input", "not_sent")
	}
	o, err := c.huaweiJSON(ctx, "/api/publish/v2/app-info?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return Status{}, err
	}
	a := o.obj("appInfo")
	s := Status{VersionCode: targetVersion, Review: "unknown", Availability: "unknown"}
	if version, ok := a.num("versionCode"); ok && version == targetVersion {
		s.Ref = a.str("versionId")
		if state, ok := a.num("releaseState"); ok {
			switch state {
			case 0:
				s.Review = "approved"
			case 1, 8, 13:
				s.Review = "rejected"
			case 4, 5, 12:
				s.Review = "in_review"
			}
		}
	}
	if live, ok := a.num("onShelfVersionCode"); ok && live == targetVersion {
		s.Availability = "live"
	}
	return s, nil
}

// LocalizationSnapshot is frozen public application metadata. Its digest binds
// the baseline to one channel's app and language; it contains no credentials.
// Store it with the intent before a later, explicit metadata write.
type LocalizationSnapshot struct {
	Channel, AppID, LanguageID, AppName, Intro, BriefIntro, Notes, Digest string
}

func NewLocalizationSnapshot(channel, appID, lang, name, intro, brief, notes string) (LocalizationSnapshot, error) {
	s := LocalizationSnapshot{Channel: channel, AppID: appID, LanguageID: lang, AppName: name, Intro: intro, BriefIntro: brief, Notes: notes}
	if (channel != "huawei" && channel != "honor") || required(appID, lang, name, intro) != nil || utf8.RuneCountInString(intro) > 8000 || utf8.RuneCountInString(brief) > 80 {
		return LocalizationSnapshot{}, fail("invalid_language_snapshot", "not_sent")
	}
	s.Digest = s.digest()
	return s, nil
}

func (s LocalizationSnapshot) digest() string {
	b := raw([]string{s.Channel, s.AppID, s.LanguageID, s.AppName, s.Intro, s.BriefIntro, s.Notes})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s LocalizationSnapshot) validate(channel, notes string) error {
	if s.Channel != channel || s.Digest == "" || s.Digest != s.digest() || required(s.AppID, s.LanguageID, s.AppName, s.Intro) != nil {
		return fail("invalid_language_snapshot", "not_sent")
	}
	if notes == "" {
		return fail("language_clear_not_verified", "not_sent")
	}
	if utf8.RuneCountInString(notes) > 500 {
		return fail("invalid_language_patch", "not_sent")
	}
	return nil
}

func localizationRow(channel, appID, languageID string, rows []object, languageKey, nameKey, introKey, briefKey, notesKey string) (LocalizationSnapshot, error) {
	var selected object
	seen := map[string]bool{}
	for _, row := range rows {
		lang := row.str(languageKey)
		if lang == "" || seen[lang] {
			return LocalizationSnapshot{}, unknown()
		}
		seen[lang] = true
		if lang == languageID {
			selected = row
		}
	}
	if selected == nil {
		return LocalizationSnapshot{}, fail("language_create_not_verified", "not_sent")
	}
	values := make([]string, 4)
	for i, key := range []string{nameKey, introKey, briefKey, notesKey} {
		if json.Unmarshal(selected[key], &values[i]) != nil || string(selected[key]) == "null" {
			return LocalizationSnapshot{}, fail("language_snapshot_incomplete", "not_sent")
		}
	}
	return NewLocalizationSnapshot(channel, appID, languageID, values[0], values[1], values[2], values[3])
}

func (c *Client) HuaweiLanguageSnapshot(ctx context.Context, appID, languageID string) (LocalizationSnapshot, error) {
	if required(appID, languageID) != nil {
		return LocalizationSnapshot{}, fail("missing_required_input", "not_sent")
	}
	o, err := c.huaweiJSON(ctx, "/api/publish/v2/app-info?"+url.Values{"appId": {appID}}.Encode(), "GET", transport.Read, "", nil)
	if err != nil {
		return LocalizationSnapshot{}, err
	}
	return localizationRow("huawei", appID, languageID, o.list("languages"), "lang", "appName", "appDesc", "briefInfo", "newFeatures")
}

// HuaweiLanguagePatch sends one business request from a frozen baseline. It
// never fetches a newer baseline or deletes another language implicitly.
func (c *Client) HuaweiLanguagePatch(ctx context.Context, op string, snapshot LocalizationSnapshot, notes string) (Acceptance, error) {
	if required(op) != nil {
		return Acceptance{}, fail("missing_required_input", "not_sent")
	}
	if err := snapshot.validate("huawei", notes); err != nil {
		return Acceptance{}, err
	}
	_, err := c.huaweiJSON(ctx, "/api/publish/v2/app-language-info?"+url.Values{"appId": {snapshot.AppID}, "releaseType": {"1"}}.Encode(), "PUT", transport.Write, op, map[string]string{"lang": snapshot.LanguageID, "appName": snapshot.AppName, "appDesc": snapshot.Intro, "briefInfo": snapshot.BriefIntro, "newFeatures": notes})
	if err != nil {
		return Acceptance{}, err
	}
	return ack(op), nil
}
