package channels

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type m4Sender func(context.Context, transport.Request) ([]byte, error)

func (f m4Sender) Send(ctx context.Context, q transport.Request) ([]byte, error) { return f(ctx, q) }

func m4Client(t *testing.T, channel string, f m4Sender) *Client {
	t.Helper()
	token, err := credentials.NewSecret([]byte("synthetic-bearer"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(token.Destroy)
	return &Client{Channel: channel, Auth: Auth{Token: token}, Wire: f, Now: func() time.Time { return time.Unix(1800000000, 0).UTC() }}
}

func m4File(t *testing.T) File {
	t.Helper()
	p := filepath.Join(t.TempDir(), "synthetic-release.apk")
	if err := os.WriteFile(p, []byte("synthetic APK bytes; not a real release"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func m4Body(t *testing.T, q transport.Request) object {
	t.Helper()
	if q.Body == nil {
		t.Fatal("missing request body")
	}
	defer q.Body.Close()
	b, err := io.ReadAll(q.Body)
	if err != nil {
		t.Fatal(err)
	}
	o, err := decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestServiceAccountJWTOwnPS256Verification(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := credentials.NewSecret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	now := time.Unix(1800000000, 0)
	token, err := ServiceAccountJWT("synthetic-key", "synthetic-subaccount", secret, now)
	if err != nil {
		t.Fatal(err)
	}
	defer token.Destroy()
	parts := strings.Split(string(token.Bytes()), ".")
	if len(parts) != 3 {
		t.Fatal("JWT parts")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	h, err := decode(hb)
	if err != nil {
		t.Fatal(err)
	}
	if h.str("alg") != "PS256" || h.str("typ") != "JWT" || h.str("kid") != "synthetic-key" {
		t.Fatal("JWT header")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	p, err := decode(pb)
	if err != nil {
		t.Fatal(err)
	}
	iat, iok := p.num("iat")
	exp, eok := p.num("exp")
	if !iok || !eok || iat != now.Unix() || exp-iat != 3600 || p.str("iss") != "synthetic-subaccount" || p.str("aud") != huaweiAudience {
		t.Fatal("JWT claims")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err = rsa.VerifyPSS(&key.PublicKey, crypto.SHA256, digest[:], sig, &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(token.String(), "synthetic") {
		t.Fatal("secret formatting")
	}
	bad, _ := credentials.NewSecret([]byte("not PKCS8"))
	defer bad.Destroy()
	if _, err = ServiceAccountJWT("key", "sub", bad, now); err == nil {
		t.Fatal("invalid private key accepted")
	}
}

func TestHuaweiCurrentOBSContractAndHeaders(t *testing.T) {
	file := m4File(t)
	calls := 0
	var grant UploadGrant
	c := m4Client(t, "huawei", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		u, _ := url.Parse(q.URL)
		if calls == 1 {
			if q.Method != "GET" || u.Path != "/api/publish/v2/upload-url/for-obs" || u.Query().Get("sha256") != file.SHA256() || u.Query().Get("contentLength") != decimal(file.Size()) || u.Query().Get("appId") != "test-app" {
				t.Fatal("wrong authorize contract")
			}
			headers := map[string]string{"Authorization": "AWS4-HMAC-SHA256 synthetic-signature", "x-amz-content-sha256": file.SHA256(), "x-amz-date": "20270115T080000Z", "Host": "synthetic.dbankcdn.com", "user-agent": "synthetic-agent", "Content-Type": "application/octet-stream"}
			return raw(map[string]any{"ret": map[string]int{"code": 0}, "urlInfo": map[string]any{"url": "https://synthetic.dbankcdn.com/synthetic-object", "objectId": "synthetic-object", "method": "PUT", "headers": headers}}), nil
		}
		if q.Method != "PUT" || q.Class != transport.Write || q.Grant == nil || q.Headers.Get("Authorization") != grant.Headers.Get("Authorization") || q.Grant.Headers.Get("Host") != "synthetic.dbankcdn.com" || q.Headers.Get("Host") != "" || q.ContentLength != file.Size() {
			t.Fatal("wrong OBS upload")
		}
		b, err := io.ReadAll(q.Body)
		q.Body.Close()
		if err != nil || int64(len(b)) != file.Size() {
			t.Fatal("wrong upload bytes")
		}
		return nil, nil
	})
	var err error
	grant, err = c.HuaweiAuthorize(context.Background(), "authorize", "test-app", file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExpiresAt.Sub(c.now()) != 4*time.Minute {
		t.Fatal("not conservative expiry")
	}
	accepted, err := c.HuaweiUpload(context.Background(), "upload-operation", grant, file)
	if err != nil || accepted.Ref != "synthetic-object" || calls != 2 {
		t.Fatalf("upload failure: %v", err)
	}
	for _, mutate := range []func(*UploadGrant){func(g *UploadGrant) { g.Headers.Set("Authorization", "Bearer synthetic-bearer") }, func(g *UploadGrant) { g.Headers.Set("Host", "attacker.invalid") }, func(g *UploadGrant) { g.Headers.Set("X-Amz-Content-Sha256", strings.Repeat("0", 64)) }, func(g *UploadGrant) { g.Headers.Del("User-Agent") }} {
		bad := grant
		bad.Headers = grant.Headers.Clone()
		mutate(&bad)
		if _, err = c.HuaweiUpload(context.Background(), "bad-upload", bad, file); err == nil {
			t.Fatal("invalid grant accepted")
		}
	}
	if calls != 2 {
		t.Fatal("invalid grant sent")
	}
}

func TestHuaweiBindAndSubmissionHaveNoBlindRetry(t *testing.T) {
	file := m4File(t)
	calls := 0
	c := m4Client(t, "huawei", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if calls == 1 {
			o := m4Body(t, q)
			files := o.list("files")
			if q.Method != "PUT" || len(files) != 1 || files[0].str("fileDestUrl") != "object-1" || files[0].str("fileName") != file.Name() {
				t.Fatal("wrong bind")
			}
			return []byte(`{"ret":{"code":0}}`), nil
		}
		if q.Method != "POST" || q.OperationID != "submit-1" {
			t.Fatal("wrong submit")
		}
		return []byte(`{"ret":{"code":204144660}}`), nil
	})
	if _, err := c.HuaweiBind(context.Background(), "bind-1", "app", file, UploadGrant{Ref: "object-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HuaweiSubmit(context.Background(), "submit-1", "app", nil); err == nil || calls != 2 {
		t.Fatal("compile code incorrectly accepted or retried")
	}
}

func TestHuaweiMissingNestedCodeIsUnknown(t *testing.T) {
	for _, response := range []string{`{}`, `{"ret":{}}`, `{"ret":{"code":null}}`, `{"ret":{"code":"0"}}`} {
		c := m4Client(t, "huawei", func(context.Context, transport.Request) ([]byte, error) { return []byte(response), nil })
		if _, err := c.HuaweiSubmit(context.Background(), "submit", "app", nil); err == nil {
			t.Fatal("missing mandatory success code accepted")
		}
	}
}

func TestHuaweiVerifyAppDoesNotConfuseDisplayNameWithPackage(t *testing.T) {
	response := `{"ret":{"code":0},"appids":[{"key":"A display name","value":"app-123"}]}`
	c := m4Client(t, "huawei", func(_ context.Context, q transport.Request) ([]byte, error) {
		u, _ := url.Parse(q.URL)
		if u.Path != "/api/publish/v2/appid-list" || u.Query().Get("packageName") != "example.synthetic" || u.Query().Get("packageTypes") != "1" {
			t.Fatal("wrong package lookup")
		}
		return []byte(response), nil
	})
	if err := c.HuaweiVerifyApp(context.Background(), "app-123", "example.synthetic"); err != nil {
		t.Fatal(err)
	}
	response = `{"ret":{"code":0},"appids":[{"key":"example.synthetic","value":"wrong-app"}]}`
	if err := c.HuaweiVerifyApp(context.Background(), "app-123", "example.synthetic"); err == nil {
		t.Fatal("wrong app accepted through display name")
	}
}

func TestHuaweiStatusSeparatesEditableAndLiveVersions(t *testing.T) {
	c := m4Client(t, "huawei", func(context.Context, transport.Request) ([]byte, error) {
		return []byte(`{"ret":{"code":0},"appInfo":{"versionCode":12,"releaseState":4,"onShelfVersionCode":11}}`), nil
	})
	s, err := c.HuaweiStatus(context.Background(), "app", 12)
	if err != nil || s.Review != "in_review" || s.Availability != "unknown" {
		t.Fatalf("wrong target status: %+v %v", s, err)
	}
	s, err = c.HuaweiStatus(context.Background(), "app", 11)
	if err != nil || s.Review != "unknown" || s.Availability != "live" {
		t.Fatalf("wrong previous status: %+v %v", s, err)
	}
	s, err = c.HuaweiStatus(context.Background(), "app", 13)
	if err != nil || s.Review != "unknown" || s.Availability != "unknown" {
		t.Fatal("old evidence contaminated target")
	}
}

func TestHonorTokenUsesFormAndMandatoryEvidence(t *testing.T) {
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		if q.URL != "https://iam.developer.honor.com/auth/token" || q.Class != transport.Lifecycle || q.Headers.Get("Authorization") != "" {
			t.Fatal("wrong token route")
		}
		b, _ := io.ReadAll(q.Body)
		q.Body.Close()
		v, _ := url.ParseQuery(string(b))
		if v.Get("client_id") != "id" || v.Get("client_secret") != "汉字&+\n" || v.Get("grant_type") != "client_credentials" {
			t.Fatal("token form altered bytes")
		}
		return []byte(`{"access_token":"synthetic-new-token","expires_in":3600,"token_type":"Bearer"}`), nil
	})
	s, _ := credentials.NewSecret([]byte("汉字&+\n"))
	defer s.Destroy()
	c.Auth.Secret = s
	c.Auth.ID = "id"
	r, err := c.HonorToken(context.Background())
	if err != nil || r.Effect != "confirmed_applied" || r.ExpiresAt.Sub(c.now()) != time.Hour {
		t.Fatalf("token failed %v", err)
	}
	defer r.Value.Destroy()
	c.Wire = m4Sender(func(context.Context, transport.Request) ([]byte, error) {
		return []byte(`{"access_token":"synthetic-new-token","expires_in":3600}`), nil
	})
	if _, err = c.HonorToken(context.Background()); err == nil {
		t.Fatal("missing token type accepted")
	}
}

func TestHonorAuthorizeRetainsLongIDAndExactExpiry(t *testing.T) {
	file := m4File(t)
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		if q.Class != transport.Write || q.OperationID != "authorize" || q.Method != "POST" {
			t.Fatal("wrong authorize")
		}
		defer q.Body.Close()
		b, _ := io.ReadAll(q.Body)
		var list []map[string]json.RawMessage
		if json.Unmarshal(b, &list) != nil || len(list) != 1 {
			t.Fatal("wrong upload descriptor")
		}
		return raw(map[string]any{"code": 0, "data": []any{map[string]any{"fileName": file.Name(), "objectId": json.Number("9223372036854775807"), "expireTime": int64(1800000600), "uploadUrl": "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=9223372036854775807"}}}), nil
	})
	g, err := c.HonorAuthorize(context.Background(), "authorize", "123", file)
	if err != nil || g.Ref != "9223372036854775807" || g.ExpiresAt.Unix() != 1800000600 {
		t.Fatalf("lost ID or expiry: %v", err)
	}
	c.Wire = m4Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		o := m4Body(t, q)
		rows := o.list("bindingFileList")
		if len(rows) != 1 || string(rows[0]["objectId"]) != "9223372036854775807" {
			t.Fatal("bind ID rounded")
		}
		return []byte(`{"code":0}`), nil
	})
	if _, err = c.HonorBind(context.Background(), "bind", "123", g); err != nil {
		t.Fatal(err)
	}
}

func TestHonorUploadUsesAuthenticatedMultipartOnlyOnExactAPI(t *testing.T) {
	file := m4File(t)
	calls := 0
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if q.Grant != nil || q.Headers.Get("Authorization") != "Bearer synthetic-bearer" || !strings.HasPrefix(q.Headers.Get("Content-Type"), "multipart/form-data;") {
			t.Fatal("wrong authenticated upload")
		}
		io.Copy(io.Discard, q.Body)
		q.Body.Close()
		return []byte(`{"code":0}`), nil
	})
	g := UploadGrant{URL: origins["honor"] + "/openapi/v1/publish/file-upload?appId=123&objectId=456", Ref: "456", Method: "POST", ExpiresAt: c.now().Add(time.Minute)}
	if _, err := c.HonorUpload(context.Background(), "upload", "123", g, file); err != nil {
		t.Fatal(err)
	}
	g.URL += "&redirect=evil"
	if _, err := c.HonorUpload(context.Background(), "upload", "123", g, file); err == nil || calls != 1 {
		t.Fatal("extra upload parameters accepted")
	}
}

func TestHonorSubmitRequiresReleaseID(t *testing.T) {
	for _, response := range []string{`{"code":0}`, `{"code":0,"data":null}`, `{"code":0,"data":123}`, `{"data":"release"}`} {
		calls := 0
		c := m4Client(t, "honor", func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte(response), nil })
		if _, err := c.HonorSubmit(context.Background(), "submit", "123", nil); err == nil || calls != 1 {
			t.Fatal("invalid submit evidence accepted/retried")
		}
	}
}

func TestHonorAuditCorrelatesExactAppAndRelease(t *testing.T) {
	response := `{"code":0,"data":[{"appId":123,"releaseId":"release-1","auditResult":1}]}`
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		o := m4Body(t, q)
		rows := o.list("appId")
		if len(rows) != 1 || rows[0].id("appId") != "123" || rows[0].str("releaseId") != "release-1" || q.Class != transport.Query {
			t.Fatal("wrong audit request")
		}
		return []byte(response), nil
	})
	s, err := c.HonorAudit(context.Background(), "123", "release-1", 99)
	if err != nil || s.Review != "approved" || s.Availability != "unknown" {
		t.Fatalf("audit failure %v", err)
	}
	response = `{"code":0,"data":[{"appId":123,"releaseId":"release-old","auditResult":1}]}`
	if _, err = c.HonorAudit(context.Background(), "123", "release-1", 99); err == nil {
		t.Fatal("old release accepted")
	}
	response = `{"code":0,"data":[{"appId":123,"releaseId":"release-1","auditResult":99}]}`
	s, err = c.HonorAudit(context.Background(), "123", "release-1", 99)
	if err != nil || s.Review != "unknown" {
		t.Fatal("unknown audit enum promoted")
	}
}

func TestHonorLanguagePatchPreservesSnapshotAndOtherLanguages(t *testing.T) {
	calls := 0
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"code":0,"data":{"basicInfo":{"appId":123},"languageInfo":[{"languageId":"zh-CN","appName":"应用","intro":"原介绍","briefIntro":"原短介绍","newFeature":"原说明"},{"languageId":"en-US","appName":"App","intro":"Intro","briefIntro":"Brief","newFeature":"Notes"}]}}`), nil
		}
		o := m4Body(t, q)
		setAll, ok := o.num("setAll")
		rows := o.list("languageInfoList")
		if !ok || setAll != 0 || len(rows) != 1 || rows[0].str("languageId") != "zh-CN" || rows[0].str("appName") != "应用" || rows[0].str("intro") != "原介绍" || rows[0].str("briefIntro") != "原短介绍" || rows[0].str("newFeature") != "修复 & +\n换行" {
			t.Fatal("language patch lost prior metadata")
		}
		return []byte(`{"code":0}`), nil
	})
	snapshot, err := c.HonorLanguageSnapshot(context.Background(), "123", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	text := "修复 & +\n换行"
	if _, err := c.HonorLanguagePatchFrozen(context.Background(), "patch", snapshot, text); err != nil {
		t.Fatal(err)
	}
	if _, err := c.HonorLanguagePatch(context.Background(), "clear", snapshot, ""); err == nil || calls != 2 {
		t.Fatal("unverified clear sent")
	}
	snapshot.Intro = "mutated-after-freeze"
	if _, err := c.HonorLanguagePatchFrozen(context.Background(), "changed", snapshot, text); err == nil || calls != 2 {
		t.Fatal("mutated baseline sent")
	}
}

func TestHonorDetailsChecksPackageOwnershipAndRedactsSecretKey(t *testing.T) {
	calls := 0
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if calls == 1 {
			u, _ := url.Parse(q.URL)
			if u.Query().Get("pkgName") != "example.synthetic" {
				t.Fatal("wrong package parameter")
			}
			return []byte(`{"code":0,"data":[{"appId":123,"packageName":"example.synthetic"}]}`), nil
		}
		return []byte(`{"code":0,"data":{"basicInfo":{"appId":123,"packageName":"example.synthetic","secretKey":"never-expose"},"releaseInfo":{"versionCode":12}}}`), nil
	})
	d, err := c.HonorDetails(context.Background(), "example.synthetic")
	if err != nil || d.AppID != "123" || d.Metadata["live_version_code"] != "12" {
		t.Fatalf("details failed %v", err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "never-expose") {
		t.Fatal("secret metadata escaped")
	}
}

func TestHuaweiLanguageSnapshotAndPatchDoNotFetchAtWriteTime(t *testing.T) {
	calls := 0
	c := m4Client(t, "huawei", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"ret":{"code":0},"languages":[{"lang":"zh-CN","appName":"应用","appDesc":"原介绍","briefInfo":"原短介绍","newFeatures":"原说明"}]}`), nil
		}
		if q.Method != "PUT" || q.Class != transport.Write || !strings.Contains(q.URL, "/api/publish/v2/app-language-info?") {
			t.Fatal("wrong language write")
		}
		o := m4Body(t, q)
		if o.str("appName") != "应用" || o.str("appDesc") != "原介绍" || o.str("briefInfo") != "原短介绍" || o.str("newFeatures") != "目标说明" || o.str("lang") != "zh-CN" {
			t.Fatal("language baseline lost")
		}
		return []byte(`{"ret":{"code":0}}`), nil
	})
	snapshot, err := c.HuaweiLanguageSnapshot(context.Background(), "app", "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.HuaweiLanguagePatch(context.Background(), "notes", snapshot, "目标说明"); err != nil || calls != 2 {
		t.Fatalf("language patch failure %v", err)
	}
	if _, err = c.HuaweiLanguagePatch(context.Background(), "clear", snapshot, ""); err == nil || calls != 2 {
		t.Fatal("clear sent")
	}
}
