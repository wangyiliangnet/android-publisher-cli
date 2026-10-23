package channels

import (
	"bytes"
	"context"
	"io"
	"net/url"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// This boundary is installed before credential/token resolution. Preflight can
// never acquire a grant, even if a caller misclassifies that GET as a read.
type oppoProductionSender struct {
	underlying Sender
	target     Target
	writable   bool
}

func (s oppoProductionSender) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	reject := func() ([]byte, error) {
		if q.Body != nil {
			q.Body.Close()
		}
		return nil, fail("oppo_production_request_rejected", "not_sent")
	}
	if q.Class == transport.Lifecycle {
		return (inspectionSender{underlying: s.underlying, clientID: s.target.Credential.ID, pkg: s.target.PackageName}).Send(ctx, q)
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || s.underlying == nil {
		return reject()
	}
	op := func(kind string) string {
		return protocol.Digest([]string{s.target.IntentDigest, "oppo", kind})
	}
	if q.Grant != nil {
		if !s.writable || q.Class != transport.Write || q.OperationID != op("upload") || q.Method != "POST" || q.Grant.URL != q.URL || q.Grant.Method != "POST" || !allowedUpload("oppo", q.URL) || q.Body == nil || q.ContentLength <= s.target.APK.Size() || len(q.Headers) != 1 || !strings.HasPrefix(q.Headers.Get("Content-Type"), "multipart/form-data; boundary=") {
			return reject()
		}
		return s.underlying.Send(ctx, q)
	}
	if u.Scheme+"://"+u.Host != origins["oppo"] {
		return reject()
	}
	validValues := func(v url.Values, count int) bool {
		if len(v) != count {
			return false
		}
		for _, values := range v {
			if len(values) != 1 || values[0] == "" {
				return false
			}
		}
		return v.Get("access_token") != "" && v.Get("timestamp") != "" && v.Get("api_sign") != ""
	}
	if q.Method == "GET" && q.Body == nil && q.ContentLength == 0 && len(q.Headers) == 0 {
		values, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return reject()
		}
		switch u.Path {
		case "/resource/v1/app/info":
			count := 4
			if _, ok := values["version_code"]; ok {
				count++
				if values.Get("version_code") != decimal(s.target.VersionCode) {
					return reject()
				}
			}
			if q.Class != transport.Read || q.OperationID != "" || !validValues(values, count) || values.Get("pkg_name") != s.target.PackageName {
				return reject()
			}
		case "/resource/v1/upload/get-upload-url":
			if !s.writable || q.Class != transport.Write || q.OperationID != op("authorize") || !validValues(values, 3) {
				return reject()
			}
		default:
			return reject()
		}
		return s.underlying.Send(ctx, q)
	}
	if q.Method != "POST" || u.RawQuery != "" || q.Body == nil || q.ContentLength <= 0 || q.ContentLength > 256<<10 || len(q.Headers) != 1 || q.Headers.Get("Content-Type") != "application/x-www-form-urlencoded; charset=utf-8" {
		return reject()
	}
	isTask := u.Path == "/resource/v1/app/task-state" && q.Class == transport.Query && q.OperationID == ""
	isSubmit := s.writable && u.Path == "/resource/v1/app/upd" && q.Class == transport.Write && q.OperationID == op("submit")
	if !isTask && !isSubmit {
		return reject()
	}
	b, err := io.ReadAll(io.LimitReader(q.Body, (256<<10)+1))
	q.Body.Close()
	defer clear(b)
	if err != nil || int64(len(b)) != q.ContentLength {
		return reject()
	}
	values, err := url.ParseQuery(string(b))
	if err != nil || !validValues(values, len(values)) || values.Get("pkg_name") != s.target.PackageName || values.Get("version_code") != decimal(s.target.VersionCode) {
		return reject()
	}
	if isTask && len(values) != 5 {
		return reject()
	}
	if isSubmit {
		if values.Get("online_type") != "1" || values.Has("sche_online_time") || values.Get("update_desc") != s.target.Notes || values.Get("apk_url") == "" {
			return reject()
		}
		for key := range values {
			if key == "pkg_name" || key == "version_code" || key == "apk_url" || key == "access_token" || key == "timestamp" || key == "api_sign" {
				continue
			}
			known := false
			for _, field := range oppoMetadataFields {
				known = known || key == field
			}
			if !known {
				return reject()
			}
		}
	}
	q.Body = io.NopCloser(bytes.NewReader(b))
	defer q.Body.Close()
	return s.underlying.Send(ctx, q)
}
