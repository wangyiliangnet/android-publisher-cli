package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestVivoQueryPacingFitsTransportBudget(t *testing.T) {
	p := controlledPolicy(account.Scope{Channel: "vivo", Account: "synthetic"}, origins["vivo"], transport.Request{Class: transport.Query})
	if p.MinInterval < time.Minute || p.Timeout < p.MinInterval+15*time.Second {
		t.Fatal("request can time out before its reserved query start")
	}
	if _, err := transport.New(p); err != nil {
		t.Fatal(err)
	}
}

func TestVivoAccountVersionAcceptsOnlyPositiveExactInt64(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int64
	}{
		{`247`, 247}, {`"247"`, 247}, {`"9007199254740993"`, 9007199254740993},
		{`"9223372036854775807"`, 9223372036854775807},
		{`"9223372036854775808"`, 0}, {`9223372036854775808`, 0},
		{`247.0`, 0}, {`"247.0"`, 0}, {`"2e2"`, 0}, {`"0247"`, 0},
		{`"+247"`, 0}, {`" 247"`, 0}, {`"247 "`, 0}, {`true`, 0}, {`null`, 0}, {`""`, 0}, {`0`, 0}, {`-1`, 0},
	} {
		t.Run(tc.value, func(t *testing.T) {
			c := m5Client(t, "vivo")
			c.Wire = m5Sender(func(_ context.Context, q transport.Request) ([]byte, error) {
				m5Body(t, q)
				return []byte(`{"code":0,"subCode":"0","data":{"packageName":"com.example.synthetic","versionCode":` + tc.value + `}}`), nil
			})
			d, err := c.VivoDetails(context.Background(), "com.example.synthetic")
			if tc.want > 0 {
				if err != nil || d.VersionCode != tc.want {
					t.Fatalf("version=%d error=%v", d.VersionCode, err)
				}
			} else if err == nil {
				t.Fatal("invalid version accepted")
			}
		})
	}
}

func TestAccountQueryDiagnosticDoesNotExposeResponseValues(t *testing.T) {
	b := []byte(`{"code":0,"subCode":"11001","data":{"versionCode":"247","packageName":"SYNTHETIC-PRIVATE","access_token":"SYNTHETIC-PRIVATE"},"error_description":"SYNTHETIC-PRIVATE","arbitrary":"SYNTHETIC-PRIVATE"}`)
	d := queryDiagnostic(b)
	if d.Shape["data.versionCode"] != "string" || d.Codes["subCode"] != "11001" {
		t.Fatal("missing schema diagnostic")
	}
	encoded, _ := json.Marshal(d)
	for _, value := range []string{"SYNTHETIC-PRIVATE", "247", "arbitrary"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("response value leaked")
		}
	}
}
