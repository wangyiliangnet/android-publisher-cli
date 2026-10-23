package channels

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestHuaweiHonorAfterApprovalSubmissionContract(t *testing.T) {
	for _, channel := range []string{"huawei", "honor"} {
		t.Run(channel, func(t *testing.T) {
			calls := 0
			c := m4Client(t, channel, func(_ context.Context, q transport.Request) ([]byte, error) {
				calls++
				u, err := url.Parse(q.URL)
				if err != nil || q.Method != "POST" || q.Class != transport.Write || q.OperationID != "submit-immediate" || u.Query().Get("appId") != "123" {
					t.Fatal("submission lost exact operation or application binding")
				}
				if channel == "huawei" {
					if u.Path != "/api/publish/v2/app-submit" || len(u.Query()) != 2 || u.Query().Get("releaseType") != "1" || u.Query().Has("releaseTime") || q.Body != nil {
						t.Fatal("Huawei immediate publication gained a schedule or extra mutation")
					}
					return []byte(`{"ret":{"code":0}}`), nil
				}
				body := m4Body(t, q)
				releaseType, ok := body.num("releaseType")
				if u.Path != "/openapi/v1/publish/submit-audit" || len(u.Query()) != 1 || !ok || releaseType != 1 || len(body) != 1 {
					t.Fatal("Honor immediate publication gained a schedule or extra mutation")
				}
				return []byte(`{"code":0,"data":"synthetic-release"}`), nil
			})
			var accepted Acceptance
			var err error
			if channel == "huawei" {
				accepted, err = c.HuaweiSubmit(context.Background(), "submit-immediate", "123", nil)
			} else {
				accepted, err = c.HonorSubmit(context.Background(), "submit-immediate", "123", nil)
			}
			if err != nil || accepted.Ref == "" || calls != 1 {
				t.Fatalf("immediate submission did not obtain one verified receipt: %v", err)
			}
		})
	}
}

func TestHuaweiSealedGrantParserRequiresSuccessAndKeepsOriginalLifetime(t *testing.T) {
	file := m4File(t)
	started := time.Unix(1800000000, 0).UTC()
	response := func() object {
		o, err := decode(raw(map[string]any{
			"ret": map[string]int{"code": 0},
			"urlInfo": map[string]any{
				"url": "https://synthetic.dbankcdn.com/object.apk", "objectId": "object.apk", "method": "PUT",
				"headers": map[string]string{"Authorization": "AWS4-HMAC-SHA256 synthetic", "x-amz-content-sha256": file.SHA256(), "x-amz-date": "20270115T080000Z", "Host": "synthetic.dbankcdn.com", "user-agent": "synthetic-agent", "Content-Type": "application/octet-stream"},
			},
		}))
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	grant, err := parseHuaweiUploadGrant(response(), file, started)
	if err != nil || !grant.ExpiresAt.Equal(started.Add(4*time.Minute)) {
		t.Fatalf("sealed grant lost original lifetime: %v", err)
	}
	if validateHuaweiGrant(grant, file, started.Add(4*time.Minute)) == nil {
		t.Fatal("recovered grant became usable at or after its original expiry")
	}
	for _, code := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"code":null}`), json.RawMessage(`{"code":"0"}`), json.RawMessage(`{"code":204144660}`)} {
		o := response()
		o["ret"] = code
		if _, err := parseHuaweiUploadGrant(o, file, started); err == nil {
			t.Fatal("unverified success envelope accepted during local recovery")
		}
	}
	if _, err := parseHuaweiUploadGrant(response(), file, time.Time{}); err == nil {
		t.Fatal("missing original request time accepted")
	}
}

func TestHonorSealedGrantRejectsAmbiguousObjectAndExpiry(t *testing.T) {
	file := m4File(t)
	now := time.Unix(1800000000, 0).UTC()
	valid := func() map[string]any {
		return map[string]any{"fileName": file.Name(), "objectId": json.Number("9223372036854775807"), "expireTime": now.Add(time.Minute).Unix(), "uploadUrl": "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=9223372036854775807"}
	}
	parse := func(row map[string]any, code any) (UploadGrant, error) {
		o, err := decode(raw(map[string]any{"code": code, "data": []any{row}}))
		if err != nil {
			return UploadGrant{}, err
		}
		return parseHonorUploadGrant(o, "123", file, now)
	}
	grant, err := parse(valid(), 0)
	if err != nil || grant.Ref != "9223372036854775807" || grant.ExpiresAt.Unix() != now.Add(time.Minute).Unix() {
		t.Fatalf("exact Long or absolute expiry lost: %v", err)
	}
	for _, mutate := range []func(map[string]any){
		func(row map[string]any) { row["objectId"] = json.Number("9223372036854775808") },
		func(row map[string]any) { row["objectId"] = json.Number("9.223372036854775807e18") },
		func(row map[string]any) { row["objectId"] = "09223372036854775807" },
		func(row map[string]any) { row["expireTime"] = now.Unix() },
		func(row map[string]any) { row["expireTime"] = "1800000060" },
		func(row map[string]any) { row["fileName"] = "other.apk" },
		func(row map[string]any) {
			row["uploadUrl"] = "http://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=9223372036854775807"
		},
		func(row map[string]any) {
			row["uploadUrl"] = "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=124&objectId=9223372036854775807"
		},
		func(row map[string]any) {
			row["uploadUrl"] = "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=123&objectId=9223372036854775807&objectId=1"
		},
	} {
		row := valid()
		mutate(row)
		if _, err := parse(row, 0); err == nil {
			t.Fatal("ambiguous sealed allocation accepted")
		}
	}
	for _, code := range []any{nil, "0", 400} {
		if _, err := parse(valid(), code); err == nil {
			t.Fatal("unverified success envelope accepted during local recovery")
		}
	}
}
