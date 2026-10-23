package channels

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func (c *Client) tencent(ctx context.Context, path string, class transport.Class, op string, business map[string]string) (object, error) {
	if err := c.channel("tencent"); err != nil {
		return nil, err
	}
	if c.Auth.Secret == nil || c.Auth.ID == "" {
		return nil, fail("invalid_credentials", "not_sent")
	}
	f := copyFields(business)
	f["user_id"] = c.Auth.ID
	f["timestamp"] = decimal(c.now().Unix())
	f["sign"] = hmacSign(f, c.Auth.Secret.Bytes(), "sign")
	o, err := c.form(ctx, "/open_file/developer_api/"+path, class, op, f)
	if err == nil {
		err = check(o, "ret")
	}
	return o, err
}
func (c *Client) TencentDetails(ctx context.Context, pkg, appID string) (Details, error) {
	if err := required(pkg, appID); err != nil {
		return Details{}, err
	}
	o, err := c.tencent(ctx, "query_app_detail", transport.Query, "", map[string]string{"pkg_name": pkg, "app_id": appID})
	if err != nil {
		return Details{}, err
	}
	// The documentation uses PascalCase; account-verified responses use
	// snake_case. Validate every present alias instead of masking a malformed
	// documented field with a fallback or choosing between conflicting values.
	var n int64
	for _, key := range []string{"VersionCode", "version_code"} {
		if _, exists := o[key]; !exists {
			continue
		}
		value, ok := o.num(key)
		if !ok || value <= 0 || (n != 0 && n != value) {
			return Details{}, unknown()
		}
		n = value
	}
	if n <= 0 || o.str("pkg_name") != pkg {
		return Details{}, unknown()
	}
	return Details{PackageName: pkg, AppID: appID, VersionCode: n, VersionName: o.str("VersionName"), Metadata: map[string]string{"app_name": o.str("app_name"), "description": o.str("introduce")}}, nil
}
func (c *Client) TencentAuthorize(ctx context.Context, op, pkg, appID string, file File) (UploadGrant, error) {
	if err := required(op, pkg, appID, file.sha256); err != nil {
		return UploadGrant{}, err
	}
	o, err := c.tencent(ctx, "get_file_upload_info", transport.Write, op, map[string]string{"pkg_name": pkg, "app_id": appID, "file_type": "apk", "file_name": file.name})
	if err != nil {
		return UploadGrant{}, err
	}
	return parseTencentUploadGrant(o, c.now())
}

func parseTencentUploadGrant(o object, now time.Time) (UploadGrant, error) {
	if err := check(o, "ret"); err != nil {
		return UploadGrant{}, err
	}
	u, err := url.Parse(o.str("pre_sign_url"))
	if err != nil || !allowedUpload("tencent", u.String()) || o.str("serial_number") == "" {
		return UploadGrant{}, unknown()
	}
	// COS signs start;end in seconds. Do not invent a one-hour lifetime.
	// Official COS examples include literal semicolons as well as %3B. Go's
	// form-query parser rejects raw semicolons; escape only for inspection and
	// preserve the original opaque URL for the upload and grant identity.
	query, err := url.ParseQuery(strings.ReplaceAll(u.RawQuery, ";", "%3B"))
	if err != nil || len(query["q-sign-time"]) != 1 {
		return UploadGrant{}, unknown()
	}
	parts := strings.Split(query.Get("q-sign-time"), ";")
	if len(parts) != 2 {
		return UploadGrant{}, unknown()
	}
	start, startErr := strconv.ParseInt(parts[0], 10, 64)
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if startErr != nil || err != nil || start < 0 || decimal(start) != parts[0] || decimal(end) != parts[1] || end <= start || end <= now.Unix() {
		return UploadGrant{}, fail("upload_grant_expired_after_allocation", "unknown")
	}
	return UploadGrant{URL: u.String(), Ref: o.str("serial_number"), ExpiresAt: time.Unix(end, 0), Method: "PUT"}, nil
}
func (c *Client) TencentUpload(ctx context.Context, op string, g UploadGrant, file File) (Acceptance, error) {
	if err := c.channel("tencent"); err != nil {
		return Acceptance{}, err
	}
	if err := c.uploadRaw(ctx, op, g, file); err != nil {
		return Acceptance{}, err
	}
	return Acceptance{g.Ref, "file_serial"}, nil
}

type TencentUpdateInput struct {
	PackageName, AppID, Serial, MD5, Notes string
	ABI                                    []string
	Policy                                 manifest.Policy
	Patch                                  map[string]manifest.Patch
}

func policyFields(policy manifest.Policy, now time.Time) (string, string, error) {
	switch policy.Mode {
	case "after_approval":
		if policy.At == "" {
			return "1", "", nil
		}
	case "scheduled":
		at, err := time.Parse(time.RFC3339, policy.At)
		if err == nil && at.After(now) {
			return "2", decimal(at.Unix()), nil
		}
	}
	return "", "", fail("unsupported_release_policy", "not_sent")
}
func (c *Client) TencentUpdate(ctx context.Context, op string, in TencentUpdateInput) (Acceptance, error) {
	if err := required(op, in.PackageName, in.AppID, in.Serial, in.MD5, in.Notes); err != nil {
		return Acceptance{}, err
	}
	typ, at, err := policyFields(in.Policy, c.now())
	if err != nil {
		return Acceptance{}, err
	}
	f := map[string]string{"pkg_name": in.PackageName, "app_id": in.AppID, "feature": in.Notes, "deploy_type": typ}
	if at != "" {
		f["deploy_time"] = at
	}
	slot, err := tencentSlot(in.ABI)
	if err != nil {
		return Acceptance{}, err
	}
	f["apk32_flag"] = "2"
	f["apk64_flag"] = "2"
	f["apk"+slot+"_flag"] = "1"
	f["apk"+slot+"_file_serial_number"] = in.Serial
	f["apk"+slot+"_file_md5"] = in.MD5
	allowed := map[string]bool{"app_name": true, "introduce": true, "one_word_summary": true}
	for k, p := range in.Patch {
		if !allowed[k] {
			return Acceptance{}, fail("unsupported_metadata_patch", "not_sent")
		}
		switch p.Action {
		case "keep":
		case "set":
			if p.Value == "" {
				return Acceptance{}, fail("unverified_clear_semantics", "not_sent")
			}
			f[k] = p.Value
		default:
			return Acceptance{}, fail("unverified_clear_semantics", "not_sent")
		}
	}
	_, err = c.tencent(ctx, "update_app", transport.Write, op, f)
	if err != nil {
		return Acceptance{}, err
	}
	return ack(op), nil
}

func tencentSlot(abis []string) (string, error) {
	var has32, has64 bool
	for _, abi := range abis {
		switch abi {
		case "armeabi-v7a", "x86":
			has32 = true
		case "arm64-v8a", "x86_64":
			has64 = true
		default:
			return "", fail("unsupported_abi", "not_sent")
		}
	}
	if !has32 && !has64 {
		return "", fail("missing_abi", "not_sent")
	}
	slot := "64"
	if has32 {
		slot = "32"
	}
	return slot, nil
}
func (c *Client) TencentAudit(ctx context.Context, pkg, appID string) (Status, error) {
	if err := required(pkg, appID); err != nil {
		return Status{}, err
	}
	o, err := c.tencent(ctx, "query_app_update_status", transport.Query, "", map[string]string{"pkg_name": pkg, "app_id": appID})
	if err != nil {
		return Status{}, err
	}
	n, ok := o.num("audit_status")
	if !ok || n < 0 {
		return Status{}, unknown()
	}
	// The audit slot has neither version nor release ID. Even approved must not
	// become an observation for this immutable target; M6 supplies that evidence.
	audit := map[int64]string{1: "in_review", 2: "rejected", 3: "approved", 8: "withdrawn"}[n]
	if audit == "" {
		audit = "unknown"
	}
	details := &protocol.ChannelContext{AuditCode: decimal(n), AuditStatus: audit, Association: "unconfirmed", APIQueryHealth: "healthy", StoreQueryHealth: "not_queried"}
	if audit == "rejected" {
		details.RejectionReason = protocol.SanitizeContextReason(o.str("audit_reason"))
	}
	return Status{Review: "unknown", Availability: "unknown", Context: details}, nil
}
