package channels

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"sync"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// No write or upload grant can cross this boundary, including a GET that
// allocates upload resources. Public app IDs are learned only from exact lookup.
type accountReadSender struct {
	query AccountQuery
	mu    sync.Mutex
	appID string
}
type readSenderBound struct {
	guard      *accountReadSender
	underlying Sender
}

func (s readSenderBound) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	return s.guard.send(ctx, s.underlying, q)
}

func (s *accountReadSender) send(ctx context.Context, underlying Sender, q transport.Request) ([]byte, error) {
	reject := func() ([]byte, error) {
		if q.Body != nil {
			q.Body.Close()
		}
		return nil, fail("account_query_read_only", "not_sent")
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || q.OperationID != "" || q.Grant != nil || underlying == nil || q.Class == transport.Write {
		return reject()
	}
	s.mu.Lock()
	appID := s.appID
	s.mu.Unlock()
	if appID == "" {
		appID = s.query.AppID
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return reject()
	}
	exact := func(v url.Values, want map[string]string) bool {
		if len(v) != len(want) {
			return false
		}
		for k, w := range want {
			values := v[k]
			if len(values) != 1 || values[0] == "" || (w != "" && values[0] != w) {
				return false
			}
		}
		return true
	}
	var body []byte
	if q.Body != nil {
		if q.ContentLength <= 0 || q.ContentLength > 1<<20 {
			return reject()
		}
		body, err = io.ReadAll(io.LimitReader(q.Body, (1<<20)+1))
		q.Body.Close()
		if err != nil || int64(len(body)) != q.ContentLength {
			clear(body)
			return reject()
		}
		defer clear(body)
	}
	form := func() url.Values {
		v, e := url.ParseQuery(string(body))
		if e != nil {
			return nil
		}
		return v
	}
	api := u.Scheme+"://"+u.Host == origins[s.query.Channel]
	bearer := func() bool {
		return len(q.Headers) == 2 && q.Headers.Get("Authorization") != "" && q.Headers.Get("Content-Type") == "application/json; charset=utf-8"
	}
	ok := false
	lookup := false
	if s.query.Channel == "honor" && u.Host == "iam.developer.honor.com" && u.Path == "/auth/token" {
		ok = q.Class == transport.Lifecycle && q.Method == "POST" && u.RawQuery == "" && len(q.Headers) == 1 && q.Headers.Get("Content-Type") == "application/x-www-form-urlencoded; charset=utf-8" && exact(form(), map[string]string{"grant_type": "client_credentials", "client_id": s.query.ClientID, "client_secret": ""})
	} else if api {
		switch s.query.Channel {
		case "huawei":
			if q.Method == "GET" && q.Class == transport.Read && q.Body == nil && q.ContentLength == 0 && bearer() {
				if u.Path == "/api/publish/v2/appid-list" {
					ok = exact(query, map[string]string{"packageName": s.query.PackageName, "packageTypes": "1"})
					lookup = ok
				}
				if u.Path == "/api/publish/v2/app-info" {
					ok = appID != "" && exact(query, map[string]string{"appId": appID})
				}
			}
		case "honor":
			if q.Method == "GET" && q.Class == transport.Read && q.Body == nil && q.ContentLength == 0 && bearer() {
				if u.Path == "/openapi/v1/publish/get-app-id" {
					ok = exact(query, map[string]string{"pkgName": s.query.PackageName})
					lookup = ok
				}
				if u.Path == "/openapi/v1/publish/get-app-detail" || u.Path == "/openapi/v1/publish/get-app-current-release" {
					ok = appID != "" && exact(query, map[string]string{"appId": appID})
				}
			}
		case "vivo":
			if q.Method == "POST" && q.Class == transport.Query && u.Path == "/router/rest" && u.RawQuery == "" && len(q.Headers) == 1 && q.Headers.Get("Content-Type") == "application/x-www-form-urlencoded; charset=utf-8" {
				ok = exact(form(), map[string]string{"packageName": s.query.PackageName, "method": "app.query.details", "access_key": s.query.ClientID, "timestamp": "", "format": "json", "v": "1.0", "sign_method": "HMAC-SHA256", "target_app_key": "developer", "sign": ""})
			}
		case "tencent":
			if q.Method == "POST" && q.Class == transport.Query && u.Path == "/open_file/developer_api/query_app_detail" && u.RawQuery == "" && len(q.Headers) == 1 && q.Headers.Get("Content-Type") == "application/x-www-form-urlencoded; charset=utf-8" {
				ok = exact(form(), map[string]string{"pkg_name": s.query.PackageName, "app_id": s.query.AppID, "user_id": s.query.ClientID, "timestamp": "", "sign": ""})
			}
		case "xiaomi":
			if q.Method == "POST" && q.Class == transport.Query && u.Path == "/devupload/dev/query" && u.RawQuery == "" && len(q.Headers) == 1 {
				typ, params, e := mime.ParseMediaType(q.Headers.Get("Content-Type"))
				if e == nil && typ == "multipart/form-data" && params["boundary"] != "" {
					reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
					fields := map[string]string{}
					seen := map[string]bool{}
					valid := true
					for {
						part, e := reader.NextPart()
						if e == io.EOF {
							break
						}
						if e != nil {
							valid = false
							break
						}
						b, e := io.ReadAll(io.LimitReader(part, 65537))
						name := part.FormName()
						if e != nil || len(b) > 65536 || part.FileName() != "" || name == "" || seen[name] {
							valid = false
						}
						fields[name] = string(b)
						seen[name] = true
						clear(b)
						part.Close()
					}
					var data map[string]string
					signature, e := hex.DecodeString(fields["SIG"])
					ok = valid && len(fields) == 2 && json.Unmarshal([]byte(fields["RequestData"]), &data) == nil && len(data) == 2 && data["packageName"] == s.query.PackageName && data["userName"] == s.query.ClientID && e == nil && len(signature) > 0
					clear(signature)
				}
			}
		}
	}
	if !ok {
		return reject()
	}
	if body != nil {
		q.Body = io.NopCloser(bytes.NewReader(body))
		defer q.Body.Close()
	}
	response, err := underlying.Send(ctx, q)
	if err != nil {
		var network *transport.Error
		if s.query.diagnostic != nil && errors.As(err, &network) {
			s.query.diagnostic.HTTPStatus = network.Status
		}
		var oauth *honorOAuthError
		if s.query.diagnostic != nil && errors.As(err, &oauth) {
			if oauth.network != nil {
				s.query.diagnostic.HTTPStatus = oauth.network.Status
			}
			s.query.diagnostic.OAuthError = oauth.code
		}
		return nil, err
	}
	if s.query.diagnostic != nil {
		*s.query.diagnostic = queryDiagnostic(response)
	}
	if lookup {
		o, e := decode(response)
		if e == nil {
			var id string
			if s.query.Channel == "huawei" {
				rows := o.list("appids")
				if len(rows) == 1 {
					id = rows[0].id("value")
				}
			}
			if s.query.Channel == "honor" {
				rows := o.list("data")
				if len(rows) == 1 && rows[0].str("packageName") == s.query.PackageName {
					id = rows[0].id("appId")
				}
			}
			if account.ValidName(id) {
				s.mu.Lock()
				if s.appID == "" && (s.query.AppID == "" || s.query.AppID == id) {
					s.appID = id
				}
				s.mu.Unlock()
			}
		}
	}
	return response, nil
}

func queryDiagnostic(b []byte) QueryDiagnostic {
	d := QueryDiagnostic{Shape: map[string]string{}, Codes: map[string]string{}}
	o, err := decode(b)
	if err != nil {
		return d
	}
	var visit func(object, string, int)
	visit = func(o object, prefix string, depth int) {
		for _, key := range []string{"code", "subCode", "ret", "data", "appInfo", "versionCode", "VersionCode", "version_code", "packageName", "pkg_name", "error", "error_description", "access_token", "expires_in", "token_type", "success", "value"} {
			b, exists := o[key]
			if !exists {
				continue
			}
			path := prefix + key
			var value any
			if json.Unmarshal(b, &value) != nil {
				continue
			}
			typ := "null"
			switch value.(type) {
			case string:
				typ = "string"
			case float64:
				typ = "number"
			case bool:
				typ = "boolean"
			case []any:
				typ = "array"
			case map[string]any:
				typ = "object"
			}
			d.Shape[path] = typ
			if key == "code" || key == "subCode" || key == "error" || key == "ret" {
				if code := fieldText(o, key); inspectionCode.MatchString(code) {
					d.Codes[path] = code
				}
			}
			if typ == "object" && depth < 2 {
				visit(o.obj(key), path+".", depth+1)
			}
		}
	}
	visit(o, "", 0)
	return d
}
