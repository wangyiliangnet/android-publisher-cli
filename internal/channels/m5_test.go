package channels

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"errors"
	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type m5Sender func(context.Context, transport.Request) ([]byte, error)

func (f m5Sender) Send(ctx context.Context, q transport.Request) ([]byte, error) { return f(ctx, q) }
func m5Secret(t *testing.T, s string) *credentials.Secret {
	t.Helper()
	v, e := credentials.NewSecret([]byte(s))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(v.Destroy)
	return v
}
func m5Client(t *testing.T, channel string) *Client {
	return &Client{Channel: channel, Auth: Auth{ID: "synthetic-id", Secret: m5Secret(t, "synthetic-secret"), Token: m5Secret(t, "synthetic-token")}, Now: func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }}
}
func m5File(t *testing.T) File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "synthetic.apk")
	if e := os.WriteFile(p, []byte("account-free fixture bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	f, e := NewFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func m5Body(t *testing.T, q transport.Request) []byte {
	t.Helper()
	if q.Body == nil {
		return nil
	}
	defer q.Body.Close()
	b, e := io.ReadAll(q.Body)
	if e != nil {
		t.Fatal(e)
	}
	if int64(len(b)) != q.ContentLength {
		t.Fatalf("content length: %d != %d", len(b), q.ContentLength)
	}
	return b
}
func m5Fields(t *testing.T, q transport.Request) map[string]string {
	t.Helper()
	v, e := url.ParseQuery(string(m5Body(t, q)))
	if e != nil {
		t.Fatal(e)
	}
	f := map[string]string{}
	for k, v := range v {
		if len(v) != 1 {
			t.Fatal("duplicate field")
		}
		f[k] = v[0]
	}
	return f
}
func m5Signed(t *testing.T, f map[string]string, name string) {
	t.Helper()
	if f[name] != hmacSign(f, []byte("synthetic-secret"), name) {
		t.Fatal("incorrect raw HMAC")
	}
}
func m5Error(t *testing.T, e error, effect string) {
	t.Helper()
	p, ok := e.(*Error)
	if !ok || p.Effect != effect {
		t.Fatalf("expected %s protocol error; got %v", effect, e)
	}
}

func TestM5OPPOTokenUsesAbsoluteExpiryAndLifecycle(t *testing.T) {
	c := m5Client(t, "oppo")
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		u, e := url.Parse(q.URL)
		if e != nil {
			t.Fatal(e)
		}
		if u.Path != "/developer/v1/token" || q.Class != transport.Lifecycle || q.Method != "GET" || u.Query().Get("client_secret") != "synthetic-secret" {
			t.Fatal("wrong lifecycle request")
		}
		return raw(map[string]any{"errno": 0, "data": map[string]any{"access_token": "new-token", "expire_in": c.now().Add(48 * time.Hour).Unix()}}), nil
	})
	r, e := c.OPPOToken(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Value.Destroy()
	if !r.ExpiresAt.Equal(c.now().Add(48*time.Hour)) || string(r.Value.Bytes()) != "new-token" || calls != 1 {
		t.Fatal("token expiry/value")
	}
	for _, body := range []string{`{"data":{"access_token":"x","expire_in":1792497600}}`, `{"errno":0,"data":{"access_token":"x","expire_in":172800}}`, `{"errno":0,"data":{"access_token":"x","expire_in":null}}`} {
		c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) { return []byte(body), nil })
		_, e = c.OPPOToken(context.Background())
		m5Error(t, e, "unknown")
	}
}
func TestM5OPPOGrantAndMultipartUpload(t *testing.T) {
	c := m5Client(t, "oppo")
	f := m5File(t)
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if calls == 1 {
			u, _ := url.Parse(q.URL)
			fields := map[string]string{}
			for k, v := range u.Query() {
				fields[k] = v[0]
			}
			m5Signed(t, fields, "api_sign")
			if q.Class != transport.Write || q.OperationID != "grant-intent" {
				t.Fatal("grant checkpoint")
			}
			return []byte(`{"errno":0,"data":{"upload_url":"https://upload.heytapmobi.com/one-file","sign":"one-time-secret"}}`), nil
		}
		if q.Grant == nil || q.Grant.URL != q.URL || q.Method != "POST" || q.Headers.Get("Authorization") != "" || q.Class != transport.Write || q.OperationID != "upload-intent" {
			t.Fatal("upload boundary")
		}
		_, p, e := mime.ParseMediaType(q.Headers.Get("Content-Type"))
		if e != nil {
			t.Fatal(e)
		}
		r := multipart.NewReader(strings.NewReader(string(m5Body(t, q))), p["boundary"])
		form, e := r.ReadForm(1 << 20)
		if e != nil {
			t.Fatal(e)
		}
		defer form.RemoveAll()
		if form.Value["sign"][0] != "one-time-secret" || form.Value["type"][0] != "apk" || len(form.File["file"]) != 1 {
			t.Fatal("upload body")
		}
		return raw(map[string]any{"errno": 0, "data": map[string]any{"url": "https://assets.heytapmobi.com/app.apk", "md5": f.MD5(), "file_size": f.Size(), "file_extension": "apk", "id": "90071992547409931234"}}), nil
	})
	g, e := c.OPPOAuthorize(context.Background(), "grant-intent")
	if e != nil {
		t.Fatal(e)
	}
	if !g.ExpiresAt.Equal(c.now().Add(30 * time.Second)) {
		t.Fatal("local use deadline")
	}
	uploaded, e := c.OPPOUpload(context.Background(), "upload-intent", g, f)
	if e != nil {
		t.Fatal(e)
	}
	if uploaded.Ref != "90071992547409931234" || calls != 2 {
		t.Fatal("reference lost precision")
	}
	for _, bad := range []UploadGrant{{URL: "https://evil.invalid/a", Ref: "s", Method: "POST", ExpiresAt: c.now().Add(time.Second)}, {URL: g.URL, Ref: g.Ref, Method: "POST", ExpiresAt: c.now()}, {URL: g.URL, Ref: g.Ref, Method: "POST", ExpiresAt: c.now().Add(time.Minute)}} {
		_, e = c.OPPOUpload(context.Background(), "bad", bad, f)
		m5Error(t, e, "not_sent")
	}
	if calls != 2 {
		t.Fatal("invalid grant sent")
	}
}
func m5OPPOUpdate(f File) OPPOUpdateInput {
	return OPPOUpdateInput{PackageName: "com.example.synthetic", VersionCode: 43, Existing: Details{PackageName: "com.example.synthetic", AppID: "9007199254740993", VersionCode: 42, Metadata: map[string]string{"app_name": "合成应用", "second_category_id": "74", "third_category_id": "6685", "summary": "合成应用简介", "detail_desc": strings.Repeat("资料", 12), "update_desc": "旧版发布说明内容", "privacy_source_url": "https://example.invalid/privacy", "icon_url": "https://example.invalid/icon.png", "pic_url": "https://example.invalid/a.png,https://example.invalid/b.png", "test_desc": "合成测试说明", "copyright_url": "https://example.invalid/copyright.png", "business_username": "合成联系人", "business_email": "test@example.invalid", "business_mobile": "12345678901", "age_level": "3", "adaptive_equipment": "4"}}, File: OPPOFile{URL: "https://assets.heytapmobi.com/app.apk", MD5: f.MD5(), Ref: "uploaded-ref", Size: f.Size()}, Patch: map[string]string{"update_desc": "修复问题并改进体验"}, OnlineType: 1}
}
func TestM5OPPOUpdatePreservesFrozenMetadataAndRequiresSuccess(t *testing.T) {
	c := m5Client(t, "oppo")
	in := m5OPPOUpdate(m5File(t))
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		fields := m5Fields(t, q)
		m5Signed(t, fields, "api_sign")
		if fields["app_name"] != "合成应用" || fields["update_desc"] != in.Patch["update_desc"] || fields["pkg_name"] != in.PackageName || fields["version_code"] != "43" {
			t.Fatal("metadata merge or target")
		}
		var a []map[string]any
		if json.Unmarshal([]byte(fields["apk_url"]), &a) != nil || len(a) != 1 || a[0]["cpu_code"] != float64(0) {
			t.Fatal("single APK wire encoding")
		}
		return []byte(`{"errno":0,"data":{"success":true}}`), nil
	})
	accepted, e := c.OPPOUpdate(context.Background(), "update-intent", in)
	if e != nil || accepted.RefKind != "local_acknowledgement" {
		t.Fatalf("acceptance: %v", e)
	}
	if calls != 1 {
		t.Fatal("duplicate write")
	}
	delete(in.Existing.Metadata, "copyright_url")
	_, e = c.OPPOUpdate(context.Background(), "missing", in)
	m5Error(t, e, "not_sent")
	if calls != 1 {
		t.Fatal("missing mandatory metadata sent")
	}
	in = m5OPPOUpdate(m5File(t))
	for _, body := range []string{`{"errno":0,"data":{}}`, `{"errno":0,"data":{"success":false}}`, `{"errno":911215,"data":{"message":"应用审核中"}}`, `{"errno":911216,"data":{"message":"版本更新任务处理中"}}`} {
		c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) { m5Body(t, q); return []byte(body), nil })
		_, e = c.OPPOUpdate(context.Background(), "ambiguous", in)
		m5Error(t, e, "unknown")
	}
}
func TestM5OPPOCopyrightProofWireFields(t *testing.T) {
	const electronic = "https://example.invalid/existing-proof.pdf?signature=SYNTHETIC-PRIVATE"
	for _, paper := range []string{"", "https://example.invalid/original-paper.png"} {
		t.Run(map[bool]string{true: "electronic_only", false: "original_proof_retained"}[paper == ""], func(t *testing.T) {
			in := m5OPPOUpdate(m5File(t))
			in.Existing.Metadata["copyright_url"] = paper
			in.Existing.Metadata["electronic_cert_url"] = electronic
			in.Existing.Metadata["app_subname"] = ""
			in.Existing.Metadata["video_url"] = ""
			c := m5Client(t, "oppo")
			calls := 0
			c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
				calls++
				fields := m5Fields(t, q)
				want := paper
				if paper == "" {
					want = electronic
				}
				if fields["copyright_url"] != want || fields["electronic_cert_url"] != electronic {
					t.Fatal("required proof or original electronic field changed")
				}
				for _, key := range []string{"app_subname", "video_url"} {
					if _, present := fields[key]; present {
						t.Fatal("empty optional metadata was sent as a clearing instruction")
					}
				}
				m5Signed(t, fields, "api_sign")
				return []byte(`{"errno":0,"data":{"success":true}}`), nil
			})
			if _, err := c.OPPOUpdate(context.Background(), "proof-intent", in); err != nil || calls != 1 || in.Existing.Metadata["copyright_url"] != paper {
				t.Fatal("proof derivation mutated its baseline or failed before signing")
			}
		})
	}
	for _, proof := range []string{"", "http://example.invalid/proof.pdf", "https://user@example.invalid/proof.pdf", "https://example.invalid/proof.pdf#fragment", "invalid-proof"} {
		in := m5OPPOUpdate(m5File(t))
		delete(in.Existing.Metadata, "copyright_url")
		in.Existing.Metadata["electronic_cert_url"] = proof
		c := m5Client(t, "oppo")
		c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) {
			t.Fatal("missing or malformed proof reached the network")
			return nil, nil
		})
		_, err := c.OPPOUpdate(context.Background(), "missing-proof", in)
		m5Error(t, err, "not_sent")
	}
}

func TestM5OPPOVersionHashAndTaskAssociation(t *testing.T) {
	c := m5Client(t, "oppo")
	const md5 = "c673d381029c42ad9961861ef655791f"
	code := "00"
	version := "43"
	task := "2"
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		if q.Method == "GET" {
			u, _ := url.Parse(q.URL)
			if u.Query().Get("version_code") != "43" {
				t.Fatal("version query missing")
			}
			return raw(map[string]any{"errno": 0, "data": map[string]any{"app_id": "100", "pkg_name": "com.example.synthetic", "version_code": version, "version_id": "90071992547409931234", "apk_md5": md5, "audit_status": code, "state": "1"}}), nil
		}
		fields := m5Fields(t, q)
		if q.Class != transport.Query || fields["version_code"] != "43" {
			t.Fatal("task query")
		}
		return raw(map[string]any{"errno": 0, "data": map[string]any{"pkg_name": "com.example.synthetic", "version_code": version, "task_state": task}}), nil
	})
	s, e := c.OPPOAudit(context.Background(), "com.example.synthetic", 43, md5)
	if e != nil || s.Review != "in_review" || s.Availability != "unknown" {
		t.Fatalf("00 qualification evidence: %+v %v", s, e)
	}
	code = "111"
	s, e = c.OPPOAudit(context.Background(), "com.example.synthetic", 43, md5)
	if e != nil || s.Availability != "live" || s.Ref != "90071992547409931234" {
		t.Fatal("live evidence")
	}
	_, e = c.OPPOAudit(context.Background(), "com.example.synthetic", 43, strings.Repeat("0", 32))
	m5Error(t, e, "unknown")
	version = "42"
	_, e = c.OPPOAudit(context.Background(), "com.example.synthetic", 43, md5)
	m5Error(t, e, "unknown")
	_, e = c.OPPOTaskStatus(context.Background(), "com.example.synthetic", 43)
	m5Error(t, e, "unknown")
	version = "43"
	r, e := c.OPPOTaskStatus(context.Background(), "com.example.synthetic", 43)
	if e != nil || r.State != "succeeded" {
		t.Fatal("task processing")
	}
	task = "9"
	_, e = c.OPPOTaskStatus(context.Background(), "com.example.synthetic", 43)
	m5Error(t, e, "unknown")
}
func TestM5VivoRequiresBothCodesAndLowercaseSerialnumber(t *testing.T) {
	c := m5Client(t, "vivo")
	f := m5File(t)
	serialKey := "serialnumber"
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		_, p, e := mime.ParseMediaType(q.Headers.Get("Content-Type"))
		if e != nil {
			t.Fatal(e)
		}
		r := multipart.NewReader(strings.NewReader(string(m5Body(t, q))), p["boundary"])
		form, e := r.ReadForm(1 << 20)
		if e != nil {
			t.Fatal(e)
		}
		defer form.RemoveAll()
		fields := map[string]string{}
		for k, v := range form.Value {
			fields[k] = v[0]
		}
		m5Signed(t, fields, "sign")
		if fields["sign_method"] != "HMAC-SHA256" || fields["method"] != "app.upload.apk.app" || fields["timestamp"] != decimal(c.now().UnixMilli()) || fields["fileMd5"] != f.MD5() {
			t.Fatal("vivo signed wire")
		}
		return raw(map[string]any{"code": 0, "subCode": "0", "data": map[string]any{serialKey: "90071992547409931234", "packageName": "com.example.synthetic", "versionCode": 43, "fileMd5": f.MD5()}}), nil
	})
	uploaded, e := c.VivoUpload(context.Background(), "upload", "com.example.synthetic", f)
	if e != nil || uploaded.SerialNumber != "90071992547409931234" {
		t.Fatalf("upload: %v", e)
	}
	serialKey = "serialNumber"
	_, e = c.VivoUpload(context.Background(), "upload", "com.example.synthetic", f)
	m5Error(t, e, "unknown")
	for _, body := range []string{`{"code":0}`, `{"code":0,"subCode":0}`, `{"code":0,"subCode":null}`, `{"code":0,"subCode":"13001"}`, `{"subCode":"0"}`} {
		o, e := decode([]byte(body))
		if e != nil {
			t.Fatal(e)
		}
		m5Error(t, vivoCheck(o), "unknown")
	}
}
func TestM5VivoUpdatePreservesTextAndNeverReusesViewingURLs(t *testing.T) {
	c := m5Client(t, "vivo")
	in := VivoUpdateInput{PackageName: "com.example.synthetic", VersionCode: 43, Existing: Details{PackageName: "com.example.synthetic", VersionCode: 42, Metadata: map[string]string{"mainTitle": "合成应用", "detailDesc": strings.Repeat("简介", 30), "icon": "https://example.invalid/icon.png", "screenshot": "https://example.invalid/screenshot.png", "copyrightList": "https://example.invalid/copyright.png", "status": "3", "saleStatus": "1"}}, File: VivoFile{SerialNumber: "90071992547409931234", PackageName: "com.example.synthetic", VersionCode: 43, MD5: strings.Repeat("a", 32)}, Patch: map[string]string{"updateDesc": "修复问题并改进体验"}, OnlineType: 2, CompatibleDevice: 1, ScheduledAt: c.now().Add(time.Hour)}
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		fields := m5Fields(t, q)
		m5Signed(t, fields, "sign")
		for _, key := range []string{"icon", "screenshot", "copyrightList", "status", "saleStatus"} {
			if _, exists := fields[key]; exists {
				t.Fatalf("query-only value reused for %s", key)
			}
		}
		if fields["apk"] != in.File.SerialNumber || fields["updateDesc"] != in.Patch["updateDesc"] || fields["mainTitle"] != "合成应用" || fields["compatibleDevice"] != "1" || fields["scheOnlineTime"] != "2026-10-20 21:00:00" {
			t.Fatal("merged update fields")
		}
		return []byte(`{"code":0,"subCode":"0"}`), nil
	})
	a, e := c.VivoUpdate(context.Background(), "update", in)
	if e != nil || a.RefKind != "local_acknowledgement" {
		t.Fatalf("update: %v", e)
	}
	in.File.VersionCode = 42
	_, e = c.VivoUpdate(context.Background(), "wrong", in)
	m5Error(t, e, "not_sent")
	if calls != 1 {
		t.Fatal("mismatched artifact sent")
	}
}
func TestM5VivoStatusSeparatesReviewLiveAndOldVersion(t *testing.T) {
	c := m5Client(t, "vivo")
	version := int64(43)
	review := int64(2)
	sale := int64(1)
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		f := m5Fields(t, q)
		if q.Class != transport.Query || f["method"] != "app.query.details" {
			t.Fatal("read-only query")
		}
		return raw(map[string]any{"code": 0, "subCode": "0", "data": map[string]any{"packageName": "com.example.synthetic", "versionCode": version, "status": review, "saleStatus": sale}}), nil
	})
	s, e := c.VivoStatus(context.Background(), "com.example.synthetic", 43)
	if e != nil || s.Review != "in_review" || s.Availability != "unknown" {
		t.Fatal("reviewing must not borrow previous live state")
	}
	review = 3
	s, e = c.VivoStatus(context.Background(), "com.example.synthetic", 43)
	if e != nil || s.Availability != "live" {
		t.Fatal("target approved and live")
	}
	sale = 0
	s, e = c.VivoStatus(context.Background(), "com.example.synthetic", 43)
	if e != nil || s.Availability != "not_observed_live" || s.Review != "approved" {
		t.Fatal("approved pending release")
	}
	version = 42
	_, e = c.VivoStatus(context.Background(), "com.example.synthetic", 43)
	m5Error(t, e, "unknown")
	version = 43
	review = 99
	s, e = c.VivoStatus(context.Background(), "com.example.synthetic", 43)
	if e != nil || s.Review != "unknown" {
		t.Fatal("unrecognized review status")
	}
}

// This vault contains only synthetic fixture bytes. Exercising Session.Token
// catches lifecycle classifications that plain protocol decoder tests cannot.
type m5Vault map[credentials.Ref][]byte

func (v m5Vault) Get(_ context.Context, r credentials.Ref) (*credentials.Secret, error) {
	b, ok := v[r]
	if !ok {
		return nil, credentials.ErrMissing
	}
	return credentials.NewSecret(b)
}
func (v m5Vault) Put(_ context.Context, r credentials.Ref, s *credentials.Secret, replace bool) error {
	if _, ok := v[r]; ok && !replace {
		return credentials.ErrExists
	}
	v[r] = s.Bytes()
	return nil
}
func (v m5Vault) Delete(_ context.Context, r credentials.Ref) error { delete(v, r); return nil }
func TestM5OPPOTokenThroughSessionCacheAndRefreshBarrier(t *testing.T) {
	for _, mode := range []string{"accepted", "not_sent", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			scope := account.Scope{Channel: "oppo", Account: "m5-synthetic"}
			manager := &credentials.Manager{Vault: m5Vault{credentials.Ref{Scope: scope, Name: "client"}: []byte("synthetic-secret")}, Accounts: account.Coordinator{Root: t.TempDir()}}
			c := m5Client(t, "oppo")
			c.Now = nil
			calls := 0
			c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
				calls++
				if mode == "not_sent" {
					return nil, &transport.Error{Code: "synthetic_before_send", Effect: "not_sent"}
				}
				if mode == "ambiguous" {
					return nil, &transport.Error{Code: "synthetic_response_lost", Effect: "unknown"}
				}
				return raw(map[string]any{"errno": 0, "data": map[string]any{"access_token": "cached-synthetic-token", "expire_in": time.Now().Add(48 * time.Hour).Unix()}}), nil
			})
			fetch := func(ctx context.Context, s *credentials.Secret) (credentials.FetchResult, error) {
				c.Auth.Secret = s
				return c.OPPOToken(ctx)
			}
			for i := 0; i < 2; i++ {
				e := manager.WithAccount(context.Background(), scope, func(s *credentials.Session) error {
					token, e := s.Token(context.Background(), "client", "publisher", fetch)
					if token != nil {
						defer token.Destroy()
						if string(token.Bytes()) != "cached-synthetic-token" {
							t.Fatal("wrong cached value")
						}
					}
					return e
				})
				switch mode {
				case "accepted":
					if e != nil {
						t.Fatal(e)
					}
				case "not_sent":
					if errors.Is(e, credentials.ErrRefreshUnknown) || e == nil {
						t.Fatalf("unsent request left an unknown refresh: %v", e)
					}
				case "ambiguous":
					if !errors.Is(e, credentials.ErrRefreshUnknown) {
						t.Fatalf("unknown refresh barrier: %v", e)
					}
				}
			}
			expected := 1
			if mode == "not_sent" {
				expected = 2
			}
			if calls != expected {
				t.Fatalf("%s: %d requests, want %d", mode, calls, expected)
			}
		})
	}
}
func TestM5MissingAuthAndIdentifiersNeverSend(t *testing.T) {
	c := m5Client(t, "oppo")
	c.Auth = Auth{}
	calls := 0
	c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) {
		calls++
		t.Fatal("missing local input reached sender")
		return nil, nil
	})
	r, e := c.OPPOToken(context.Background())
	m5Error(t, e, "not_sent")
	if r.Effect != "not_sent" {
		t.Fatal("local token failure effect")
	}
	_, e = c.OPPODetails(context.Background(), "com.example.synthetic", 43)
	m5Error(t, e, "not_sent")
	_, e = c.OPPOAuthorize(context.Background(), "authorize")
	m5Error(t, e, "not_sent")
	_, e = c.OPPOTaskStatus(context.Background(), "", 43)
	m5Error(t, e, "not_sent")
	c.Channel = "vivo"
	_, e = c.VivoDetails(context.Background(), "com.example.synthetic")
	m5Error(t, e, "not_sent")
	_, e = c.VivoUpload(context.Background(), "upload", "com.example.synthetic", m5File(t))
	m5Error(t, e, "not_sent")
	if calls != 0 {
		t.Fatal("unexpected request")
	}
}
