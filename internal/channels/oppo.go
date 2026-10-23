package channels

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// OPPO's public API documentation: docs 10998, 10999, 11003, 11004,
// 11001 and 11176. Token exchange is a lifecycle effect, even though GET.
func (c *Client) OPPOToken(ctx context.Context) (credentials.FetchResult, error) {
	if err := c.channel("oppo"); err != nil {
		return oppoTokenFailure(err)
	}
	secret := c.Auth.Secret.Bytes()
	defer clear(secret)
	if err := required(c.Auth.ID, string(secret)); err != nil {
		return oppoTokenFailure(err)
	}
	q := url.Values{"client_id": {c.Auth.ID}, "client_secret": {string(secret)}}
	o, err := c.send(ctx, origins[c.Channel]+"/developer/v1/token?"+q.Encode(), "GET", transport.Lifecycle, "", nil, nil, 0, nil)
	if err != nil {
		return oppoTokenFailure(err)
	}
	if err = check(o, "errno"); err != nil {
		return oppoTokenFailure(err)
	}
	d := o.obj("data")
	expiry, ok := d.num("expire_in")
	if !ok || expiry <= c.now().Unix() || expiry > 253402300799 || d.str("access_token") == "" {
		return oppoTokenFailure(unknown())
	}
	token, err := credentials.NewSecret([]byte(d.str("access_token")))
	if err != nil {
		return oppoTokenFailure(unknown())
	}
	return credentials.FetchResult{Value: token, ExpiresAt: time.Unix(expiry, 0).UTC(), Effect: "confirmed_applied"}, nil
}

// A fetch failure must classify its lifecycle effect for Session.Token's
// durable refresh barrier. Missing local input removes the marker; a request
// with insufficient response evidence keeps the marker across future sessions.
func oppoTokenFailure(err error) (credentials.FetchResult, error) {
	effect := "unknown"
	var local *Error
	var remote *transport.Error
	if errors.As(err, &local) && local.Effect == "not_sent" {
		effect = "not_sent"
	}
	if errors.As(err, &remote) && remote.Effect == "not_sent" {
		effect = "not_sent"
	}
	return credentials.FetchResult{Effect: effect}, err
}
func channelMD5(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 16 }
func (c *Client) oppoSigned(fields map[string]string) (map[string]string, error) {
	if err := c.channel("oppo"); err != nil {
		return nil, err
	}
	secret, token := c.Auth.Secret.Bytes(), c.Auth.Token.Bytes()
	defer clear(secret)
	defer clear(token)
	if err := required(string(secret), string(token)); err != nil {
		return nil, err
	}
	f := copyFields(fields)
	f["access_token"] = string(token)
	f["timestamp"] = decimal(c.now().Unix())
	f["api_sign"] = hmacSign(f, secret, "api_sign")
	return f, nil
}
func (c *Client) oppoQuery(ctx context.Context, path string, fields map[string]string) (object, error) {
	f, err := c.oppoSigned(fields)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for k, v := range f {
		q.Set(k, v)
	}
	o, err := c.send(ctx, origins[c.Channel]+path+"?"+q.Encode(), "GET", transport.Read, "", nil, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	if err = check(o, "errno"); err != nil {
		return nil, err
	}
	return o, nil
}

var oppoMetadataFields = []string{"app_name", "app_subname", "second_category_id", "third_category_id", "summary", "detail_desc", "update_desc", "privacy_source_url", "icon_url", "pic_url", "landscape_pic_url", "video_url", "online_type", "sche_online_time", "test_desc", "electronic_cert_url", "copyright_url", "icp_url", "special_url", "special_file_url", "business_username", "business_email", "business_mobile", "business_qq", "business_position", "business_address", "age_level", "adaptive_equipment", "adaptive_type", "customer_contact", "audit_status", "audit_status_name", "state", "apk_md5", "version_id"}
var oppoRequiredMetadata = []string{"app_name", "second_category_id", "third_category_id", "summary", "detail_desc", "update_desc", "privacy_source_url", "icon_url", "pic_url", "test_desc", "copyright_url", "business_username", "business_email", "business_mobile", "age_level", "adaptive_equipment"}

// The publishing API requires the generic copyright_url. Some accounts return
// their existing proof only in electronic_cert_url; apkgo issue #25 / PR #26
// records this protocol discrepancy. Keep both original fields in the baseline
// and derive the required request field without changing existing proof.
func oppoCopyrightSource(metadata map[string]string) string {
	if metadata["copyright_url"] != "" {
		return "copyright_url"
	}
	u, err := url.Parse(metadata["electronic_cert_url"])
	if err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == "" {
		return "electronic_cert_url"
	}
	return ""
}

func fieldText(o object, key string) string {
	if v := o.str(key); v != "" {
		return v
	}
	if n, ok := o.num(key); ok {
		return decimal(n)
	}
	return ""
}
func (c *Client) OPPODetails(ctx context.Context, pkg string, version int64) (Details, error) {
	if err := required(pkg); err != nil {
		return Details{}, err
	}
	if version < 0 {
		return Details{}, fail("invalid_version", "not_sent")
	}
	f := map[string]string{"pkg_name": pkg}
	if version > 0 {
		f["version_code"] = decimal(version)
	}
	o, err := c.oppoQuery(ctx, "/resource/v1/app/info", f)
	if err != nil {
		return Details{}, err
	}
	d := o.obj("data")
	n, err := strconv.ParseInt(d.id("version_code"), 10, 64)
	if err != nil || n <= 0 || d.str("pkg_name") != pkg || d.id("app_id") == "" || (version > 0 && n != version) {
		return Details{}, unknown()
	}
	out := Details{PackageName: pkg, AppID: d.id("app_id"), VersionCode: n, VersionName: d.str("version_name"), Metadata: map[string]string{}}
	for _, k := range oppoMetadataFields {
		if _, ok := d[k]; ok {
			out.Metadata[k] = fieldText(d, k)
		}
	}
	return out, nil
}

// OPPOAuthorize caps local use to thirty seconds. OPPO documents no expiry
// timestamp: this is a local deadline, not evidence of a remote validity period.
// Production wiring remains gated on account/host/one-time-grant validation.
func (c *Client) OPPOAuthorize(ctx context.Context, op string) (UploadGrant, error) {
	if err := required(op); err != nil {
		return UploadGrant{}, err
	}
	f, err := c.oppoSigned(nil)
	if err != nil {
		return UploadGrant{}, err
	}
	q := url.Values{}
	for k, v := range f {
		q.Set(k, v)
	}
	o, err := c.send(ctx, origins[c.Channel]+"/resource/v1/upload/get-upload-url?"+q.Encode(), "GET", transport.Write, op, nil, nil, 0, nil)
	if err != nil {
		return UploadGrant{}, err
	}
	if err = check(o, "errno"); err != nil {
		return UploadGrant{}, err
	}
	d := o.obj("data")
	endpoint, sign := d.str("upload_url"), d.str("sign")
	if !allowedUpload("oppo", endpoint) || sign == "" {
		return UploadGrant{}, unknown()
	}
	return UploadGrant{URL: endpoint, Ref: sign, ExpiresAt: c.now().Add(30 * time.Second), Method: "POST"}, nil
}

type OPPOFile struct {
	URL, MD5, Ref string
	Size          int64
}
type oppoFileCheckpoint struct {
	URL, MD5, Ref string
	Size          int64
}

func (OPPOFile) String() string     { return "[REDACTED OPPO file reference]" }
func (v OPPOFile) GoString() string { return v.String() }
func (v OPPOFile) MarshalJSON() ([]byte, error) {
	return []byte(`"[REDACTED OPPO file reference]"`), nil
}
func (c *Client) OPPOUpload(ctx context.Context, op string, g UploadGrant, f File) (OPPOFile, error) {
	if err := c.channel("oppo"); err != nil {
		return OPPOFile{}, err
	}
	if err := required(op, g.Ref); err != nil {
		return OPPOFile{}, err
	}
	if g.Method != "POST" || !allowedUpload("oppo", g.URL) || !g.ExpiresAt.After(c.now()) || g.ExpiresAt.After(c.now().Add(30*time.Second)) || f.size <= 0 {
		return OPPOFile{}, fail("invalid_upload_grant", "not_sent")
	}
	body, size, contentType, err := multipartBody(map[string]string{"type": "apk", "sign": g.Ref}, map[string]File{"file": f})
	if err != nil {
		return OPPOFile{}, err
	}
	o, err := c.send(ctx, g.URL, "POST", transport.Write, op, http.Header{"Content-Type": {contentType}}, body, size, &transport.Grant{URL: g.URL, Method: g.Method, ExpiresAt: g.ExpiresAt})
	if err != nil {
		return OPPOFile{}, err
	}
	return parseOPPOUploadedFile(o, f)
}

// url is an opaque vendor file reference passed back over the signed HTTPS API.
// The executor never downloads it. The actual multipart upload still requires
// an exact HTTPS grant and the reviewed upload-host allowlist.
func parseOPPOUploadedFile(o object, f File) (OPPOFile, error) {
	if err := check(o, "errno"); err != nil {
		return OPPOFile{}, err
	}
	d := o.obj("data")
	n, ok := d.num("file_size")
	u, e := url.Parse(d.str("url"))
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || !ok || n != f.size || !strings.EqualFold(d.str("md5"), f.md5) || d.str("file_extension") != "apk" || d.id("id") == "" {
		return OPPOFile{}, unknown()
	}
	return OPPOFile{URL: d.str("url"), MD5: f.md5, Ref: d.id("id"), Size: n}, nil
}

type OPPOUpdateInput struct {
	PackageName string
	VersionCode int64
	Existing    Details
	File        OPPOFile
	Patch       map[string]string
	OnlineType  int
	ScheduledAt time.Time
}

func (c *Client) OPPOUpdate(ctx context.Context, op string, in OPPOUpdateInput) (Acceptance, error) {
	if err := c.channel("oppo"); err != nil {
		return Acceptance{}, err
	}
	if err := required(op, in.PackageName, in.Existing.AppID, in.File.URL, in.File.Ref, in.File.MD5); err != nil {
		return Acceptance{}, err
	}
	if in.Existing.PackageName != in.PackageName || in.Existing.VersionCode <= 0 || in.VersionCode <= in.Existing.VersionCode || in.File.Size <= 0 || !channelMD5(in.File.MD5) {
		return Acceptance{}, fail("target_mismatch", "not_sent")
	}
	f, err := oppoUpdateFields(in, c.now())
	if err != nil {
		return Acceptance{}, err
	}
	f, err = c.oppoSigned(f)
	if err != nil {
		return Acceptance{}, err
	}
	o, err := c.form(ctx, "/resource/v1/app/upd", transport.Write, op, f)
	if err != nil {
		return Acceptance{}, err
	}
	if err = check(o, "errno"); err != nil {
		return Acceptance{}, err
	}
	var success bool
	if json.Unmarshal(o.obj("data")["success"], &success) != nil || !success {
		return Acceptance{}, unknown()
	}
	return ack(op), nil
}

// A task is processing evidence, independent of review and availability.
type OPPOTask struct {
	PackageName string
	VersionCode int64
	State       string
}

func (c *Client) OPPOTaskStatus(ctx context.Context, pkg string, version int64) (OPPOTask, error) {
	if err := required(pkg); err != nil {
		return OPPOTask{}, err
	}
	if version <= 0 {
		return OPPOTask{}, fail("invalid_version", "not_sent")
	}
	f, err := c.oppoSigned(map[string]string{"pkg_name": pkg, "version_code": decimal(version)})
	if err != nil {
		return OPPOTask{}, err
	}
	o, err := c.form(ctx, "/resource/v1/app/task-state", transport.Query, "", f)
	if err != nil {
		return OPPOTask{}, err
	}
	if err = check(o, "errno"); err != nil {
		return OPPOTask{}, err
	}
	d := o.obj("data")
	if d.str("pkg_name") != pkg || d.id("version_code") != decimal(version) {
		return OPPOTask{}, unknown()
	}
	states := map[string]string{"1": "pending", "2": "succeeded", "3": "failed"}
	state := states[d.str("task_state")]
	if state == "" {
		return OPPOTask{}, unknown()
	}
	return OPPOTask{pkg, version, state}, nil
}
func (c *Client) OPPOAudit(ctx context.Context, pkg string, version int64, md5 string) (Status, error) {
	if version <= 0 || !channelMD5(md5) {
		return Status{}, fail("missing_target_evidence", "not_sent")
	}
	d, err := c.OPPODetails(ctx, pkg, version)
	if err != nil {
		return Status{}, err
	}
	if !strings.EqualFold(d.Metadata["apk_md5"], md5) || d.Metadata["version_id"] == "" {
		return Status{}, unknown()
	}
	code := d.Metadata["audit_status"]
	s := Status{VersionCode: version, Ref: d.Metadata["version_id"], Review: "unknown", Availability: "unknown"}
	switch code {
	case "0":
		s.Review = "unknown"
	case "1", "4", "00":
		s.Review = "in_review"
	case "2", "6", "7", "111":
		s.Review = "approved"
	case "3", "5", "-11", "444":
		s.Review = "rejected"
	}
	// Explicit version-specific audit status is required as app state alone may
	// describe the previous live version while this version is in review.
	if code == "7" {
		s.Availability = "scheduled"
	}
	if code == "111" && d.Metadata["state"] == "1" {
		s.Availability = "live"
	}
	if code == "222" && d.Metadata["state"] == "2" {
		s.Availability = "not_observed_live"
	}
	return s, nil
}

// oppoUpdateFields validates frozen metadata without credentials or network.
func oppoUpdateFields(in OPPOUpdateInput, now time.Time) (map[string]string, error) {
	f := map[string]string{}
	for _, k := range oppoMetadataFields {
		if k == "audit_status" || k == "audit_status_name" || k == "state" || k == "apk_md5" || k == "version_id" {
			continue
		}
		if v, ok := in.Existing.Metadata[k]; ok && v != "" {
			f[k] = v
		}
	}
	for k, v := range in.Patch {
		if v == "" {
			return nil, fail("unverified_clear_semantics", "not_sent")
		}
		if _, ok := f[k]; !ok {
			allowed := false
			for _, known := range oppoMetadataFields[:len(oppoMetadataFields)-5] {
				if k == known {
					allowed = true
				}
			}
			if !allowed {
				return nil, fail("unsupported_metadata_patch", "not_sent")
			}
		}
		f[k] = v
	}
	if source := oppoCopyrightSource(f); source != "" {
		f["copyright_url"] = f[source]
	}
	if in.OnlineType != 1 && in.OnlineType != 2 {
		return nil, fail("unsupported_release_policy", "not_sent")
	}
	f["online_type"] = strconv.Itoa(in.OnlineType)
	if in.OnlineType == 2 {
		if !in.ScheduledAt.After(now) {
			return nil, fail("invalid_schedule", "not_sent")
		}
		f["sche_online_time"] = in.ScheduledAt.In(time.FixedZone("China", 8*3600)).Format("2006-01-02 15:04:05")
	} else {
		delete(f, "sche_online_time")
	}
	for _, k := range oppoRequiredMetadata {
		if err := required(f[k]); err != nil {
			return nil, err
		}
	}
	if utf8.RuneCountInString(f["summary"]) > 13 || utf8.RuneCountInString(f["detail_desc"]) < 20 || utf8.RuneCountInString(f["update_desc"]) < 5 || utf8.RuneCountInString(f["test_desc"]) > 400 {
		return nil, fail("invalid_metadata", "not_sent")
	}
	for _, r := range f["summary"] {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			return nil, fail("invalid_metadata", "not_sent")
		}
	}
	if f["adaptive_equipment"] != "4" && f["adaptive_equipment"] != "5" && f["adaptive_equipment"] != "6" {
		return nil, fail("invalid_metadata", "not_sent")
	}
	for _, k := range []string{"second_category_id", "third_category_id", "age_level"} {
		n, e := strconv.ParseInt(f[k], 10, 64)
		if e != nil || n <= 0 {
			return nil, fail("invalid_metadata", "not_sent")
		}
	}
	if len(strings.Split(f["pic_url"], ",")) < 2 {
		return nil, fail("invalid_metadata", "not_sent")
	}
	f["pkg_name"], f["version_code"] = in.PackageName, decimal(in.VersionCode)
	f["apk_url"] = string(raw([]map[string]any{{"url": in.File.URL, "md5": in.File.MD5, "cpu_code": 0}}))
	return f, nil
}
