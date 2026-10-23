package channels

import (
	"testing"
	"time"
)

func TestTencentCOSGrantTimeKeepsOpaqueURLAndExactLifetime(t *testing.T) {
	for _, tc := range []struct {
		query string
		valid bool
	}{
		{"q-sign-time=1800000000;1800003600&q-key-time=1800000000;1800003600", true},
		{"q-sign-time=1800000000%3B1800003600", true},
		{"q-sign-time=1800000000%3b1800003600", true},
		{"q-sign-time=1800000000;1800003600&q-sign-time=1800000000%3B1800003600", false},
		{"q-sign-time=1800000000;1800003600;1800007200", false},
		{"q-sign-time=1800000000;1799999999", false},
		{"q-sign-time=01800000000;1800003600", false},
		{"q-sign-time=-1;1800003600", false},
		{"q-sign-time=1800000000;9223372036854775808", false},
		{"q-sign-time=1800000000;1800003600&bad=%GG", false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			endpoint := "https://fixture.cos.ap-shanghai.myqcloud.com/object.apk?" + tc.query
			o, err := decode(raw(map[string]any{"ret": 0, "serial_number": "synthetic-file", "pre_sign_url": endpoint}))
			if err != nil {
				t.Fatal(err)
			}
			g, err := parseTencentUploadGrant(o, time.Unix(1800000001, 0))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && (g.URL != endpoint || g.ExpiresAt.Unix() != 1800003600) {
				t.Fatal("opaque signed URL or actual expiry changed")
			}
		})
	}
}
