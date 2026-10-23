package channels

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"reflect"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// A second boundary limits the controlled transport to the exact endpoint,
// operation kind and application of this frozen adapter. Token lifecycle remains
// under account coordination; read-only construction cannot allocate uploads.
type fiveProductionSender struct {
	underlying Sender
	target     Target
	writable   bool
}

func (s fiveProductionSender) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	reject := func() ([]byte, error) {
		if q.Body != nil {
			q.Body.Close()
		}
		return nil, fail("production_request_rejected", "not_sent")
	}
	t := s.target
	u, e := url.Parse(q.URL)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || s.underlying == nil {
		return reject()
	}
	config := AccountQuery{Channel: t.Account.Channel, Account: t.Account.Account, ClientID: t.Credential.ID, SecretName: t.Credential.SecretName, PackageName: t.PackageName, AppID: t.AppID, LanguageID: t.LanguageID}
	// Public account reads have their own strict method/body/identity allowlist.
	if q.Class == transport.Lifecycle || q.Class == transport.Read || q.Class == transport.Query && !(t.Account.Channel == "honor" && u.Path == "/openapi/v1/publish/get-audit-result" || t.Account.Channel == "tencent" && u.Path == "/open_file/developer_api/query_app_update_status") {
		return (&accountReadSender{query: config}).send(ctx, s.underlying, q)
	}
	op := func(kind string) bool {
		return q.OperationID == protocol.Digest([]string{t.IntentDigest, t.Account.Channel, kind})
	}
	if q.Grant != nil {
		if !s.writable || q.Class != transport.Write || !op("upload") || q.Method != "PUT" || q.Body == nil || q.ContentLength != t.APK.Size() || q.Grant.URL != q.URL || q.Grant.Method != q.Method || !allowedUpload(t.Account.Channel, q.URL) || (t.Account.Channel != "huawei" && t.Account.Channel != "tencent") {
			return reject()
		}
		return s.underlying.Send(ctx, q)
	}
	if u.Scheme+"://"+u.Host != origins[t.Account.Channel] {
		return reject()
	}
	values, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return reject()
	}
	exact := func(want map[string]string) bool {
		if len(values) != len(want) {
			return false
		}
		for k, v := range want {
			if len(values[k]) != 1 || values.Get(k) != v {
				return false
			}
		}
		return true
	}
	if q.Class == transport.Query && q.OperationID == "" && q.Method == "POST" && u.RawQuery == "" {
		b, err := fiveSmallBody(&q)
		if err != nil {
			return reject()
		}
		defer clear(b)
		valid := false
		if t.Account.Channel == "tencent" {
			f, err := url.ParseQuery(string(b))
			valid = err == nil && len(q.Headers) == 1 && q.Headers.Get("Content-Type") == "application/x-www-form-urlencoded; charset=utf-8" && fiveExactForm(f, map[string]string{"pkg_name": t.PackageName, "app_id": t.AppID, "user_id": t.Credential.ID, "timestamp": f.Get("timestamp"), "sign": f.Get("sign")}) && f.Get("timestamp") != "" && f.Get("sign") != ""
		} else if t.Account.Channel == "honor" {
			o, err := decode(b)
			rows := o.list("appId")
			valid = err == nil && len(q.Headers) == 2 && q.Headers.Get("Authorization") != "" && q.Headers.Get("Content-Type") == "application/json; charset=utf-8" && len(o) == 1 && len(rows) == 1 && rows[0].id("appId") == t.AppID && rows[0].str("releaseId") != "" && len(rows[0]) == 2
		}
		if !valid {
			return reject()
		}
		return s.underlying.Send(ctx, q)
	}
	if !s.writable || q.Class != transport.Write {
		return reject()
	}
	valid := false
	switch t.Account.Channel {
	case "huawei":
		bearer := len(q.Headers) == 2 && q.Headers.Get("Authorization") != "" && q.Headers.Get("Content-Type") == "application/json; charset=utf-8"
		if !bearer {
			return reject()
		}
		switch u.Path {
		case "/api/publish/v2/upload-url/for-obs":
			want := map[string]string{"appId": t.AppID, "fileName": t.APK.Name(), "sha256": t.APK.SHA256(), "contentLength": decimal(t.APK.Size()), "releaseType": "1"}
			if t.Mainland != nil {
				want["chineseMainlandFlag"] = "0"
				if *t.Mainland {
					want["chineseMainlandFlag"] = "1"
				}
			}
			valid = op("authorize") && q.Method == "GET" && q.Body == nil && q.ContentLength == 0 && exact(want)
		case "/api/publish/v2/app-submit":
			valid = op("submit") && q.Method == "POST" && q.Body == nil && q.ContentLength == 0 && exact(map[string]string{"appId": t.AppID, "releaseType": "1"})
		case "/api/publish/v2/app-file-info", "/api/publish/v2/app-language-info":
			want := map[string]string{"appId": t.AppID}
			if op("metadata") {
				want["releaseType"] = "1"
			}
			if q.Method != "PUT" || !exact(want) {
				return reject()
			}
			b, err := fiveSmallBody(&q)
			if err != nil {
				return reject()
			}
			defer clear(b)
			o, err := decode(b)
			if err != nil {
				return reject()
			}
			if op("bind") && u.Path == "/api/publish/v2/app-file-info" {
				rows := o.list("files")
				n, ok := o.num("fileType")
				valid = ok && n == 5 && len(o) == 2 && len(rows) == 1 && len(rows[0]) == 2 && rows[0].str("fileName") == t.APK.Name() && rows[0].str("fileDestUrl") != ""
			}
			if op("metadata") && u.Path == "/api/publish/v2/app-language-info" {
				var fields map[string]string
				valid = json.Unmarshal(b, &fields) == nil && reflect.DeepEqual(fields, map[string]string{"lang": t.LanguageID, "appName": t.Metadata["lang_app_name"], "appDesc": t.Metadata["lang_intro"], "briefInfo": t.Metadata["lang_brief"], "newFeatures": t.Notes})
			}
		}
	case "honor":
		if len(q.Headers) != 2 || q.Headers.Get("Authorization") == "" || q.Method != "POST" {
			return reject()
		}
		if u.Path == "/openapi/v1/publish/file-upload" {
			valid = op("upload") && len(values) == 2 && len(values["appId"]) == 1 && values.Get("appId") == t.AppID && len(values["objectId"]) == 1 && values.Get("objectId") != "" && q.ContentLength > t.APK.Size()
			if valid {
				fields, err := fiveMultipartFields(&q, t)
				valid = err == nil && len(fields) == 0
			}
			break
		}
		if q.Headers.Get("Content-Type") != "application/json; charset=utf-8" || !exact(map[string]string{"appId": t.AppID}) {
			return reject()
		}
		b, err := fiveSmallBody(&q)
		if err != nil {
			return reject()
		}
		defer clear(b)
		o, err := decode(b)
		switch u.Path {
		case "/openapi/v1/publish/get-file-upload-url":
			var rows []map[string]json.RawMessage
			if op("authorize") && uniqueJSON(b) && json.Unmarshal(b, &rows) == nil && len(rows) == 1 {
				a := object(rows[0])
				typ, ok := a.num("fileType")
				size, sizeOK := a.num("fileSize")
				valid = len(a) == 4 && ok && typ == 100 && sizeOK && size == t.APK.Size() && a.str("fileName") == t.APK.Name() && a.str("fileSha256") == t.APK.SHA256()
			}
		case "/openapi/v1/publish/update-file-info":
			rows := o.list("bindingFileList")
			valid = op("bind") && err == nil && len(o) == 1 && len(rows) == 1 && len(rows[0]) == 1 && rows[0].id("objectId") != ""
		case "/openapi/v1/publish/submit-audit":
			n, ok := o.num("releaseType")
			valid = op("submit") && err == nil && len(o) == 1 && ok && n == 1
		case "/openapi/v1/publish/update-language-info":
			want := map[string]any{"setAll": 0, "languageInfoList": []map[string]string{{"languageId": t.LanguageID, "appName": t.Metadata["lang_app_name"], "intro": t.Metadata["lang_intro"], "briefIntro": t.Metadata["lang_brief"], "newFeature": t.Notes}}}
			valid = op("metadata") && err == nil && protocol.Digest(json.RawMessage(b)) == protocol.Digest(json.RawMessage(raw(want)))
		}
	case "tencent":
		if q.Method != "POST" || u.RawQuery != "" || len(q.Headers) != 1 || q.Headers.Get("Content-Type") != "application/x-www-form-urlencoded; charset=utf-8" {
			return reject()
		}
		b, err := fiveSmallBody(&q)
		if err != nil {
			return reject()
		}
		defer clear(b)
		f, err := url.ParseQuery(string(b))
		if err != nil {
			return reject()
		}
		for _, values := range f {
			if len(values) != 1 {
				return reject()
			}
		}
		if f.Get("pkg_name") != t.PackageName || f.Get("app_id") != t.AppID || f.Get("user_id") != t.Credential.ID || f.Get("timestamp") == "" || f.Get("sign") == "" {
			return reject()
		}
		if u.Path == "/open_file/developer_api/get_file_upload_info" {
			valid = op("authorize") && fiveExactForm(f, map[string]string{"pkg_name": t.PackageName, "app_id": t.AppID, "user_id": t.Credential.ID, "timestamp": f.Get("timestamp"), "sign": f.Get("sign"), "file_type": "apk", "file_name": t.APK.Name()})
		}
		if u.Path == "/open_file/developer_api/update_app" {
			slot, err := tencentSlot(t.ABI)
			want := map[string]string{"pkg_name": t.PackageName, "app_id": t.AppID, "user_id": t.Credential.ID, "timestamp": f.Get("timestamp"), "sign": f.Get("sign"), "feature": t.Notes, "deploy_type": "1", "apk32_flag": "2", "apk64_flag": "2", "apk" + slot + "_file_md5": t.APK.MD5(), "apk" + slot + "_file_serial_number": f.Get("apk" + slot + "_file_serial_number")}
			want["apk"+slot+"_flag"] = "1"
			valid = err == nil && op("submit") && fiveExactForm(f, want) && f.Get("apk"+slot+"_file_serial_number") != ""
		}
	case "vivo":
		if q.Method != "POST" || u.Path != "/router/rest" || u.RawQuery != "" || len(q.Headers) != 1 {
			return reject()
		}
		var f url.Values
		if op("upload") {
			fields, err := fiveMultipartFields(&q, t)
			if err != nil {
				return reject()
			}
			f = url.Values{}
			for k, v := range fields {
				f.Set(k, v)
			}
			valid = f.Get("method") == "app.upload.apk.app" && len(f) == 10 && f.Get("fileMd5") == t.APK.MD5() && q.ContentLength > t.APK.Size()
		}
		if op("submit") {
			if q.Headers.Get("Content-Type") != "application/x-www-form-urlencoded; charset=utf-8" {
				return reject()
			}
			b, err := fiveSmallBody(&q)
			if err != nil {
				return reject()
			}
			defer clear(b)
			f, err = url.ParseQuery(string(b))
			if err != nil {
				return reject()
			}
			expected, err := vivoUpdateFields(VivoUpdateInput{PackageName: t.PackageName, VersionCode: t.VersionCode, Existing: t.Existing, File: VivoFile{SerialNumber: f.Get("apk"), MD5: t.APK.MD5()}, Patch: map[string]string{"updateDesc": t.Notes}, OnlineType: 1, CompatibleDevice: t.CompatibleDevice}, time.Time{})
			valid = err == nil && f.Get("method") == "app.sync.update.app" && f.Get("apk") != "" && len(f) == len(expected)+8
			for k, v := range expected {
				valid = valid && f.Get(k) == v && len(f[k]) == 1
			}
		}
		valid = valid && f.Get("packageName") == t.PackageName && f.Get("access_key") == t.Credential.ID && f.Get("timestamp") != "" && f.Get("sign") != "" && f.Get("format") == "json" && f.Get("v") == "1.0" && f.Get("sign_method") == "HMAC-SHA256" && f.Get("target_app_key") == "developer"
		for _, values := range f {
			valid = valid && len(values) == 1
		}
	case "xiaomi":
		if !op("push") || q.Method != "POST" || u.Path != "/devupload/dev/push" || u.RawQuery != "" || len(q.Headers) != 1 || q.ContentLength <= t.APK.Size()+t.Icon.Size() {
			return reject()
		}
		f, err := fiveMultipartFields(&q, t)
		if err != nil {
			return reject()
		}
		o, err := decode([]byte(f["RequestData"]))
		info := o.obj("appInfo")
		n, ok := o.num("synchroType")
		signature, se := hex.DecodeString(f["SIG"])
		clear(signature)
		valid = err == nil && se == nil && len(f) == 2 && len(signature) > 0 && len(o) == 3 && o.str("userName") == t.Credential.ID && ok && n == 1 && len(info) == 5 && info.str("packageName") == t.PackageName && info.str("appName") == t.Metadata["app_name"] && info.str("privacyUrl") == t.Metadata["privacy_url"] && info.str("updateDesc") == t.Notes && info.str("versionName") == t.VersionName
	}
	if !valid {
		return reject()
	}
	return s.underlying.Send(ctx, q)
}

func fiveSmallBody(q *transport.Request) ([]byte, error) {
	if q.Body == nil || q.ContentLength <= 0 || q.ContentLength > 256<<10 {
		return nil, fail("production_body_invalid", "not_sent")
	}
	b, e := io.ReadAll(io.LimitReader(q.Body, (256<<10)+1))
	q.Body.Close()
	if e != nil || int64(len(b)) != q.ContentLength {
		clear(b)
		return nil, fail("production_body_invalid", "not_sent")
	}
	q.Body = io.NopCloser(bytes.NewReader(b))
	return b, nil
}

func fiveExactForm(v url.Values, want map[string]string) bool {
	if len(v) != len(want) {
		return false
	}
	for k, value := range want {
		if len(v[k]) != 1 || v.Get(k) != value {
			return false
		}
	}
	return true
}

type fivePrefixReader struct {
	reader io.Reader
	prefix bytes.Buffer
}

func (r *fivePrefixReader) Read(b []byte) (int, error) {
	n, e := r.reader.Read(b)
	if r.prefix.Len()+n > 64<<10 {
		return 0, io.ErrShortBuffer
	}
	r.prefix.Write(b[:n])
	return n, e
}

type fiveRestoredBody struct {
	io.Reader
	io.Closer
}

// Read only the bounded text fields and first file header, then put the consumed
// prefix back. APK bytes continue streaming; they are never buffered in memory.
func fiveMultipartFields(q *transport.Request, t Target) (map[string]string, error) {
	typ, params, e := mime.ParseMediaType(q.Headers.Get("Content-Type"))
	if e != nil || typ != "multipart/form-data" || params["boundary"] == "" || q.Body == nil {
		return nil, fail("production_multipart_invalid", "not_sent")
	}
	original := q.Body
	r := &fivePrefixReader{reader: original}
	defer func() {
		q.Body = &fiveRestoredBody{Reader: io.MultiReader(bytes.NewReader(r.prefix.Bytes()), original), Closer: original}
	}()
	m := multipart.NewReader(r, params["boundary"])
	fields := map[string]string{}
	seen := map[string]bool{}
	for {
		part, e := m.NextPart()
		if e != nil {
			return nil, fail("production_multipart_invalid", "not_sent")
		}
		if part.FileName() != "" {
			name := "file"
			if t.Account.Channel == "xiaomi" {
				name = "apk"
			}
			if part.FormName() != name || part.FileName() != t.APK.Name() {
				return nil, fail("production_multipart_invalid", "not_sent")
			}
			return fields, nil
		}
		name := part.FormName()
		if name == "" || seen[name] || len(fields) > 50 {
			return nil, fail("production_multipart_invalid", "not_sent")
		}
		b, e := io.ReadAll(io.LimitReader(part, 32<<10))
		if e != nil || len(b) >= 32<<10 {
			return nil, fail("production_multipart_invalid", "not_sent")
		}
		fields[name] = string(b)
		seen[name] = true
		clear(b)
	}
}
