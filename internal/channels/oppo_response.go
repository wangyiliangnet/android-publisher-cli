package channels

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type OPPOWriteDiagnostic struct {
	OperationID      string            `json:"operation_id"`
	Step             string            `json:"step"`
	HTTPStatus       int               `json:"http_status,omitempty"`
	VendorCode       *int64            `json:"errno,omitempty"`
	TransportCode    string            `json:"transport_code,omitempty"`
	Complete         bool              `json:"response_complete"`
	ResponseRef      string            `json:"private_response_ref,omitempty"`
	Recording        string            `json:"recording"`
	JSONTypes        map[string]string `json:"json_types,omitempty"`
	UploadScheme     string            `json:"upload_scheme,omitempty"`
	UploadHost       string            `json:"upload_host,omitempty"`
	UploadURLPresent bool              `json:"upload_url_present"`
	SignPresent      bool              `json:"sign_present"`
	FileChecks       map[string]bool   `json:"file_checks,omitempty"`
	FileExtension    string            `json:"file_extension_class,omitempty"`
	FileURLScheme    string            `json:"file_url_scheme,omitempty"`
}

type oppoResponseEnvelope struct {
	SchemaVersion                    int `json:"schema_version"`
	Scope, Intent, OperationID, Step string
	HTTPStatus                       int
	Complete                         bool
	CollectedAt                      time.Time
	Body                             []byte
}

func oppoResponseShape(d *OPPOWriteDiagnostic, body []byte) {
	o, err := decode(body)
	if err != nil {
		return
	}
	typeOf := func(b json.RawMessage) string {
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "missing"
		}
		switch v[0] {
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
	d.JSONTypes = map[string]string{"errno": typeOf(o["errno"]), "data": typeOf(o["data"])}
	data := o.obj("data")
	d.UploadURLPresent = data.str("upload_url") != ""
	d.SignPresent = data.str("sign") != ""
	for _, key := range []string{"upload_url", "sign", "url", "md5", "file_size", "file_extension", "id"} {
		d.JSONTypes["data."+key] = typeOf(data[key])
	}
	switch ext := data.str("file_extension"); ext {
	case "apk", ".apk", "APK":
		d.FileExtension = ext
	case "":
	default:
		d.FileExtension = "other"
	}
	if fileURL, e := url.Parse(data.str("url")); e == nil {
		switch fileURL.Scheme {
		case "http", "https":
			d.FileURLScheme = fileURL.Scheme
		case "":
		default:
			d.FileURLScheme = "other"
		}
	}
	u, err := url.Parse(data.str("upload_url"))
	if err != nil || u.User != nil {
		return
	}
	switch u.Scheme {
	case "https", "http":
		d.UploadScheme = u.Scheme
	case "":
	default:
		d.UploadScheme = "other"
	}
	host := strings.ToLower(u.Hostname())
	if host == "heytapmobi.com" || strings.HasSuffix(host, ".heytapmobi.com") || host == "oppomobile.com" || strings.HasSuffix(host, ".oppomobile.com") || host == "oppo.com" || strings.HasSuffix(host, ".oppo.com") {
		valid := len(host) <= 253
		for _, c := range host {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
				valid = false
			}
		}
		if valid {
			d.UploadHost = host
		}
	}
}

// This reads a previously sealed response; it never fetches another grant.
func (p *OPPOProduction) withWriteResponse(ctx context.Context, q protocol.Request, use func(OPPOWriteDiagnostic, oppoResponseEnvelope, Materials) error) error {
	return p.runtime.Manager.WithAccount(ctx, p.target.Account, func(s *credentials.Session) error {
		b, err := s.Lease().Read("oppo-response-" + q.OperationID)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var d OPPOWriteDiagnostic
		if json.Unmarshal(b, &d) != nil || d.OperationID != q.OperationID || d.Step != q.Kind || !strings.HasPrefix(d.ResponseRef, "material-") || len(d.ResponseRef) != 73 {
			return fail("oppo_response_index_invalid", "not_sent")
		}
		materials := keychainMaterials{vault: p.runtime.Manager.Vault, scope: p.target.Account, lease: s.Lease()}
		var envelope oppoResponseEnvelope
		if err = loadMaterial(ctx, materials, d.ResponseRef, &envelope); err != nil {
			return err
		}
		defer clear(envelope.Body)
		if envelope.SchemaVersion != 1 || envelope.Scope != p.target.Scope || envelope.Intent != p.target.IntentDigest || envelope.OperationID != q.OperationID || envelope.Step != q.Kind {
			return fail("oppo_response_binding_invalid", "not_sent")
		}
		if envelope.HTTPStatus != 0 && (envelope.HTTPStatus < 100 || envelope.HTTPStatus > 599) {
			return fail("oppo_response_status_invalid", "not_sent")
		}
		return use(d, envelope, materials)
	})
}

func (p *OPPOProduction) readWriteDiagnostic(ctx context.Context, q protocol.Request) error {
	return p.withWriteResponse(ctx, q, func(d OPPOWriteDiagnostic, envelope oppoResponseEnvelope, _ Materials) error {
		fresh := OPPOWriteDiagnostic{OperationID: q.OperationID, Step: q.Kind, HTTPStatus: envelope.HTTPStatus, Complete: envelope.Complete, ResponseRef: d.ResponseRef, Recording: "sealed_in_keychain"}
		switch d.TransportCode {
		case "http_status", "network_error", "request_canceled", "response_read_failed", "response_too_large", "redirect_rejected", "request_failed":
			fresh.TransportCode = d.TransportCode
		}
		if o, e := decode(envelope.Body); e == nil {
			if n, ok := o.num("errno"); ok && n >= -1000000000000 && n <= 1000000000000 {
				fresh.VendorCode = &n
			}
		}
		oppoResponseShape(&fresh, envelope.Body)
		if q.Kind == "upload" {
			if o, e := decode(envelope.Body); e == nil {
				data := o.obj("data")
				size, ok := data.num("file_size")
				fresh.FileChecks = map[string]bool{"size_is_int64": ok, "size_matches": ok && size == p.target.APK.Size(), "md5_matches": strings.EqualFold(data.str("md5"), p.target.APK.MD5()), "extension_matches": data.str("file_extension") == "apk", "file_id_present": data.id("id") != ""}
				if u, e := url.Parse(data.str("url")); e == nil {
					fresh.FileChecks["url_has_host"] = u.Host != ""
					fresh.FileChecks["url_has_userinfo"] = u.User != nil
					fresh.FileChecks["url_has_fragment"] = u.Fragment != ""
				}
			}
		}
		*p.diagnostic = fresh
		return nil
	})
}

// A complete, bound successful upload response is sufficient local receipt
// evidence. No upload URL is requested or fetched while recovering this cache.
func (p *OPPOProduction) reconcileSealedUpload(ctx context.Context, q protocol.Request) (*protocol.Receipt, error) {
	var receipt *protocol.Receipt
	err := p.withWriteResponse(ctx, q, func(_ OPPOWriteDiagnostic, envelope oppoResponseEnvelope, materials Materials) error {
		if !envelope.Complete || envelope.HTTPStatus != 200 {
			return nil
		}
		o, err := decode(envelope.Body)
		if err != nil {
			return nil
		}
		file, err := parseOPPOUploadedFile(o, p.target.APK)
		if err != nil {
			return nil
		}
		payload := oppoFileCheckpoint{file.URL, file.MD5, file.Ref, file.Size}
		ref := "material-" + account.Key(q.OperationID)
		prior, err := materials.Get(ctx, ref)
		if err == nil {
			defer prior.Destroy()
			b := prior.Bytes()
			defer clear(b)
			var saved oppoFileCheckpoint
			if json.Unmarshal(b, &saved) != nil || saved != payload {
				return fail("oppo_upload_checkpoint_conflict", "not_sent")
			}
		} else if errors.Is(err, credentials.ErrMissing) {
			ref, err = saveMaterial(ctx, materials, q.OperationID, payload)
			if err != nil {
				return err
			}
		} else {
			return credentials.PublicError(err)
		}
		receipt = &protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: ref}
		return nil
	})
	return receipt, err
}

type observedOPPOSender struct {
	Sender
	observe func(context.Context, transport.Request, int, []byte, error) error
}

func (s observedOPPOSender) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	b, err := s.Sender.Send(ctx, q)
	status := 200
	var network *transport.Error
	if err != nil {
		status = 0
		if errors.As(err, &network) {
			status = network.Status
		}
	}
	if e := s.observe(ctx, q, status, b, err); e != nil {
		clear(b)
		return nil, e
	}
	return b, err
}

// Only safe typed metadata leaves this function. Raw bytes (including signed
// URLs and nested error text) are sealed in Keychain before protocol parsing.
func recordOPPOResponse(ctx context.Context, lease *account.Lease, materials Materials, t Target, last *OPPOWriteDiagnostic, q transport.Request, status int, body []byte, sendErr error) error {
	if q.Class != transport.Write {
		return nil
	}
	step := ""
	for _, kind := range []string{"authorize", "upload", "submit"} {
		if q.OperationID == protocol.Digest([]string{t.IntentDigest, "oppo", kind}) {
			step = kind
		}
	}
	if step == "" || !lease.Valid(t.Account) {
		return fail("oppo_response_outside_scope", "unknown")
	}
	d := OPPOWriteDiagnostic{OperationID: q.OperationID, Step: step, HTTPStatus: status, Complete: sendErr == nil, Recording: "not_saved"}
	var network *transport.Error
	if errors.As(sendErr, &network) {
		switch network.Code {
		case "http_status", "network_error", "request_canceled", "response_read_failed", "response_too_large", "redirect_rejected":
			d.TransportCode = network.Code
		default:
			d.TransportCode = "request_failed"
		}
		d.Complete = network.Code == "http_status"
	} else if sendErr != nil {
		d.TransportCode = "request_failed"
	}
	if o, err := decode(body); err == nil {
		if n, ok := o.num("errno"); ok && n >= -1000000000000 && n <= 1000000000000 {
			d.VendorCode = &n
		}
	}
	oppoResponseShape(&d, body)
	*last = d
	if len(body) > 32<<10 {
		return fail("oppo_response_evidence_too_large", "unknown")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fail("oppo_response_evidence_unavailable", "unknown")
	}
	private := oppoResponseEnvelope{1, t.Scope, t.IntentDigest, q.OperationID, step, status, d.Complete, time.Now().UTC(), body}
	ref, err := saveMaterial(ctx, materials, "oppo-http-"+q.OperationID+"-"+hex.EncodeToString(nonce[:]), private)
	if err != nil {
		return err
	}
	d.ResponseRef = ref
	d.Recording = "sealed_in_keychain"
	*last = d
	b, _ := json.Marshal(d)
	if err = lease.Write("oppo-response-"+q.OperationID, b); err != nil {
		return fail("oppo_response_index_unavailable", "unknown")
	}
	return nil
}
