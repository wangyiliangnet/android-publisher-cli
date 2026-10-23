package channels

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestXiaomiProductionUpdateOmitsScheduleAndPreservesNotes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Channel: "xiaomi", Auth: Auth{ID: "synthetic@example.invalid", Secret: m3Secret(t, "fixture"), PublicKey: &key.PublicKey}}
	notes := "本次更新：\n- 保留空格 &+ 字符。\n"
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		_, params, err := mime.ParseMediaType(q.Headers.Get("Content-Type"))
		if err != nil {
			t.Fatal(err)
		}
		r := multipart.NewReader(strings.NewReader(string(m5Body(t, q))), params["boundary"])
		form, err := r.ReadForm(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		defer form.RemoveAll()
		var data map[string]json.RawMessage
		if json.Unmarshal([]byte(form.Value["RequestData"][0]), &data) != nil || string(data["synchroType"]) != "1" {
			t.Fatal("not a version update")
		}
		var info map[string]json.RawMessage
		if json.Unmarshal(data["appInfo"], &info) != nil {
			t.Fatal("missing appInfo")
		}
		var got string
		if json.Unmarshal(info["updateDesc"], &got) != nil || got != notes {
			t.Fatal("release notes changed")
		}
		if _, ok := info["onlineTime"]; ok {
			t.Fatal("immediate update supplied a scheduled time")
		}
		if _, ok := info["testAccount"]; ok {
			t.Fatal("unconfigured test accounts changed")
		}
		return []byte(`{"result":0}`), nil
	})
	_, err = c.XiaomiPush(context.Background(), "push", XiaomiUpdateInput{PackageName: "com.example.synthetic", AppName: "合成应用", PrivacyURL: "https://example.invalid/privacy", Notes: notes, APK: m3File(t, "release.apk"), Icon: m3File(t, "icon.png")})
	if err != nil || calls != 1 {
		t.Fatalf("single push: calls=%d error=%v", calls, err)
	}
}

func TestTencentProductionMixedABIUsesCompatibleSlotAndNoSchedule(t *testing.T) {
	c := m5Client(t, "tencent")
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		f := productionForm(t, q)
		if f["deploy_type"] != "1" || f["deploy_time"] != "" || f["apk32_flag"] != "1" || f["apk64_flag"] != "2" || f["apk32_file_serial_number"] != "synthetic-serial" || f["apk64_file_serial_number"] != "" {
			t.Fatal("mixed ABI or after-approval fields changed")
		}
		for _, field := range []string{"app_name", "introduce", "one_word_summary", "login_account", "copyright_licences_file_serial_number"} {
			if _, exists := f[field]; exists {
				t.Fatalf("unrequested metadata sent: %s", field)
			}
		}
		return []byte(`{"ret":0}`), nil
	})
	_, err := c.TencentUpdate(context.Background(), "submit", TencentUpdateInput{PackageName: "com.example.synthetic", AppID: "123", Serial: "synthetic-serial", MD5: strings.Repeat("a", 32), Notes: "合成更新说明", ABI: []string{"armeabi-v7a", "arm64-v8a", "x86", "x86_64"}, Policy: manifest.Policy{Mode: "after_approval"}})
	if err != nil || calls != 1 {
		t.Fatalf("single submit: calls=%d error=%v", calls, err)
	}
}

func TestVivoProductionAfterApprovalOmitsScheduledTime(t *testing.T) {
	c := m5Client(t, "vivo")
	calls := 0
	c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		f := productionForm(t, q)
		if f["onlineType"] != "1" || f["scheOnlineTime"] != "" || f["updateDesc"] != "优化操作并修复问题\n" || f["apk"] != "synthetic-serial" || f["compatibleDevice"] != "2" {
			t.Fatal("after-approval or frozen fields changed")
		}
		return []byte(`{"code":0,"subCode":"0"}`), nil
	})
	_, err := c.VivoUpdate(context.Background(), "submit", VivoUpdateInput{PackageName: "com.example.synthetic", VersionCode: 248, Existing: Details{PackageName: "com.example.synthetic", VersionCode: 247, Metadata: map[string]string{"copyrightList": "https://example.invalid/existing-proof"}}, File: VivoFile{SerialNumber: "synthetic-serial", PackageName: "com.example.synthetic", VersionCode: 248, MD5: strings.Repeat("a", 32)}, Patch: map[string]string{"updateDesc": "优化操作并修复问题\n"}, OnlineType: 1, CompatibleDevice: 2})
	if err != nil || calls != 1 {
		t.Fatalf("single submit: calls=%d error=%v", calls, err)
	}
}

func productionForm(t *testing.T, q transport.Request) map[string]string {
	t.Helper()
	values, err := url.ParseQuery(string(m5Body(t, q)))
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		if len(value) != 1 {
			t.Fatal("ambiguous form field")
		}
		out[key] = value[0]
	}
	return out
}

func TestVivoUploadedFileRequiresExactIdentityAndVersion(t *testing.T) {
	file := m5File(t)
	for _, tc := range []struct {
		name, version, pkg, md5, serial string
		accepted                        bool
	}{
		{"number", `248`, "com.example.synthetic", file.MD5(), "serial", true},
		{"canonical string", `"248"`, "com.example.synthetic", file.MD5(), "serial", true},
		{"large precise string", `"9007199254740993"`, "com.example.synthetic", file.MD5(), "serial", true},
		{"fraction", `248.0`, "com.example.synthetic", file.MD5(), "serial", false},
		{"exponent", `"2.48e2"`, "com.example.synthetic", file.MD5(), "serial", false},
		{"leading zero", `"0248"`, "com.example.synthetic", file.MD5(), "serial", false},
		{"overflow", `"9223372036854775808"`, "com.example.synthetic", file.MD5(), "serial", false},
		{"wrong package", `248`, "com.example.other", file.MD5(), "serial", false},
		{"wrong digest", `248`, "com.example.synthetic", strings.Repeat("a", 32), "serial", false},
		{"missing serial", `248`, "com.example.synthetic", file.MD5(), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"code":0,"subCode":"0","data":{"packageName":"` + tc.pkg + `","serialnumber":"` + tc.serial + `","fileMd5":"` + tc.md5 + `","versionCode":` + tc.version + `}}`)
			o, err := decode(body)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parseVivoUploadedFile(o, "com.example.synthetic", file)
			if tc.accepted {
				if err != nil || f.VersionCode <= 0 || f.PackageName != "com.example.synthetic" || f.MD5 != file.MD5() || f.SerialNumber == "" {
					t.Fatal("valid upload rejected", err)
				}
			} else if err == nil {
				t.Fatal("incomplete or mismatched upload accepted")
			}
		})
	}
}

func TestTencentUploadAuthorizationRequiresCompleteOfficialEnvelope(t *testing.T) {
	now := time.Unix(1800000000, 0)
	validURL := "https://fixture.cos.ap-shanghai.myqcloud.com/object.apk?q-sign-time=1800000000%3B1800003600"
	for _, tc := range []struct {
		name, code, url, serial string
		accepted                bool
	}{
		{"accepted", `0`, validURL, "synthetic-serial", true},
		{"missing success", `null`, validURL, "synthetic-serial", false},
		{"string success", `"0"`, validURL, "synthetic-serial", false},
		{"unknown code", `987`, validURL, "synthetic-serial", false},
		{"missing serial", `0`, validURL, "", false},
		{"http upload", `0`, strings.Replace(validURL, "https:", "http:", 1), "synthetic-serial", false},
		{"spoofed COS", `0`, strings.Replace(validURL, ".myqcloud.com", ".myqcloud.com.example.invalid", 1), "synthetic-serial", false},
		{"missing expiry", `0`, "https://fixture.cos.ap-shanghai.myqcloud.com/object.apk", "synthetic-serial", false},
		{"expired", `0`, "https://fixture.cos.ap-shanghai.myqcloud.com/object.apk?q-sign-time=1700000000%3B1700003600", "synthetic-serial", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"ret":` + tc.code + `,"pre_sign_url":"` + tc.url + `","serial_number":"` + tc.serial + `"}`)
			o, err := decode(body)
			if err != nil {
				t.Fatal(err)
			}
			grant, err := parseTencentUploadGrant(o, now)
			if tc.accepted {
				if err != nil || grant.URL != validURL || grant.Ref != tc.serial || grant.Method != "PUT" || !grant.ExpiresAt.After(now) {
					t.Fatal("valid authorization rejected", err)
				}
			} else if err == nil {
				t.Fatal("incomplete or unsafe authorization accepted")
			}
		})
	}
}
