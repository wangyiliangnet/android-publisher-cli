package protocol

import (
	"strings"
	"testing"
)

func TestContextReasonIsSafeBoundedAndIdempotent(t *testing.T) {
	reason := "请修改隐私说明\x1b[31m\nclient_secret=synthetic-secret token:synthetic-token https://example.invalid/path?q=synthetic-private-query Bearer synthetic-bearer 0123456789abcdef0123456789abcdef \"password\":\"synthetic phrase secret\""
	safe := SanitizeContextReason(reason)
	for _, forbidden := range []string{"synthetic-secret", "synthetic-token", "synthetic-private-query", "synthetic-bearer", "synthetic phrase secret", "0123456789abcdef", "\x1b", "https://"} {
		if strings.Contains(safe, forbidden) {
			t.Fatal("sensitive vendor reason leaked")
		}
	}
	if !strings.Contains(safe, "请修改隐私说明") || safe != SanitizeContextReason(safe) {
		t.Fatal("sanitization lost useful reason or is not canonical")
	}
	long := SanitizeContextReason(strings.Repeat("字", 1000))
	if len([]rune(long)) > 321 || long != SanitizeContextReason(long) {
		t.Fatal("unbounded or unstable explanation")
	}
}

func TestChannelContextIndependentQueryHealth(t *testing.T) {
	for _, tc := range []struct{ api, store, status string }{
		{"healthy", "healthy", "observed"}, {"healthy", "not_supported", "observed"},
		{"healthy", "failed", "partial"}, {"failed", "healthy", "partial"},
		{"failed", "failed", "query_failed"}, {"failed", "not_supported", "query_failed"},
	} {
		c := &ChannelContext{APIQueryHealth: tc.api, StoreQueryHealth: tc.store}
		if c.QueryStatus() != tc.status {
			t.Fatal("partial source failure hidden")
		}
	}
}

func TestChannelContextValidationDoesNotAcceptRawProjection(t *testing.T) {
	base := ChannelContext{AuditCode: "2", AuditStatus: "rejected", RejectionReason: "请补充隐私说明", APIQueryHealth: "healthy", StoreQueryHealth: "failed", Association: "unconfirmed", SourceURL: "https://sj.qq.com/appdetail/com.example.fixture"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	mutations := []func(*ChannelContext){
		func(c *ChannelContext) { c.Association = "target_verified" },
		func(c *ChannelContext) { c.RejectionReason = "token=synthetic-private-token" },
		func(c *ChannelContext) { c.SourceURL += "?token=synthetic" },
		func(c *ChannelContext) { c.SourceURL = "https://sj.qq.com.evil.invalid/appdetail/com.example.fixture" },
		func(c *ChannelContext) { c.APIQueryHealth = "failed" },
		func(c *ChannelContext) { c.StoreVersionName = "2.0" },
		func(c *ChannelContext) { c.APIVersionName = "\x1b[31m" },
		func(c *ChannelContext) { c.AuditStatus = "made_up" },
	}
	for i, mutate := range mutations {
		value := base
		mutate(&value)
		if err := value.Validate(); err == nil {
			t.Fatalf("unsafe context mutation %d accepted", i)
		}
	}
}
