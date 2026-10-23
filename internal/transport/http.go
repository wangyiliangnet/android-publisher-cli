// Package transport performs bounded HTTP operations. It does not interpret
// vendor success codes or authorize/retry publishing writes; adapters/engine do.
package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

type Class string

const (
	Read      Class = "safe_read"
	Query     Class = "read_once" // e.g. a protocol-defined POST query, never retried
	Write     Class = "business_write"
	Lifecycle Class = "token_lifecycle"
)

type Policy struct {
	ID                    string
	Origin                string
	Scope                 account.Scope
	Upload                bool
	Methods               []string
	Headers               []string // protocol-defined allowlist, including any authentication header
	Timeout               time.Duration
	ResponseHeaderTimeout time.Duration
	MinInterval           time.Duration
	ReadAttempts          int
	MaxResponse           int64
}
type Client struct {
	policy Policy
	origin *url.URL
	client *http.Client
}
type Grant struct {
	URL       string
	Method    string
	ExpiresAt time.Time
	// Headers binds object-storage credentials to exactly one upload grant.
	// API Bearer credentials remain forbidden on upload clients.
	Headers http.Header
}

func (g Grant) String() string               { return "[REDACTED upload grant]" }
func (g Grant) GoString() string             { return g.String() }
func (g Grant) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED upload grant]"`), nil }

type Request struct {
	URL           string
	Method        string
	Class         Class
	OperationID   string
	Headers       http.Header
	Body          io.ReadCloser
	ContentLength int64
	Grant         *Grant
}

func (q Request) String() string               { return "[REDACTED HTTP request]" }
func (q Request) GoString() string             { return q.String() }
func (q Request) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED HTTP request]"`), nil }

type Result struct {
	Status   int `json:"status"`
	Attempts int `json:"attempts"`
	body     []byte
}

func (r Result) Bytes() []byte { return append([]byte(nil), r.body...) }
func (r Result) String() string {
	return fmt.Sprintf("HTTP status=%d attempts=%d body=[REDACTED]", r.Status, r.Attempts)
}
func (r Result) GoString() string { return r.String() }
func (r Result) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Status   int `json:"status"`
		Attempts int `json:"attempts"`
	}{r.Status, r.Attempts})
}

type Error struct {
	Code     string `json:"code"`
	Effect   string `json:"effect"`
	Status   int    `json:"status,omitempty"`
	Attempts int    `json:"attempts"`
}

func (e *Error) Error() string       { return "controlled HTTP: " + e.Code + " (" + e.Effect + ")" }
func (e *Error) WriteEffect() string { return e.Effect }

func New(policy Policy) (*Client, error) { return newClient(policy, nil) }
func newClient(p Policy, tlsConfig *tls.Config) (*Client, error) {
	p.Methods = append([]string(nil), p.Methods...)
	p.Headers = append([]string(nil), p.Headers...)
	for i, h := range p.Headers {
		p.Headers[i] = http.CanonicalHeaderKey(h)
	}
	u, err := url.Parse(p.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !p.Scope.Valid() || !account.ValidName(p.ID) || len(p.Methods) == 0 {
		return nil, &Error{Code: "invalid_policy", Effect: "not_sent"}
	}
	if p.Timeout == 0 {
		p.Timeout = 45 * time.Second
	}
	if p.ResponseHeaderTimeout == 0 {
		p.ResponseHeaderTimeout = min(15*time.Second, p.Timeout)
	}
	if p.MaxResponse == 0 {
		p.MaxResponse = 1 << 20
	}
	if p.ReadAttempts == 0 {
		p.ReadAttempts = 1
	}
	if p.Timeout <= 0 || p.Timeout > 90*time.Second || p.ResponseHeaderTimeout <= 0 || p.ResponseHeaderTimeout > p.Timeout || p.ReadAttempts < 1 || p.ReadAttempts > 3 || p.MaxResponse < 1 || p.MaxResponse > 4<<20 || p.MinInterval < 0 || p.MinInterval > time.Hour {
		return nil, &Error{Code: "invalid_policy", Effect: "not_sent"}
	}
	for _, method := range p.Methods {
		if !validMethod(method) {
			return nil, &Error{Code: "invalid_policy", Effect: "not_sent"}
		}
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	// Fresh HTTP/1 connections and non-replayable bodies avoid net/http's
	// transparent retries on reused connections or HTTP/2 GOAWAY.
	t := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: p.ResponseHeaderTimeout,
		DisableKeepAlives: true, DisableCompression: true, Protocols: protocols, MaxResponseHeaderBytes: 64 << 10}
	return &Client{p, u, &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func validMethod(method string) bool {
	for _, m := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		if method == m {
			return true
		}
	}
	return false
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func (c *Client) validate(l *account.Lease, q Request) error {
	if !l.Valid(c.policy.Scope) || !contains(c.policy.Methods, q.Method) || (q.Class != Read && q.Class != Query && q.Class != Write && q.Class != Lifecycle) {
		return &Error{Code: "invalid_request", Effect: "not_sent"}
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != c.origin.Scheme || u.Host != c.origin.Host || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return &Error{Code: "endpoint_not_allowed", Effect: "not_sent"}
	}
	if q.Class == Read && (q.Method != "GET" && q.Method != "HEAD" || q.Body != nil) {
		return &Error{Code: "read_is_not_replayable", Effect: "not_sent"}
	}
	if q.Class == Write && !account.ValidName(q.OperationID) {
		return &Error{Code: "missing_operation_id", Effect: "not_sent"}
	}
	if q.ContentLength < 0 || q.Body == nil && q.ContentLength != 0 || q.Body != nil && q.ContentLength == 0 {
		return &Error{Code: "invalid_content_length", Effect: "not_sent"}
	}
	if c.policy.Upload {
		if q.Class != Write || q.Grant == nil || q.URL != q.Grant.URL || q.Method != q.Grant.Method || time.Until(q.Grant.ExpiresAt) <= 0 || q.Body == nil {
			return &Error{Code: "upload_grant_invalid", Effect: "not_sent"}
		}
		grantNames := map[string]bool{}
		for key, values := range q.Grant.Headers {
			name := http.CanonicalHeaderKey(key)
			if grantNames[name] || len(values) != 1 {
				return &Error{Code: "upload_grant_invalid", Effect: "not_sent"}
			}
			grantNames[name] = true
			if name == "Host" {
				if values[0] != u.Host {
					return &Error{Code: "upload_grant_invalid", Effect: "not_sent"}
				}
				continue
			}
			if len(q.Headers.Values(name)) != 1 || q.Headers.Get(name) != values[0] {
				return &Error{Code: "upload_grant_header_mismatch", Effect: "not_sent"}
			}
		}
	} else if q.Grant != nil {
		return &Error{Code: "upload_grant_on_api_request", Effect: "not_sent"}
	}
	names := map[string]bool{}
	for key, values := range q.Headers {
		name := http.CanonicalHeaderKey(key)
		if names[name] || len(values) != 1 {
			return &Error{Code: "duplicate_or_empty_header", Effect: "not_sent"}
		}
		names[name] = true
		storageAuth := c.policy.Upload && name == "Authorization" && len(values) == 1 && q.Grant.Headers.Get(name) == values[0] && (strings.HasPrefix(values[0], "AWS4-HMAC-SHA256 ") || strings.HasPrefix(values[0], "OBS "))
		if name == "Cookie" || name == "Proxy-Authorization" || name == "Host" || name == "Referer" || name == "Idempotency-Key" || name == "X-Idempotency-Key" || name == "Content-Length" || name == "Transfer-Encoding" || c.policy.Upload && name == "Authorization" && !storageAuth || !contains(c.policy.Headers, name) {
			return &Error{Code: "header_not_allowed", Effect: "not_sent"}
		}
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				return &Error{Code: "invalid_header", Effect: "not_sent"}
			}
		}
	}
	return nil
}

// Do consumes and closes Body exactly once. On any ambiguous write outcome the
// effect is unknown. Calling it again requires engine reconciliation, never a
// transport retry or an automatic token refresh after HTTP 401.
func (c *Client) Do(ctx context.Context, l *account.Lease, q Request) (Result, error) {
	var result Result
	if q.Body != nil {
		defer q.Body.Close()
	}
	if err := c.validate(l, q); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.policy.Timeout)
	defer cancel()
	stop := context.AfterFunc(l.Context(), cancel)
	defer stop()
	attempts := 1
	if q.Class == Read {
		attempts = c.policy.ReadAttempts
	}
	effect := "not_sent"
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := l.Wait(ctx, c.policy.MinInterval); err != nil {
			return result, &Error{Code: "account_wait_canceled_or_unavailable", Effect: effect, Attempts: result.Attempts}
		}
		if c.policy.Upload && time.Until(q.Grant.ExpiresAt) <= 0 {
			return result, &Error{Code: "upload_grant_expired", Effect: effect, Attempts: result.Attempts}
		}
		req, err := http.NewRequestWithContext(ctx, q.Method, q.URL, q.Body)
		if err != nil {
			return result, &Error{Code: "invalid_request", Effect: effect, Attempts: result.Attempts}
		}
		req.ContentLength = q.ContentLength
		req.GetBody = nil
		req.Header = q.Headers.Clone()
		result.Attempts = attempt
		if q.Class == Write || q.Class == Lifecycle {
			effect = "unknown"
		}
		response, err := c.client.Do(req)
		if err != nil {
			if attempt < attempts && ctx.Err() == nil {
				continue
			}
			return result, &Error{Code: "request_failed_or_canceled", Effect: effect, Attempts: attempt}
		}
		result.Status = response.StatusCode
		if retryable(result.Status) {
			if err := l.DeferUntil(retryUntil(time.Now(), response.Header.Get("Retry-After"))); err != nil {
				response.Body.Close()
				return result, &Error{Code: "account_quota_persistence_failed", Effect: effect, Status: result.Status, Attempts: attempt}
			}
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, c.policy.MaxResponse+1))
		response.Body.Close()
		if readErr != nil || int64(len(data)) > c.policy.MaxResponse {
			if attempt < attempts && ctx.Err() == nil {
				continue
			}
			return result, &Error{Code: "response_incomplete_or_too_large", Effect: effect, Status: result.Status, Attempts: attempt}
		}
		result.body = data
		if result.Status >= 300 && result.Status < 400 {
			return result, &Error{Code: "redirect_refused", Effect: effect, Status: result.Status, Attempts: attempt}
		}
		if result.Status < 200 || result.Status >= 300 {
			if attempt < attempts && retryable(result.Status) {
				continue
			}
			return result, &Error{Code: "http_status", Effect: effect, Status: result.Status, Attempts: attempt}
		}
		return result, nil
	}
	panic("unreachable")
}
func retryable(code int) bool { return code == 429 || code == 502 || code == 503 || code == 504 }
func retryUntil(now time.Time, value string) time.Time {
	value = strings.TrimSpace(value)
	digits := value != ""
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		// JSON timestamps end at year 9999. Saturating beyond this point keeps
		// an unrepresentable server delay blocked instead of shortening it.
		maximum := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(maximum.Unix()-now.Unix()) {
			return maximum
		}
		return time.Unix(now.Unix()+int64(seconds), int64(now.Nanosecond())).UTC()
	}
	if at, err := http.ParseTime(value); err == nil {
		if at.Before(now) {
			return now
		}
		return at
	}
	return now.Add(100 * time.Millisecond)
}

// OpenUpload opens a regular file without following symlinks and supplies the
// exact size. The engine still verifies the frozen artifact before invoking it.
func OpenUpload(path string) (*os.File, int64, error) {
	f, err := privatefs.Open(path, os.O_RDONLY)
	if err != nil {
		return nil, 0, &Error{Code: "upload_file_unavailable", Effect: "not_sent"}
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, &Error{Code: "upload_file_unavailable", Effect: "not_sent"}
	}
	return f, info.Size(), nil
}

// Form preserves protocol-significant empty values, newlines, Unicode and +/&.
func Form(values url.Values) (io.ReadCloser, int64) {
	encoded := values.Encode()
	return io.NopCloser(strings.NewReader(encoded)), int64(len(encoded))
}
