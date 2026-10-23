package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestTencentVersionAliasesRequireConsistentPositiveInt64(t *testing.T) {
	for _, tc := range []struct {
		fields string
		want   int64
	}{
		{`"VersionCode":247`, 247}, {`"version_code":247`, 247},
		{`"VersionCode":247,"version_code":247`, 247},
		{`"version_code":9007199254740993`, 9007199254740993},
		{`"VersionCode":247,"version_code":248`, 0},
		{`"VersionCode":null,"version_code":247`, 0},
		{`"VersionCode":"247","version_code":247`, 0},
		{`"VersionCode":247,"version_code":null`, 0},
		{`"version_code":247.0`, 0}, {`"version_code":"247"`, 0},
		{`"version_code":0`, 0}, {`"version_code":-1`, 0},
		{`"version_code":9223372036854775808`, 0}, {`"other":247`, 0},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			c := m5Client(t, "tencent")
			pkg := "com.example.synthetic"
			c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
				f := m5Fields(t, q)
				if f["pkg_name"] != "com.example.synthetic" || f["app_id"] != "123" || q.Class != transport.Query {
					t.Fatal("query identity changed")
				}
				return []byte(`{"ret":0,"pkg_name":"` + pkg + `","app_name":"SYNTHETIC-PRIVATE",` + tc.fields + `}`), nil
			})
			d, err := c.TencentDetails(context.Background(), "com.example.synthetic", "123")
			if tc.want > 0 {
				if err != nil || d.VersionCode != tc.want || d.AppID != "123" {
					t.Fatalf("version=%d error=%v", d.VersionCode, err)
				}
				pkg = "com.example.other"
				if _, err = c.TencentDetails(context.Background(), "com.example.synthetic", "123"); err == nil {
					t.Fatal("wrong package accepted")
				}
			} else if err == nil {
				t.Fatal("invalid version evidence accepted")
			}
		})
	}
}

func TestTencentQueryDiagnosticExposesOnlyBoundedRetAndVersionType(t *testing.T) {
	for _, tc := range []struct {
		ret  string
		want string
	}{{`0`, "0"}, {`-10001`, "-10001"}, {`"SYNTHETIC-PRIVATE"`, ""}, {`"https://example.invalid/secret"`, ""}, {`1234567890123`, ""}} {
		d := queryDiagnostic([]byte(`{"ret":` + tc.ret + `,"version_code":247,"pkg_name":"SYNTHETIC-PRIVATE","login_account":"SYNTHETIC-PRIVATE"}`))
		if d.Codes["ret"] != tc.want || d.Shape["version_code"] != "number" {
			t.Fatal("missing query diagnostic")
		}
		b, _ := json.Marshal(d)
		for _, value := range []string{"SYNTHETIC-PRIVATE", "example.invalid", "247", "login_account"} {
			if strings.Contains(string(b), value) {
				t.Fatal("sensitive response data leaked")
			}
		}
	}
}
