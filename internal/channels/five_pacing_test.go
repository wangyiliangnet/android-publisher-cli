package channels

import (
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestVivoProductionWriteReservesAccountIntervalAfterInspection(t *testing.T) {
	for _, class := range []transport.Class{transport.Query, transport.Write} {
		p := controlledPolicy(account.Scope{Channel: "vivo", Account: "fixture"}, origins["vivo"], transport.Request{Class: class})
		if p.MinInterval != time.Minute || p.Timeout != 90*time.Second || (class == transport.Write && p.ResponseHeaderTimeout != 90*time.Second) {
			t.Fatalf("vivo account pacing or single-request budget changed: %+v", p)
		}
	}
}
