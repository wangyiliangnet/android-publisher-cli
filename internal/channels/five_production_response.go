package channels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// FiveWriteDiagnostic exposes only bounded protocol codes, schema shapes and
// vendor-owned DNS names. Raw descriptions, headers and signed URLs stay sealed.
type FiveWriteDiagnostic struct {
	AuthorizationGeneration int               `json:"authorization_generation,omitempty"`
	OperationID             string            `json:"operation_id"`
	Step                    string            `json:"step"`
	HTTPStatus              int               `json:"http_status,omitempty"`
	Complete                bool              `json:"response_complete"`
	ResponseRef             string            `json:"private_response_ref,omitempty"`
	Recording               string            `json:"recording"`
	TransportCode           string            `json:"transport_code,omitempty"`
	Codes                   map[string]string `json:"vendor_codes,omitempty"`
	Shape                   map[string]string `json:"response_shape,omitempty"`
	UploadHost              string            `json:"upload_host,omitempty"`
	UploadScheme            string            `json:"upload_scheme,omitempty"`
	GrantChecks             map[string]bool   `json:"grant_checks,omitempty"`
	GrantAuthKind           string            `json:"grant_auth_kind,omitempty"`
}

type fiveResponseEnvelope struct {
	SchemaVersion                                    int
	Scope, Intent, OperationID, Step, AdapterBinding string
	HTTPStatus                                       int
	Complete                                         bool
	CollectedAt                                      time.Time
	StartedAt                                        time.Time
	Request                                          protocol.Request
	Body                                             []byte
}

func inspectionJSONType(b json.RawMessage) string {
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "missing"
	}
	switch value[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 'n':
		return "null"
	case 't', 'f':
		return "boolean"
	default:
		return "number"
	}
}

func (p *PreparedProduction) LastWriteDiagnostic() *FiveWriteDiagnostic {
	if p.diagnostic == nil || p.diagnostic.OperationID == "" {
		return nil
	}
	d := *p.diagnostic
	d.Codes, d.Shape = copyFields(d.Codes), copyFields(d.Shape)
	return &d
}

func fiveResponseDiagnostic(t Target, step, op string, status int, complete bool, body []byte) FiveWriteDiagnostic {
	d := FiveWriteDiagnostic{OperationID: op, Step: step, HTTPStatus: status, Complete: complete, Recording: "not_saved"}
	q := queryDiagnostic(body)
	d.Codes, d.Shape = q.Codes, q.Shape
	o, e := decode(body)
	if e != nil {
		return d
	}
	// These are fixed schema paths only; never expose response values while
	// diagnosing a live allocation that failed validation after HTTP success.
	if t.Account.Channel == "huawei" {
		headers := o.obj("urlInfo").obj("headers")
		canonical := object{}
		for key, value := range headers {
			canonical[strings.ToLower(key)] = value
		}
		for _, key := range []string{"authorization", "content-type", "host", "user-agent", "x-amz-content-sha256", "x-amz-date", "x-obs-content-sha256", "x-obs-date", "date"} {
			d.Shape["urlInfo.headers.normalized."+key] = inspectionJSONType(canonical[key])
		}
		auth := canonical.str("authorization")
		switch {
		case strings.HasPrefix(auth, "AWS4-HMAC-SHA256 "):
			d.GrantAuthKind = "AWS4-HMAC-SHA256"
		case strings.HasPrefix(auth, "OBS "):
			d.GrantAuthKind = "OBS"
		case auth != "":
			d.GrantAuthKind = "other"
		}
		grantURL, _ := url.Parse(o.obj("urlInfo").str("url"))
		d.GrantChecks = map[string]bool{"sha256_matches": canonical.str("x-amz-content-sha256") == t.APK.SHA256(), "content_type_matches": canonical.str("content-type") == "application/octet-stream", "host_matches": grantURL != nil && strings.EqualFold(canonical.str("host"), grantURL.Host), "method_put": o.obj("urlInfo").str("method") == "PUT"}
		for _, path := range []string{"urlInfo", "urlInfo.url", "urlInfo.uploadUrl", "urlInfo.objectId", "urlInfo.method", "urlInfo.headers", "urlInfo.headers.Host", "urlInfo.headers.Content-Type", "urlInfo.headers.X-Amz-Content-Sha256", "urlInfo.headers.X-Amz-Date", "urlInfo.headers.Authorization", "urlInfo.headers.User-Agent", "uploadUrl", "objectId", "method", "headers"} {
			parts := strings.Split(path, ".")
			parent := o
			for _, part := range parts[:len(parts)-1] {
				parent = parent.obj(part)
			}
			d.Shape[path] = inspectionJSONType(parent[parts[len(parts)-1]])
		}
	}
	for _, pair := range []struct {
		object    object
		key, path string
	}{{o, "result", "result"}, {o.obj("ret"), "code", "ret.code"}} {
		if code := fieldText(pair.object, pair.key); inspectionCode.MatchString(code) {
			d.Codes[pair.path] = code
		}
	}
	var upload string
	switch t.Account.Channel {
	case "huawei":
		upload = o.obj("urlInfo").str("url")
	case "honor":
		rows := o.list("data")
		if len(rows) == 1 {
			upload = rows[0].str("uploadUrl")
		}
	case "tencent":
		upload = o.str("pre_sign_url")
	}
	if u, e := url.Parse(upload); e == nil && u.User == nil {
		if u.Scheme == "http" || u.Scheme == "https" {
			d.UploadScheme = u.Scheme
		} else if u.Scheme != "" {
			d.UploadScheme = "other"
		}
		host := strings.ToLower(u.Hostname())
		vendor := false
		for _, suffix := range []string{".huawei.com", ".dbankcdn.com", ".dbankcdn.cn", ".dbankcloud.cn", ".dbankcloud.com", ".myhuaweicloud.com", ".myhuaweicloud.cn", ".huaweicloud.com", ".huaweicloud.cn", ".hwclouds.com", ".hicloud.com", ".honor.com", ".myqcloud.com"} {
			vendor = vendor || strings.HasSuffix(host, suffix)
		}
		valid := len(host) > 0 && len(host) <= 253
		for _, c := range host {
			valid = valid && (c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-')
		}
		if vendor && valid {
			d.UploadHost = host
		}
	}
	return d
}

func recordFiveResponse(ctx context.Context, lease *account.Lease, materials Materials, t Target, last *FiveWriteDiagnostic, q transport.Request, status int, body []byte, sendErr error) error {
	if q.Class != transport.Write {
		return nil
	}
	step := ""
	for _, kind := range []string{"authorize", "upload", "bind", "metadata", "submit", "push"} {
		if q.OperationID == protocol.Digest([]string{t.IntentDigest, t.Account.Channel, kind}) {
			step = kind
		}
	}
	if step == "" || !lease.Valid(t.Account) {
		return fail("production_response_outside_scope", "unknown")
	}
	complete := sendErr == nil
	transportCode := ""
	var network *transport.Error
	if errors.As(sendErr, &network) {
		switch network.Code {
		case "http_status", "network_error", "request_canceled", "response_read_failed", "response_too_large", "redirect_rejected":
			transportCode = network.Code
		default:
			transportCode = "request_failed"
		}
		complete = network.Code == "http_status"
	} else if sendErr != nil {
		transportCode = "request_failed"
	}
	d := fiveResponseDiagnostic(t, step, q.OperationID, status, complete, body)
	d.TransportCode = transportCode
	*last = d
	if len(body) > 32<<10 {
		return fail("production_response_evidence_too_large", "unknown")
	}
	var nonce [16]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return fail("production_response_evidence_unavailable", "unknown")
	}
	operation, ok := ctx.Value(fiveOperationContextKey{}).(fiveOperationContext)
	if !ok || operation.Request.OperationID != q.OperationID || operation.Request.Kind != step || operation.Request.Scope != t.Scope || operation.Request.IntentDigest != t.IntentDigest || operation.StartedAt.IsZero() {
		return fail("production_response_request_binding_missing", "unknown")
	}
	d.AuthorizationGeneration = operation.Request.AuthorizationGeneration
	envelope := fiveResponseEnvelope{SchemaVersion: 1, Scope: t.Scope, Intent: t.IntentDigest, OperationID: q.OperationID, Step: step, AdapterBinding: adapterBinding(t), HTTPStatus: status, Complete: complete, CollectedAt: time.Now().UTC(), StartedAt: operation.StartedAt, Request: operation.Request, Body: body}
	ref, e := saveMaterial(ctx, materials, "five-http-"+q.OperationID+"-"+hex.EncodeToString(nonce[:]), envelope)
	if e != nil {
		return e
	}
	d.ResponseRef, d.Recording = ref, "sealed_in_keychain"
	*last = d
	b, _ := json.Marshal(d)
	if lease.Write("five-response-"+operation.Request.MaterialOperationKey(), b) != nil {
		return fail("production_response_index_unavailable", "unknown")
	}
	return nil
}

func (p *PreparedProduction) readFiveResponse(ctx context.Context, lease *account.Lease, materials Materials, q protocol.Request) (*fiveResponseEnvelope, error) {
	b, e := lease.Read("five-response-" + q.MaterialOperationKey())
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var index FiveWriteDiagnostic
	if json.Unmarshal(b, &index) != nil || index.OperationID != q.OperationID || index.Step != q.Kind {
		return nil, fail("production_response_index_invalid", "not_sent")
	}
	var env fiveResponseEnvelope
	if e = loadMaterial(ctx, materials, index.ResponseRef, &env); e != nil {
		return nil, e
	}
	if env.SchemaVersion != 1 || env.Scope != p.target.Scope || env.Intent != p.target.IntentDigest || env.OperationID != q.OperationID || env.Step != q.Kind || env.AdapterBinding != adapterBinding(p.target) || env.CollectedAt.IsZero() || env.HTTPStatus != 0 && (env.HTTPStatus < 100 || env.HTTPStatus > 599) {
		clear(env.Body)
		return nil, fail("production_response_evidence_invalid", "not_sent")
	}
	if env.StartedAt.IsZero() || env.StartedAt.After(env.CollectedAt) || !reflect.DeepEqual(env.Request, q) {
		clear(env.Body)
		return nil, fail("production_response_request_binding_invalid", "not_sent")
	}
	d := fiveResponseDiagnostic(p.target, q.Kind, q.OperationID, env.HTTPStatus, env.Complete, env.Body)
	d.AuthorizationGeneration = q.AuthorizationGeneration
	d.ResponseRef, d.Recording = index.ResponseRef, "sealed_in_keychain"
	*p.diagnostic = d
	return &env, nil
}
