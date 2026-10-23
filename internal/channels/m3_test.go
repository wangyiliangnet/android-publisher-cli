package channels

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

// tlsFixture only exists in tests. It validates the original static destination
// before replacing its authority with this test's verified local TLS server.
type tlsFixture struct {
	t      *testing.T
	server *httptest.Server
	count  atomic.Int32
}

func (f *tlsFixture) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	f.count.Add(1)
	if q.Body != nil {
		defer q.Body.Close()
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, unknown()
	}
	valid := false
	for _, origin := range origins {
		if origin == "https://"+u.Host {
			valid = true
		}
	}
	if q.Grant != nil {
		valid = allowedUpload("tencent", q.URL) || allowedUpload("huawei", q.URL) || allowedUpload("oppo", q.URL)
	}
	if !valid {
		f.t.Fatal("fixture saw unexpected destination")
	}
	if q.Class == transport.Write && q.OperationID == "" {
		f.t.Fatal("write without journal identity")
	}
	r, err := http.NewRequestWithContext(ctx, q.Method, f.server.URL+u.RequestURI(), q.Body)
	if err != nil {
		return nil, err
	}
	r.ContentLength = q.ContentLength
	r.GetBody = nil
	r.Header = q.Headers.Clone()
	c := f.server.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(r)
	if err != nil {
		return nil, fail("fixture_lost_response", "unknown")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode != 200 {
		return nil, unknown()
	}
	return b, nil
}
func localTLS(t *testing.T, h http.HandlerFunc) *tlsFixture {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	return &tlsFixture{t: t, server: s}
}
func m3File(t *testing.T, name string) File {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("synthetic APK bytes 中文\x00\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func m3Secret(t *testing.T, s string) *credentials.Secret {
	t.Helper()
	v, e := credentials.NewSecret([]byte(s))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(v.Destroy)
	return v
}

func TestM3RawSignatureVectorAndRedaction(t *testing.T) {
	f := map[string]string{"A": "", "message": "中文 空格\n&+=测试"}
	if hmacSign(f, []byte("synthetic-secret")) != "2e73ebdde0e2a2e5d88dab307e648cb8f85261d24589e05f891f8d5c95a8351d" {
		t.Fatal("raw UTF-8 HMAC vector changed")
	}
	a := Auth{ID: "fixture-private", Secret: m3Secret(t, "fixture-private")}
	g := UploadGrant{URL: "https://example.invalid/?signature=fixture-private"}
	for _, v := range []any{a, g, object{"token": json.RawMessage(`"fixture-private"`)}, &Client{Auth: a}} {
		b, _ := json.Marshal(v)
		if strings.Contains(fmt.Sprintf("%#v %s", v, b), "fixture-private") {
			t.Fatal("secret in diagnostic")
		}
	}
}
func TestM3XiaomiEncryptedMultipartAndSinglePush(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	apk, icon := m3File(t, "release.apk"), m3File(t, "icon.png")
	password := strings.Repeat("synthetic-password", 30)
	notes := "中文 space & +\nnext line"
	online := time.Now().Add(time.Hour).Truncate(time.Millisecond)
	w := localTLS(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/devupload/dev/push" || r.ContentLength <= 0 {
			t.Error("wrong push envelope")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error("multipart rejected")
			return
		}
		defer r.MultipartForm.RemoveAll()
		request := r.FormValue("RequestData")
		var data struct {
			Sync int `json:"synchroType"`
			Info struct {
				Notes  string `json:"updateDesc"`
				Online int64  `json:"onlineTime"`
				Tests  string `json:"testAccount"`
			} `json:"appInfo"`
		}
		if json.Unmarshal([]byte(request), &data) != nil || data.Sync != 1 || data.Info.Notes != notes || data.Info.Online != online.UnixMilli() || !strings.Contains(data.Info.Tests, "zh_CN") {
			t.Error("request text, nested test-account or schedule changed")
		}
		cipher, err := hex.DecodeString(r.FormValue("SIG"))
		if err != nil || len(cipher) < 2*key.Size() || len(cipher)%key.Size() != 0 {
			t.Error("invalid RSA chunks")
			return
		}
		var plain []byte
		for len(cipher) > 0 {
			b, e := rsa.DecryptPKCS1v15(rand.Reader, key, cipher[:key.Size()])
			if e != nil {
				t.Error("cannot decrypt SIG")
				return
			}
			plain = append(plain, b...)
			cipher = cipher[key.Size():]
		}
		var envelope struct {
			Password string                        `json:"password"`
			Sig      []struct{ Name, Hash string } `json:"sig"`
		}
		if json.Unmarshal(plain, &envelope) != nil || envelope.Password != password || len(envelope.Sig) != 3 {
			t.Error("invalid digest envelope")
		}
		for _, s := range envelope.Sig {
			var b []byte
			if s.Name == "RequestData" {
				b = []byte(request)
			} else {
				f, _, e := r.FormFile(s.Name)
				if e != nil {
					t.Error("missing signed file")
					return
				}
				b, _ = io.ReadAll(f)
				f.Close()
			}
			h := md5.Sum(b)
			if hex.EncodeToString(h[:]) != s.Hash {
				t.Error("signature not over exact wire bytes")
			}
		}
		io.WriteString(w, `{"result":0}`)
	})
	c := &Client{Channel: "xiaomi", Wire: w, Auth: Auth{ID: "synthetic@example.invalid", Secret: m3Secret(t, password), PublicKey: &key.PublicKey}}
	a, err := c.XiaomiPush(context.Background(), "operation-push", XiaomiUpdateInput{PackageName: "com.example.fixture", AppName: "合成应用", PrivacyURL: "https://example.invalid/privacy", Notes: notes, APK: apk, Icon: icon, OnlineAt: &online, TestAccount: m3Secret(t, `{"zh_CN":{"auditNotes":"模拟说明"}}`)})
	if err != nil || a.RefKind != "local_acknowledgement" || w.count.Load() != 1 {
		t.Fatal("push was not a single accepted operation", err)
	}
}
func TestM3MissingSuccessAndUnknownWriteNeverRetry(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	f := m3File(t, "fixture.apk")
	for _, body := range []string{`{}`, `{"result":null}`, `{"result":"0"}`, `{"result":987654}`, `{"result":987654,"result":0}`, `{`} {
		t.Run(body, func(t *testing.T) {
			w := localTLS(t, func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); io.WriteString(w, body) })
			c := &Client{Channel: "xiaomi", Wire: w, Auth: Auth{ID: "fixture", Secret: m3Secret(t, "fixture"), PublicKey: &key.PublicKey}}
			_, err := c.XiaomiPush(context.Background(), "operation", XiaomiUpdateInput{PackageName: "com.example.fixture", AppName: "App", PrivacyURL: "https://example.invalid", Notes: "fixture", APK: f, Icon: f})
			if err == nil || w.count.Load() != 1 {
				t.Fatal("missing or unknown result became success/retry")
			}
		})
	}
}
func TestM3TencentAuthorizationUploadAndABIUpdate(t *testing.T) {
	file := m3File(t, "release.apk")
	expiry := time.Now().Add(time.Hour).Unix()
	var methods []string
	w := localTLS(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/object.apk" {
			b, _ := io.ReadAll(r.Body)
			if int64(len(b)) != file.size || r.Header.Get("Authorization") != "" || r.ContentLength != file.size {
				t.Error("COS body or credentials mismatch")
			}
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		f := map[string]string{}
		for k, v := range r.PostForm {
			if len(v) != 1 {
				t.Error("duplicate field")
			}
			f[k] = v[0]
		}
		if f["sign"] != hmacSign(f, []byte("synthetic-secret"), "sign") || f["user_id"] != "synthetic-user" || len(f["timestamp"]) != 10 {
			t.Error("bad signed form")
		}
		switch r.URL.Path {
		case "/open_file/developer_api/get_file_upload_info":
			fmt.Fprintf(w, `{"ret":0,"serial_number":"serial-9007199254740993","pre_sign_url":"https://fixture.cos.ap-shanghai.myqcloud.com/object.apk?q-sign-time=1%%3B%d&q-signature=synthetic-private"}`, expiry)
		case "/open_file/developer_api/update_app":
			if f["feature"] != "更新 中文 &+\n内容" || f["apk32_flag"] != "2" || f["apk64_flag"] != "1" || f["apk64_file_serial_number"] != "serial-9007199254740993" || f["apk64_file_md5"] != file.md5 || f["deploy_type"] != "1" {
				t.Error("bad update ABI/form")
			}
			io.WriteString(w, `{"ret":0}`)
		default:
			t.Error("unexpected API call")
		}
	})
	c := &Client{Channel: "tencent", Wire: w, Auth: Auth{ID: "synthetic-user", Secret: m3Secret(t, "synthetic-secret")}}
	g, err := c.TencentAuthorize(context.Background(), "authorize", "com.example.fixture", "9007199254740993", file)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.TencentUpload(context.Background(), "upload", g, file); err != nil {
		t.Fatal(err)
	}
	a, err := c.TencentUpdate(context.Background(), "submit", TencentUpdateInput{PackageName: "com.example.fixture", AppID: "9007199254740993", Serial: g.Ref, MD5: file.md5, Notes: "更新 中文 &+\n内容", ABI: []string{"arm64-v8a"}, Policy: manifest.Policy{Mode: "after_approval"}})
	if err != nil || a.RefKind != "local_acknowledgement" || len(methods) != 3 {
		t.Fatal("three separate vendor operations failed", err)
	}
}
func TestM3TencentAuditCannotIdentifyTargetAndExpiredUploadStops(t *testing.T) {
	w := localTLS(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"ret":0,"audit_status":3}`)
	})
	c := &Client{Channel: "tencent", Wire: w, Auth: Auth{ID: "fixture", Secret: m3Secret(t, "fixture")}}
	s, err := c.TencentAudit(context.Background(), "com.example.fixture", "123")
	if err != nil || s.Review != "unknown" || s.Availability != "unknown" || s.VersionCode != 0 {
		t.Fatal("unversioned approval became target evidence")
	}
	_, err = c.TencentUpload(context.Background(), "upload", UploadGrant{URL: "https://fixture.cos.ap-shanghai.myqcloud.com/object.apk", Ref: "serial", Method: "PUT", ExpiresAt: time.Now().Add(-time.Second)}, m3File(t, "a.apk"))
	if err == nil || w.count.Load() != 1 {
		t.Fatal("expired grant sent")
	}
}
func TestM3ChangedArtifactRejectedBeforeNetwork(t *testing.T) {
	f := m3File(t, "a.apk")
	if err := os.WriteFile(f.path, bytes.Repeat([]byte("x"), int(f.size)), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := f.open()
	var e *Error
	if !errors.As(err, &e) || e.Effect != "not_sent" {
		t.Fatal("changed frozen artifact allowed")
	}
}
