package channels

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// The current authentication page (327) specifies HMAC-SHA256. Older endpoint
// tables still say hmac. Use the current common contract and never silently
// resend a business request with a different sign_method.
func (c *Client) vivoSigned(method string, fields map[string]string) (map[string]string, error) {
	if err := c.channel("vivo"); err != nil {
		return nil, err
	}
	secret := c.Auth.Secret.Bytes()
	defer clear(secret)
	if err := required(c.Auth.ID, string(secret)); err != nil {
		return nil, err
	}
	f := copyFields(fields)
	f["method"], f["access_key"], f["timestamp"] = method, c.Auth.ID, decimal(c.now().UnixMilli())
	f["format"], f["v"], f["sign_method"], f["target_app_key"] = "json", "1.0", "HMAC-SHA256", "developer"
	f["sign"] = hmacSign(f, secret, "sign", "file")
	return f, nil
}
func vivoCheck(o object) error {
	if err := check(o, "code"); err != nil {
		return err
	}
	if o.str("subCode") != "0" {
		if o.str("subCode") == "" {
			return unknown()
		}
		return fail("vendor_business_error", "unknown")
	}
	return nil
}

var vivoMetadataFields = []string{"onlineType", "updateDesc", "detailDesc", "icon", "screenshot", "mainTitle", "subTitle", "appClassify", "subAppClassify", "remark", "specialQualifications", "ecopyright", "copyrightList", "copyrightAuthLetter", "safetyreport", "networkCultureLicense", "privateSelfCheck", "compatibleDevice", "customerService", "video", "videoCover", "simpleDesc", "commitmentLetter", "icpRecordType", "icpLicenseType", "icpLicenseNo", "icpAuthLetter", "offInternetLetter", "rateAge", "status", "saleStatus"}

func (c *Client) VivoDetails(ctx context.Context, pkg string) (Details, error) {
	if err := required(pkg); err != nil {
		return Details{}, err
	}
	f, err := c.vivoSigned("app.query.details", map[string]string{"packageName": pkg})
	if err != nil {
		return Details{}, err
	}
	o, err := c.form(ctx, "/router/rest", transport.Query, "", f)
	if err != nil {
		return Details{}, err
	}
	if err = vivoCheck(o); err != nil {
		return Details{}, err
	}
	d := o.obj("data")
	n, ok := vivoVersionCode(d)
	if !ok || n <= 0 || d.str("packageName") != pkg {
		return Details{}, unknown()
	}
	out := Details{PackageName: pkg, VersionCode: n, VersionName: d.str("versionName"), Metadata: map[string]string{}}
	for _, k := range vivoMetadataFields {
		if _, ok := d[k]; ok {
			out.Metadata[k] = fieldText(d, k)
		}
	}
	return out, nil
}

// The documented integer and account-verified decimal string represent the
// same version identity. Never coerce fractions, exponents or rounded numbers.
func vivoVersionCode(d object) (int64, bool) {
	n, ok := d.num("versionCode")
	if !ok {
		text := d.str("versionCode")
		var err error
		n, err = strconv.ParseInt(text, 10, 64)
		ok = err == nil && strconv.FormatInt(n, 10) == text
	}
	return n, ok && n > 0
}

type VivoFile struct {
	SerialNumber, PackageName, VersionName, MD5 string
	VersionCode                                 int64
}

func (v VivoFile) String() string   { return "vivo uploaded file reference" }
func (v VivoFile) GoString() string { return v.String() }
func (c *Client) VivoUpload(ctx context.Context, op, pkg string, file File) (VivoFile, error) {
	if err := required(op, pkg); err != nil {
		return VivoFile{}, err
	}
	if file.size <= 0 || file.size > 3*1024*1024*1024 {
		return VivoFile{}, fail("unsupported_artifact_size", "not_sent")
	}
	f, err := c.vivoSigned("app.upload.apk.app", map[string]string{"packageName": pkg, "fileMd5": file.md5})
	if err != nil {
		return VivoFile{}, err
	}
	o, err := c.multipart(ctx, "/router/rest", transport.Write, op, f, map[string]File{"file": file}, nil)
	if err != nil {
		return VivoFile{}, err
	}
	return parseVivoUploadedFile(o, pkg, file)
}

func parseVivoUploadedFile(o object, pkg string, file File) (VivoFile, error) {
	if err := vivoCheck(o); err != nil {
		return VivoFile{}, err
	}
	d := o.obj("data")
	n, ok := vivoVersionCode(d)
	if d.str("packageName") != pkg || d.str("serialnumber") == "" || !ok || n <= 0 || !strings.EqualFold(d.str("fileMd5"), file.md5) {
		return VivoFile{}, unknown()
	}
	return VivoFile{d.str("serialnumber"), pkg, d.str("versionName"), file.md5, n}, nil
}

type VivoUpdateInput struct {
	PackageName                  string
	VersionCode                  int64
	Existing                     Details
	File                         VivoFile
	Patch                        map[string]string
	OnlineType, CompatibleDevice int
	ScheduledAt                  time.Time
}

func (c *Client) VivoUpdate(ctx context.Context, op string, in VivoUpdateInput) (Acceptance, error) {
	if err := c.channel("vivo"); err != nil {
		return Acceptance{}, err
	}
	if err := required(op, in.PackageName, in.File.SerialNumber, in.File.MD5); err != nil {
		return Acceptance{}, err
	}
	if in.Existing.PackageName != in.PackageName || in.Existing.VersionCode <= 0 || in.VersionCode <= in.Existing.VersionCode || in.File.PackageName != in.PackageName || in.File.VersionCode != in.VersionCode || !channelMD5(in.File.MD5) {
		return Acceptance{}, fail("target_mismatch", "not_sent")
	}
	if (in.OnlineType != 1 && in.OnlineType != 2) || in.CompatibleDevice < 1 || in.CompatibleDevice > 3 {
		return Acceptance{}, fail("unsupported_release_policy", "not_sent")
	}
	f, err := vivoUpdateFields(in, c.now())
	if err != nil {
		return Acceptance{}, err
	}
	f, err = c.vivoSigned("app.sync.update.app", f)
	if err != nil {
		return Acceptance{}, err
	}
	o, err := c.form(ctx, "/router/rest", transport.Write, op, f)
	if err != nil {
		return Acceptance{}, err
	}
	if err = vivoCheck(o); err != nil {
		return Acceptance{}, err
	}
	return ack(op), nil
}

// VivoStatus associates observations with an exact version. The details API has
// no documented APK digest or operation identifier, so it cannot reconcile an
// unknown update write or establish that this executor's metadata was applied.
func (c *Client) VivoStatus(ctx context.Context, pkg string, version int64) (Status, error) {
	if version <= 0 {
		return Status{}, fail("invalid_version", "not_sent")
	}
	d, err := c.VivoDetails(ctx, pkg)
	if err != nil {
		return Status{}, err
	}
	if d.VersionCode != version {
		return Status{}, unknown()
	}
	s := Status{VersionCode: version, Review: "unknown", Availability: "unknown"}
	switch d.Metadata["status"] {
	case "1":
		s.Review = "unknown"
	case "2":
		s.Review = "in_review"
	case "3":
		s.Review = "approved"
	case "4":
		s.Review = "rejected"
	case "5":
		s.Review = "withdrawn"
	}
	switch d.Metadata["saleStatus"] {
	case "0":
		s.Availability = "not_observed_live"
	case "1":
		if s.Review == "approved" {
			s.Availability = "live"
		}
	case "2":
		s.Availability = "not_observed_live"
	}
	return s, nil
}

// vivoUpdateFields validates frozen metadata without credentials or network.
func vivoUpdateFields(in VivoUpdateInput, now time.Time) (map[string]string, error) {
	if (in.OnlineType != 1 && in.OnlineType != 2) || in.CompatibleDevice < 1 || in.CompatibleDevice > 3 {
		return nil, fail("unsupported_release_policy", "not_sent")
	}
	f := map[string]string{}
	allowed := map[string]bool{}
	for _, k := range vivoMetadataFields {
		if k == "status" || k == "saleStatus" {
			continue
		}
		allowed[k] = true
		// Details returns viewing URLs for attachments; update expects upload
		// serial numbers. Omit unchanged attachments rather than echoing URLs
		// into fields with a different representation.
		switch k {
		case "icon", "screenshot", "specialQualifications", "ecopyright", "copyrightList", "copyrightAuthLetter", "safetyreport", "video", "videoCover", "commitmentLetter", "icpAuthLetter", "offInternetLetter":
			continue
		}
		if v, ok := in.Existing.Metadata[k]; ok {
			f[k] = v
		}
	}
	for k, v := range in.Patch {
		if v == "" {
			return nil, fail("unverified_clear_semantics", "not_sent")
		}
		if !allowed[k] {
			return nil, fail("unsupported_metadata_patch", "not_sent")
		}
		f[k] = v
	}
	proof := copyFields(in.Existing.Metadata)
	for k, v := range in.Patch {
		proof[k] = v
	}
	if proof["ecopyright"] == "" && proof["copyrightList"] == "" {
		return nil, fail("missing_required_input", "not_sent")
	}
	if utf8.RuneCountInString(f["mainTitle"])+utf8.RuneCountInString(f["subTitle"]) > 20 || (f["subTitle"] != "" && (f["mainTitle"] == "" || strings.HasSuffix(f["mainTitle"], "+"))) {
		return nil, fail("invalid_metadata", "not_sent")
	}
	if v, ok := f["updateDesc"]; ok && (utf8.RuneCountInString(v) < 5 || utf8.RuneCountInString(v) > 200) {
		return nil, fail("invalid_metadata", "not_sent")
	}
	if v, ok := f["detailDesc"]; ok && (utf8.RuneCountInString(v) < 50 || utf8.RuneCountInString(v) > 1000) {
		return nil, fail("invalid_metadata", "not_sent")
	}
	if v, ok := f["remark"]; ok && v != "" && (utf8.RuneCountInString(v) < 10 || utf8.RuneCountInString(v) > 200) {
		return nil, fail("invalid_metadata", "not_sent")
	}
	f["packageName"], f["versionCode"], f["apk"], f["fileMd5"] = in.PackageName, decimal(in.VersionCode), in.File.SerialNumber, in.File.MD5
	f["onlineType"], f["compatibleDevice"] = strconv.Itoa(in.OnlineType), strconv.Itoa(in.CompatibleDevice)
	if in.OnlineType == 2 {
		if !in.ScheduledAt.After(now) {
			return nil, fail("invalid_schedule", "not_sent")
		}
		f["scheOnlineTime"] = in.ScheduledAt.In(time.FixedZone("China", 8*3600)).Format("2006-01-02 15:04:05")
	}
	return f, nil
}
