package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
)

func TestStorageSignedHeadersBoundToExactGrant(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "AWS4-HMAC-SHA256 synthetic-obs" || r.Header.Get("X-Amz-Content-Sha256") != "synthetic-hash" || string(b) != "APK" {
			t.Error("storage grant headers or binary body changed")
		}
	}))
	defer server.Close()
	c, accounts := testClient(t, server, func(p *Policy) {
		p.Upload = true
		p.Headers = []string{"Authorization", "Content-Type", "X-Amz-Content-Sha256", "X-Amz-Date", "User-Agent"}
	})
	makeRequest := func() Request {
		h := http.Header{"Authorization": {"AWS4-HMAC-SHA256 synthetic-obs"}, "Content-Type": {"application/octet-stream"}, "X-Amz-Content-Sha256": {"synthetic-hash"}, "X-Amz-Date": {"20261020T010000Z"}, "User-Agent": {"synthetic-agent"}}
		g := &Grant{URL: server.URL + "/object", Method: "PUT", ExpiresAt: time.Now().Add(time.Minute), Headers: h.Clone()}
		return Request{URL: g.URL, Method: "PUT", Class: Write, OperationID: "upload", Headers: h, Body: io.NopCloser(bytes.NewBufferString("APK")), ContentLength: 3, Grant: g}
	}
	if _, err := do(t, c, accounts, makeRequest()); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){
		func(q *Request) { q.Headers.Set("Authorization", "Bearer synthetic-api") },
		func(q *Request) {
			q.Headers.Set("Authorization", "Bearer synthetic-api")
			q.Grant.Headers.Set("Authorization", "Bearer synthetic-api")
		},
		func(q *Request) { q.Headers.Set("X-Amz-Date", "20261020T020000Z") },
		func(q *Request) { q.Grant.Headers.Set("Host", "unrelated.invalid") },
		func(q *Request) { q.Headers.Set("Cookie", "secret") },
		func(q *Request) { q.Grant.Headers = nil },
		func(q *Request) { q.Headers["X-Amz-Date"] = append(q.Headers["X-Amz-Date"], "injected-second-value") },
		func(q *Request) { q.Headers["x-amz-date"] = []string{"duplicate-case-variant"} },
		func(q *Request) { q.Grant.Headers["x-amz-date"] = []string{"duplicate-case-variant"} },
	} {
		q := makeRequest()
		change(&q)
		if _, err := do(t, c, accounts, q); err == nil {
			t.Fatal("changed grant sent")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid grant reached server")
	}
	// A live account lease is still mandatory even with valid storage credentials.
	if _, err := c.Do(context.Background(), &account.Lease{}, makeRequest()); err == nil {
		t.Fatal("upload escaped account coordination")
	}
}
