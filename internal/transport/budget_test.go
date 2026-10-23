package transport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExplicitResponseHeaderBudgetIsHonoredAndBounded(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(40 * time.Millisecond)
		io.WriteString(w, "accepted")
	}))
	defer server.Close()
	c, accounts := testClient(t, server, func(p *Policy) { p.Timeout = time.Second; p.ResponseHeaderTimeout = 500 * time.Millisecond })
	transport, ok := c.client.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 500*time.Millisecond {
		t.Fatal("explicit vendor header budget discarded")
	}
	if _, err := do(t, c, accounts, Request{URL: server.URL, Method: "POST", Class: Write, OperationID: "one-attempt"}); err != nil {
		t.Fatal("response inside vendor budget rejected", err)
	}
	p := c.policy
	p.ResponseHeaderTimeout = 2 * time.Second
	if _, err := New(p); err == nil {
		t.Fatal("header wait exceeded total bounded request budget")
	}
}
