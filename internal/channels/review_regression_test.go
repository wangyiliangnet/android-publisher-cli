package channels

import (
	"context"
	"errors"
	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestVendorBusinessBudgetAllowsDocumentedParsingDuration(t *testing.T) {
	p := controlledPolicy(account.Scope{Channel: "tencent", Account: "synthetic"}, origins["tencent"], transport.Request{Class: transport.Write})
	if p.Timeout < time.Minute || p.ResponseHeaderTimeout < time.Minute || p.Timeout > 90*time.Second || p.ResponseHeaderTimeout > p.Timeout {
		t.Fatal("vendor APK operation budget is shorter than documented parsing duration")
	}
}

func TestAllocatedAuthorizationValidationNeverClaimsUnsent(t *testing.T) {
	file := m4File(t)
	for _, scenario := range []string{"huawei-host", "huawei-headers", "tencent-expired"} {
		t.Run(scenario, func(t *testing.T) {
			channel := "huawei"
			if strings.HasPrefix(scenario, "tencent") {
				channel = "tencent"
			}
			calls := 0
			c := m4Client(t, channel, func(_ context.Context, q transport.Request) ([]byte, error) {
				calls++
				if q.Class != transport.Write || q.OperationID != "authorize" {
					t.Error("allocation not classified as journaled single write")
				}
				if channel == "tencent" {
					return raw(map[string]any{"ret": 0, "serial_number": "allocated-serial", "pre_sign_url": "https://fixture.cos.ap-test.myqcloud.com/file?q-sign-time=1%3B2"}), nil
				}
				host := "synthetic.dbankcdn.com"
				if scenario == "huawei-host" {
					host = "unknown.invalid"
				}
				return raw(map[string]any{"ret": map[string]int{"code": 0}, "urlInfo": map[string]any{"url": "https://" + host + "/object", "objectId": "allocated-object", "method": "PUT"}}), nil
			})
			c.Auth.ID = "fixture"
			c.Auth.Secret = m3Secret(t, "fixture")
			var err error
			if channel == "huawei" {
				_, err = c.HuaweiAuthorize(context.Background(), "authorize", "app", file, nil)
			} else {
				_, err = c.TencentAuthorize(context.Background(), "authorize", "com.example.fixture", "123", file)
			}
			var e *Error
			if !errors.As(err, &e) || e.Effect != "unknown" || calls != 1 {
				t.Fatal("allocated authorization became retryable", err, calls)
			}
		})
	}
}
func TestHuaweiHeaderCaseConflictsAlwaysRejected(t *testing.T) {
	file := m4File(t)
	c := m4Client(t, "huawei", func(context.Context, transport.Request) ([]byte, error) {
		headers := map[string]string{"Authorization": "AWS4-HMAC-SHA256 synthetic", "authorization": "OBS conflicting", "x-amz-content-sha256": file.SHA256(), "x-amz-date": "20270115T080000Z", "Host": "synthetic.dbankcdn.com", "user-agent": "synthetic-agent", "Content-Type": "application/octet-stream"}
		return raw(map[string]any{"ret": map[string]int{"code": 0}, "urlInfo": map[string]any{"url": "https://synthetic.dbankcdn.com/object", "objectId": "object", "method": "PUT", "headers": headers}}), nil
	})
	for i := 0; i < 30; i++ {
		if _, err := c.HuaweiAuthorize(context.Background(), "authorize", "app", file, nil); err == nil {
			t.Fatal("ambiguous case variant accepted")
		}
	}
}
func TestUploadReadsVerifiedSnapshotAfterSourceInodeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.apk")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sent string
	c := &Client{Channel: "tencent", Wire: m4Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
		if err := os.WriteFile(path, []byte("tampered"), 0600); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(q.Body)
		if err != nil {
			t.Fatal(err)
		}
		sent = string(b)
		return nil, nil
	})}
	_, err = c.TencentUpload(context.Background(), "upload", UploadGrant{URL: "https://fixture.cos.ap-test.myqcloud.com/object", Ref: "serial", Method: http.MethodPut, ExpiresAt: time.Now().Add(time.Minute)}, f)
	if err != nil || sent != "original" {
		t.Fatal("sent bytes do not match verified snapshot", err)
	}
}
func TestPlansRejectUnsupportedAndEmptyPatchesBeforeAnyWrite(t *testing.T) {
	for _, channel := range []string{"xiaomi", "tencent", "oppo", "vivo"} {
		for _, empty := range []bool{false, true} {
			t.Run(channel+map[bool]string{true: "-empty", false: "-unsupported"}[empty], func(t *testing.T) {
				target := runtimeTarget(t, channel)
				target.Icon = target.APK
				target.Existing.UpdateAllowed = true
				target.Metadata["app_name"] = "Synthetic"
				target.Metadata["privacy_url"] = "https://example.invalid"
				target.Existing.Metadata = copyFields(target.Metadata)
				key := "unsupported_metadata"
				value := "new text"
				if empty {
					value = ""
					key = map[string]string{"xiaomi": "app_name", "tencent": "introduce", "oppo": "special_url", "vivo": "simpleDesc"}[channel]
				}
				target.Patch = map[string]manifest.Patch{key: {Action: "set", Value: value}}
				calls := 0
				run := &StaticRuntime{Wire: runtimeSender(func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil }), Grants: &MemoryGrants{}}
				a, err := NewAdapter([]Target{target}, run)
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.BuildPlan(runtimeRequest(target, ""))
				if err == nil || calls != 0 {
					t.Fatal("unimplemented/clear patch escaped planning")
				}
			})
		}
	}
}
func TestM4NotesLengthCheckedBeforeAllocation(t *testing.T) {
	for _, channel := range []string{"huawei", "honor"} {
		for _, count := range []int{500, 501} {
			target := runtimeTarget(t, channel)
			target.Notes = strings.Repeat("文", count)
			calls := 0
			a, err := NewAdapter([]Target{target}, &StaticRuntime{Wire: runtimeSender(func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil }), Grants: &MemoryGrants{}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.BuildPlan(runtimeRequest(target, ""))
			if (err == nil) != (count == 500) || calls != 0 {
				t.Fatal("notes limit was not checked before business allocation", channel, count, err)
			}
		}
	}
}
func TestM5DirectEmptyPatchesNeverSend(t *testing.T) {
	file := m5File(t)
	calls := 0
	c := m5Client(t, "oppo")
	c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil })
	in := m5OPPOUpdate(file)
	in.Patch["special_url"] = ""
	if _, err := c.OPPOUpdate(context.Background(), "submit", in); err == nil || calls != 0 {
		t.Fatal("OPPO clear sent")
	}
	c = m5Client(t, "vivo")
	c.Wire = m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil })
	_, err := c.VivoUpdate(context.Background(), "submit", VivoUpdateInput{PackageName: "com.example.synthetic", VersionCode: 43, Existing: Details{PackageName: "com.example.synthetic", VersionCode: 42, Metadata: map[string]string{"copyrightList": "existing-proof"}}, File: VivoFile{PackageName: "com.example.synthetic", VersionCode: 43, SerialNumber: "serial", MD5: strings.Repeat("a", 32)}, Patch: map[string]string{"simpleDesc": ""}, OnlineType: 1, CompatibleDevice: 1})
	if err == nil || calls != 0 {
		t.Fatal("vivo clear sent")
	}
}

func TestAdapterBindingRejectsChangedPublicConfiguration(t *testing.T) {
	base := runtimeTarget(t, "vivo")
	q := runtimeRequest(base, "upload")
	calls := 0
	for _, change := range []func(*Target){
		func(t *Target) { t.Account.Account = "another-account" }, func(t *Target) { t.AppID = "another-app" }, func(t *Target) { t.LanguageID = "other-language" },
		func(t *Target) { t.CompatibleDevice = 2 }, func(t *Target) { t.Credential.ID = "another-client-id" }, func(t *Target) { v := true; t.Mainland = &v },
	} {
		changed := cloneTarget(base)
		change(&changed)
		a, err := NewAdapter([]Target{changed}, &StaticRuntime{Wire: runtimeSender(func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil }), Grants: &MemoryGrants{}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.BuildPlan(q); err == nil {
			t.Fatal("configuration changed under a frozen plan")
		}
		if _, err = a.ExecuteOne(context.Background(), q); err == nil {
			t.Fatal("configuration changed under a persisted intent")
		}
		if _, err = a.Reconcile(context.Background(), q); err == nil {
			t.Fatal("reconciliation changed accounts")
		}
	}
	if calls != 0 {
		t.Fatal("configuration mismatch reached HTTP")
	}
}

func TestFrozenBlobUsesExplicitPublicWireName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, []byte("synthetic apk"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := NewNamedFile(path, "release.apk")
	if err != nil || file.Name() != "release.apk" {
		t.Fatal("frozen blob lost APK filename", err)
	}
	for _, name := range []string{"../release.apk", "bad\r\n.apk", "folder\\a.apk", ""} {
		if _, err = NewNamedFile(path, name); err == nil {
			t.Fatal("untrusted wire filename accepted")
		}
	}
}
