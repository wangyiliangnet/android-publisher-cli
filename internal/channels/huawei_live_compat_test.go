package channels

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHuaweiExactObservedOBSHostAndPrivateDiagnostic(t *testing.T) {
	const host = "nsp-appgallery-agcfs-drcn.obs.cn-north-2.myhuaweicloud.cn"
	for _, endpoint := range []string{"https://" + host + "/object", "https://other.myhuaweicloud.cn/object", "https://" + host + ".example.invalid/object", "http://" + host + "/object", "https://" + host + ":8443/object"} {
		if allowedUpload("huawei", endpoint) != (endpoint == "https://"+host+"/object") {
			t.Fatal("unexpected exact-host decision")
		}
	}
	target := Target{}
	target.Account.Channel = "huawei"
	o := raw(map[string]any{"ret": map[string]int{"code": 0}, "urlInfo": map[string]any{"url": "https://" + host + "/object?signature=SYNTHETIC-PRIVATE", "method": "PUT", "objectId": "SYNTHETIC-PRIVATE", "headers": map[string]string{"host": host, "authorization": "AWS4-HMAC-SHA256 SYNTHETIC-PRIVATE", "content-type": "application/octet-stream", "x-amz-date": "SYNTHETIC-PRIVATE", "user-agent": "SYNTHETIC-PRIVATE"}}})
	d := fiveResponseDiagnostic(target, "authorize", strings.Repeat("a", 64), 200, true, o)
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "SYNTHETIC-PRIVATE") || strings.Contains(string(b), "signature=") || d.UploadHost != host || d.Shape["urlInfo.headers.normalized.user-agent"] != "string" || d.GrantAuthKind != "AWS4-HMAC-SHA256" {
		t.Fatal("diagnostic leaked private values or missed normalized headers")
	}
}
