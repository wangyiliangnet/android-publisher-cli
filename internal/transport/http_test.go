package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

var httpScope = account.Scope{Channel: "oppo", Account: "synthetic-http-account"}

func testClient(t *testing.T, server *httptest.Server, change func(*Policy)) (*Client, account.Coordinator) {
	t.Helper()
	p := Policy{ID: "fixture", Origin: server.URL, Scope: httpScope, Methods: []string{"GET", "POST", "PUT"}, Headers: []string{"Content-Type", "Authorization"}, ReadAttempts: 3, Timeout: 3 * time.Second}
	if change != nil {
		change(&p)
	}
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	if config.InsecureSkipVerify {
		t.Fatal("TLS fixture must verify certificate")
	}
	c, err := newClient(p, config)
	if err != nil {
		t.Fatal(err)
	}
	return c, account.Coordinator{Root: t.TempDir()}
}
func do(t *testing.T, c *Client, accounts account.Coordinator, q Request) (Result, error) {
	t.Helper()
	var result Result
	err := accounts.With(context.Background(), httpScope, func(l *account.Lease) error {
		var err error
		result, err = c.Do(context.Background(), l, q)
		return err
	})
	return result, err
}
func TestFormEncodingAndHTTPIsNotBusinessSuccess(t *testing.T) {
	want := url.Values{"notes": {"中文 space & +\nnext line"}, "empty": {""}, "id": {"900719925474099312345"}}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.ContentLength <= 0 {
			t.Error("missing Content-Length")
		}
		if err := r.ParseForm(); err != nil || !reflect.DeepEqual(r.PostForm, want) {
			t.Error("protocol form bytes changed")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":"0","subCode":"synthetic_denied","message":"fixture-private"}`)
	}))
	defer server.Close()
	c, accounts := testClient(t, server, nil)
	body, size := Form(want)
	result, err := do(t, c, accounts, Request{URL: server.URL + "/form", Method: "POST", Class: Lifecycle, Headers: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, Body: body, ContentLength: size})
	if err != nil || calls.Load() != 1 {
		t.Fatal("form operation failed:", err)
	}
	var envelope map[string]string
	if json.Unmarshal(result.Bytes(), &envelope) != nil || envelope["subCode"] != "synthetic_denied" {
		t.Fatal("transport lost business failure evidence")
	}
	// Transport only returns bytes; adapters must reject the lower-level failure.
	if strings.Contains(fmt.Sprintf("%#v", result), "fixture-private") {
		t.Fatal("raw response in diagnostic")
	}
}
func TestSafeReadRetryAndNoBusinessOrLifecycleRetry(t *testing.T) {
	for _, class := range []Class{Read, Query, Write, Lifecycle} {
		t.Run(string(class), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) < 3 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(503)
					io.WriteString(w, "fixture-private")
					return
				}
				io.WriteString(w, "done")
			}))
			defer server.Close()
			c, accounts := testClient(t, server, nil)
			method := "POST"
			if class == Read {
				method = "GET"
			}
			result, err := do(t, c, accounts, Request{URL: server.URL, Method: method, Class: class, OperationID: "operation-1"})
			if class == Read {
				if err != nil || calls.Load() != 3 || string(result.Bytes()) != "done" {
					t.Fatal("safe read did not retry within bound:", err)
				}
			} else {
				var safe *Error
				if !errors.As(err, &safe) || calls.Load() != 1 || result.Attempts != 1 {
					t.Fatal("non-read operation retried:", err, calls.Load())
				}
				if (class == Write || class == Lifecycle) && safe.Effect != "unknown" {
					t.Fatal("write failure incorrectly proved no effect")
				}
			}
		})
	}
}
func TestRedirectHostMethodCredentialsAndQuotaBoundaries(t *testing.T) {
	var calls, targetCalls atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetCalls.Add(1) }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", target.URL+"/?signature=fixture-private")
		w.WriteHeader(307)
	}))
	defer server.Close()
	c, accounts := testClient(t, server, nil)
	_, err := do(t, c, accounts, Request{URL: server.URL + "/?token=fixture-private", Method: "POST", Class: Write, OperationID: "op", Headers: http.Header{"Authorization": {"Bearer fixture-private"}}})
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != "redirect_refused" || targetCalls.Load() != 0 || calls.Load() != 1 {
		t.Fatal("redirect boundary failed:", err)
	}
	for _, q := range []Request{{URL: target.URL, Method: "GET", Class: Read}, {URL: server.URL, Method: "DELETE", Class: Write, OperationID: "op"}, {URL: server.URL, Method: "GET", Class: Read, Headers: http.Header{"Cookie": {"fixture-private"}}}, {URL: server.URL, Method: "GET", Class: Read, Headers: http.Header{"Idempotency-Key": {"op"}}}} {
		_, err := do(t, c, accounts, q)
		if !errors.As(err, &safe) || safe.Effect != "not_sent" {
			t.Fatal("invalid request sent:", err)
		}
	}
	if calls.Load() != 1 || targetCalls.Load() != 0 {
		t.Fatal("invalid requests reached endpoint")
	}
	if _, err := New(Policy{ID: "bad", Origin: "http://127.0.0.1", Scope: httpScope, Methods: []string{"GET"}}); err == nil {
		t.Fatal("plain HTTP allowed")
	}
	// A public client must use normal system trust, including for local TLS.
	public, err := New(c.policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := do(t, public, accounts, Request{URL: server.URL, Method: "GET", Class: Read}); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
}
func TestStreamingUploadCancelAndNoReplay(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	var length atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		length.Store(r.ContentLength)
		var first [16]byte
		io.ReadFull(r.Body, first[:])
		close(started)
		io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	c, accounts := testClient(t, server, func(p *Policy) { p.Upload = true; p.Methods = []string{"PUT"}; p.Headers = []string{"Content-Type"} })
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		writer.Write(bytes.Repeat([]byte("a"), 64<<10))
		<-ctx.Done()
		writer.CloseWithError(ctx.Err())
	}()
	grant := &Grant{URL: server.URL + "/upload?signature=fixture-private", Method: "PUT", ExpiresAt: time.Now().Add(time.Minute)}
	done := make(chan error, 1)
	go func() {
		done <- accounts.With(ctx, httpScope, func(l *account.Lease) error {
			_, err := c.Do(ctx, l, Request{URL: grant.URL, Method: "PUT", Class: Write, OperationID: "upload-1", Body: reader, ContentLength: 128 << 20, Grant: grant})
			return err
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload never streamed")
	}
	cancel()
	select {
	case err := <-done:
		var safe *Error
		if !errors.As(err, &safe) || safe.Effect != "unknown" || calls.Load() != 1 || length.Load() != 128<<20 {
			t.Fatal("canceled upload replayed or lost uncertainty:", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled upload did not terminate")
	}
	// API credentials cannot be attached to a presigned upload request.
	q := Request{URL: grant.URL, Method: "PUT", Class: Write, OperationID: "upload-2", Headers: http.Header{"Authorization": {"Bearer fixture-private"}}, Body: io.NopCloser(strings.NewReader("x")), ContentLength: 1, Grant: grant}
	if _, err := do(t, c, accounts, q); err == nil {
		t.Fatal("API credential attached to upload")
	}
	if calls.Load() != 1 {
		t.Fatal("rejected upload was sent")
	}
}
func TestResponseLimitsRetryAfterAndDiagnosticRedaction(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/large" {
			io.WriteString(w, strings.Repeat("x", 128))
			return
		}
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer server.Close()
	c, accounts := testClient(t, server, func(p *Policy) { p.MaxResponse = 64; p.Timeout = 80 * time.Millisecond })
	_, err := do(t, c, accounts, Request{URL: server.URL + "/large", Method: "POST", Class: Write, OperationID: "op"})
	var safe *Error
	if !errors.As(err, &safe) || safe.Code != "response_incomplete_or_too_large" || safe.Effect != "unknown" {
		t.Fatal(err)
	}
	_, err = do(t, c, accounts, Request{URL: server.URL + "/quota", Method: "GET", Class: Read})
	if err == nil || calls.Load() != 2 {
		t.Fatal("Retry-After wait was bypassed")
	}
	// A fresh client/Coordinator must honor the persisted account cooldown too.
	_, err = do(t, c, account.Coordinator{Root: accounts.Root}, Request{URL: server.URL, Method: "GET", Class: Read})
	if err == nil || calls.Load() != 2 {
		t.Fatal("account cooldown lost across clients")
	}
	q := Request{URL: server.URL + "/?signature=fixture-private", Headers: http.Header{"Authorization": {"fixture-private"}}, Grant: &Grant{URL: "fixture-private"}}
	for _, value := range []any{q, q.Grant, Result{Status: 200, body: []byte("fixture-private")}, safe} {
		data, _ := json.Marshal(value)
		for _, text := range []string{fmt.Sprint(value), fmt.Sprintf("%#v", value), string(data)} {
			if strings.Contains(text, "fixture-private") {
				t.Fatal("secret or signed URL in diagnostics")
			}
		}
	}
	path := filepath.Join(t.TempDir(), "upload")
	os.WriteFile(path, []byte("abc"), 0600)
	f, size, err := OpenUpload(path)
	if err != nil || size != 3 {
		t.Fatal("upload size:", err)
	}
	f.Close()
}

func TestLongRetryAfterPersistsWithoutShortening(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, header string
		want         time.Time
	}{
		{"delta_seconds", "86400", base.Add(24 * time.Hour)},
		{"http_date", base.Add(24 * time.Hour).Format(http.TimeFormat), base.Add(24 * time.Hour)},
		{"huge_seconds", "999999999999999999999999999999999999", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(429)
			}))
			defer server.Close()
			c, accounts := testClient(t, server, func(p *Policy) { p.Timeout = 40 * time.Millisecond })
			if _, err := do(t, c, accounts, Request{URL: server.URL, Method: "POST", Class: Write, OperationID: "quota-op"}); err == nil {
				t.Fatal("HTTP 429 claimed success")
			}
			other := account.Coordinator{Root: accounts.Root}
			if err := other.With(context.Background(), httpScope, func(l *account.Lease) error {
				data, err := l.Read("request-start")
				if err != nil {
					return err
				}
				var at time.Time
				if json.Unmarshal(data, &at) != nil {
					return errors.New("invalid persisted cooldown")
				}
				if at.Before(tc.want) {
					t.Fatalf("cooldown shortened to %s; server required at least %s", at, tc.want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := do(t, c, other, Request{URL: server.URL, Method: "GET", Class: Read}); err == nil || calls.Load() != 1 {
				t.Fatal("reopened coordinator bypassed long cooldown")
			}
		})
	}
}

// This adapter exists only in tests. It exercises the existing journal against
// the new HTTPS layer without implementing or contacting any vendor protocol.
type controlledFakeAdapter struct {
	client   *Client
	accounts account.Coordinator
	base     string
}

func (a *controlledFakeAdapter) call(ctx context.Context, path string, body any, class Class, operation string, result any) error {
	data, _ := json.Marshal(body)
	return a.accounts.With(ctx, httpScope, func(l *account.Lease) error {
		response, err := a.client.Do(ctx, l, Request{URL: a.base + path, Method: "POST", Class: class, OperationID: operation, Headers: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))})
		if err != nil {
			return err
		}
		return json.Unmarshal(response.Bytes(), result)
	})
}
func (a *controlledFakeAdapter) Inspect(ctx context.Context, scope string) (r protocol.RemoteSnapshot, err error) {
	err = a.call(ctx, "/inspect", map[string]any{"scope": scope}, Query, "", &r)
	return
}
func (a *controlledFakeAdapter) ExecuteOne(ctx context.Context, q protocol.Request) (r protocol.Receipt, err error) {
	err = a.call(ctx, "/execute", q, Write, q.OperationID, &r)
	return
}
func (a *controlledFakeAdapter) Reconcile(ctx context.Context, q protocol.Request) (r protocol.Reconciliation, err error) {
	err = a.call(ctx, "/reconcile", map[string]any{"request": q}, Query, "", &r)
	return
}
func (a *controlledFakeAdapter) Observe(ctx context.Context, scope string, version int64, ref string) (r protocol.Observation, err error) {
	err = a.call(ctx, "/observe", map[string]any{"scope": scope, "version_code": version, "remote_ref": ref}, Query, "", &r)
	return
}
func TestCanceledAppliedUploadReconcilesBeforeEngineResume(t *testing.T) {
	base := t.TempDir()
	fake, err := simulation.New(filepath.Join(base, "remote"), simulation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	var cancel context.CancelFunc
	var canceled atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/execute" && !canceled.Swap(true) {
			// Commit the remote upload, then lose its response by canceling the client.
			recorded := httptest.NewRecorder()
			fake.ServeHTTP(recorded, r)
			cancel()
			<-r.Context().Done()
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, accounts := testClient(t, server, nil)
	adapter := &controlledFakeAdapter{c, accounts, server.URL}
	path, err := simulation.Init(filepath.Join(base, "input"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: adapter}
	plan, err := e.Plan(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Preflight(context.Background(), plan.ReleaseID, []string{"oppo"}); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	cancel = stop
	defer stop()
	if _, err = e.Submit(ctx, plan.ReleaseID, []string{"oppo"}); err == nil {
		t.Fatal("canceled response claimed success")
	}
	s.Close()
	s, err = state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Store = s
	if _, err = e.Submit(context.Background(), plan.ReleaseID, []string{"oppo"}); err != nil {
		t.Fatal("resume:", err)
	}
	var stats simulation.Stats
	if err = adapter.call(context.Background(), "/stats", map[string]any{}, Query, "", &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Calls["oppo"]["upload"] != 1 || stats.Total != 3 {
		t.Fatal("canceled upload was repeated:", stats)
	}
}
