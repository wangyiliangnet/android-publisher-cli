package channels

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestHonorCurrentReleaseUsesExactReadOnlyBoundDraftQuery(t *testing.T) {
	calls := 0
	c := m4Client(t, "honor", func(_ context.Context, q transport.Request) ([]byte, error) {
		calls++
		u, err := url.Parse(q.URL)
		if err != nil || u.Scheme+"://"+u.Host != origins["honor"] || u.Path != "/openapi/v1/publish/get-app-current-release" || len(u.Query()) != 1 || len(u.Query()["appId"]) != 1 || u.Query().Get("appId") != "9223372036854775807" || q.Class != transport.Read || q.Method != "GET" || q.OperationID != "" || q.Body != nil || q.Grant != nil || q.ContentLength != 0 || q.Headers.Get("Authorization") != "Bearer synthetic-bearer" {
			t.Fatal("current release query lost its exact read-only application binding")
		}
		return []byte(`{"code":0,"data":{"appId":9223372036854775807,"releaseId":"synthetic-draft","versionCode":9007199254740993,"versionName":"3.2.7","auditResult":4,"auditMessage":"PRIVATE-AUDIT-MESSAGE","auditAttachment":["https://private.invalid/PRIVATE-ATTACHMENT"],"unknown":"PRIVATE-UNKNOWN"}}`), nil
	})
	d, err := c.HonorCurrentRelease(context.Background(), "9223372036854775807")
	if err != nil || d.ReleaseID != "synthetic-draft" || d.VersionCode != 9007199254740993 || d.VersionName != "3.2.7" || d.AuditResult != 4 || calls != 1 {
		t.Fatalf("bound draft evidence was not retained exactly: %+v %v", d, err)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "https://") {
		t.Fatal("current release query retained raw audit material")
	}
}

func TestHonorCurrentReleaseRejectsUnboundOrAmbiguousEvidenceWithoutRetry(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"releaseId": "synthetic-draft", "versionCode": int64(248), "versionName": "3.2.7", "auditResult": 4}
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing_release_id": func(d map[string]any) { delete(d, "releaseId") },
		"numeric_release_id": func(d map[string]any) { d["releaseId"] = 123 },
		"empty_release_id":   func(d map[string]any) { d["releaseId"] = "" },
		"missing_version":    func(d map[string]any) { delete(d, "versionCode") },
		"null_version":       func(d map[string]any) { d["versionCode"] = nil },
		"string_version":     func(d map[string]any) { d["versionCode"] = "248" },
		"overflow_version":   func(d map[string]any) { d["versionCode"] = json.Number("9223372036854775808") },
		"exponent_version":   func(d map[string]any) { d["versionCode"] = json.Number("2.48e2") },
		"zero_version":       func(d map[string]any) { d["versionCode"] = 0 },
		"missing_name":       func(d map[string]any) { delete(d, "versionName") },
		"null_name":          func(d map[string]any) { d["versionName"] = nil },
		"empty_name":         func(d map[string]any) { d["versionName"] = "" },
		"missing_audit":      func(d map[string]any) { delete(d, "auditResult") },
		"null_audit":         func(d map[string]any) { d["auditResult"] = nil },
		"string_audit":       func(d map[string]any) { d["auditResult"] = "4" },
		"unknown_audit":      func(d map[string]any) { d["auditResult"] = 5 },
		"negative_audit":     func(d map[string]any) { d["auditResult"] = -1 },
		"wrong_app":          func(d map[string]any) { d["appId"] = 124 },
		"malformed_app":      func(d map[string]any) { d["appId"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			data := valid()
			mutate(data)
			calls := 0
			c := m4Client(t, "honor", func(context.Context, transport.Request) ([]byte, error) {
				calls++
				return raw(map[string]any{"code": 0, "data": data}), nil
			})
			if _, err := c.HonorCurrentRelease(context.Background(), "123"); err == nil || calls != 1 {
				t.Fatal("ambiguous draft was accepted or read was retried")
			}
		})
	}
	for _, body := range []string{
		`{}`, `{"code":null}`, `{"code":"0"}`, `{"code":400,"data":{"releaseId":"synthetic","versionCode":248,"versionName":"3.2.7","auditResult":4}}`,
		`{"code":0}`, `{"code":0,"data":null}`, `{"code":0,"data":[]}`, `{"code":0,"data":[{"releaseId":"synthetic","versionCode":248,"versionName":"3.2.7","auditResult":4}]}`,
		`{"code":0,"data":{"releaseId":"synthetic","versionCode":248,"versionCode":249,"versionName":"3.2.7","auditResult":4}}`,
		`{"releaseId":"synthetic","versionCode":248,"versionName":"3.2.7","auditResult":4}`,
	} {
		calls := 0
		c := m4Client(t, "honor", func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte(body), nil })
		if _, err := c.HonorCurrentRelease(context.Background(), "123"); err == nil || calls != 1 {
			t.Fatal("missing or noncanonical success envelope was accepted or retried")
		}
	}
}

func TestHonorCurrentReleaseKeepsKnownStatesDistinct(t *testing.T) {
	for _, state := range []int{0, 1, 2, 3, 4} {
		o, err := decode(raw(map[string]any{"code": 0, "data": map[string]any{"releaseId": "synthetic-draft", "versionCode": 248, "versionName": "3.2.7", "auditResult": state}}))
		if err != nil {
			t.Fatal(err)
		}
		d, err := parseHonorCurrentRelease(o, "123")
		if err != nil || d.AuditResult != int64(state) {
			t.Fatalf("current release state %d lost: %v", state, err)
		}
	}
	for _, app := range []string{"", "0", "0123", "123,124", "9223372036854775808", "123&appId=124"} {
		calls := 0
		c := m4Client(t, "honor", func(context.Context, transport.Request) ([]byte, error) { calls++; return nil, nil })
		if _, err := c.HonorCurrentRelease(context.Background(), app); err == nil || calls != 0 {
			t.Fatal("invalid application ID reached the query sender")
		}
	}
}
