package channels

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func syntheticStorePage(pkg, version string, code any) string {
	item := map[string]any{"pkg_name": pkg, "app_id": "9007199254740993", "version_name": version}
	if code != nil {
		item["version_code"] = code
	}
	card := map[string]any{"cardId": "yybn_game_basic_info", "data": map[string]any{"itemData": []any{item}}}
	data := map[string]any{"props": map[string]any{"pageProps": map[string]any{"canonicalUrl": tencentStoreURL(pkg), "dynamicCardResponse": map[string]any{"data": map[string]any{"components": []any{card}}}}}}
	b, _ := json.Marshal(data)
	return `<html><body><script type="application/json" id="__NEXT_DATA__">` + string(b) + `</script></body></html>`
}

func TestTencentStoreParsesOnlyCanonicalBasicCard(t *testing.T) {
	page := syntheticStorePage("test.synthetic", "3.2.7", json.Number("9007199254740993"))
	got, err := parseTencentStorePage([]byte(page), "test.synthetic")
	if err != nil || got.Name != "3.2.7" || got.Code != 9007199254740993 {
		t.Fatal("precise public version not parsed", err)
	}
	page = syntheticStorePage("test.synthetic", "3.2.6", nil)
	if got, err := parseTencentStorePage([]byte(page), "test.synthetic"); err != nil || got.Code != 0 || got.Name != "3.2.6" {
		t.Fatal("public version name must work without numeric code", err)
	}
	if _, err := parseTencentStorePage([]byte("<p>K 中文</p>"+page), "test.synthetic"); err != nil {
		t.Fatal("Unicode text changed script byte offsets", err)
	}
	cases := map[string]string{
		"wrong_package":     strings.ReplaceAll(page, "test.synthetic", "test.other"),
		"wrong_canonical":   strings.Replace(page, "https://sj.qq.com/appdetail/test.synthetic", "https://example.invalid/appdetail/test.synthetic", 1),
		"recommendation":    strings.Replace(page, "yybn_game_basic_info", "yybn_game_related_game", 1),
		"invalid_version":   strings.Replace(page, "3.2.6", `3.2.6\u001b[31m`, 1),
		"duplicate_json":    strings.Replace(page, `"version_name":"3.2.6"`, `"version_name":"3.2.6","version_name":"3.2.7"`, 1),
		"duplicate_script":  page + page,
		"commented_script":  "<!--" + page + "-->",
		"script_attribute":  `<div title='` + page + `'></div>`,
		"textarea_script":   `<textarea>` + page + `</textarea>`,
		"template_script":   `<template>` + page + `</template>`,
		"bad_closing_tag":   strings.Replace(page, `</script>`, `</scripted>`, 1),
		"nonnumeric_app_id": strings.Replace(page, `9007199254740993`, `synthetic-bogus`, 1),
		"duplicate_id_attr": strings.Replace(page, `id="__NEXT_DATA__"`, `id="__NEXT_DATA__" ID="other"`, 1),
		"executable_script": strings.Replace(page, `type="application/json"`, `type="text/javascript"`, 1),
		"external_script":   strings.Replace(page, `<script `, `<script src="https://example.invalid/x.js" `, 1),
		"overflow_code":     syntheticStorePage("test.synthetic", "3.2.7", "9223372036854775808"),
		"oversize":          page + strings.Repeat(" ", publicStoreBodyLimit),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := parseTencentStorePage([]byte(body), "test.synthetic"); err == nil {
				t.Fatal("untrusted page accepted")
			}
		})
	}
}

type storeTestTransport func(*http.Request) (*http.Response, error)

func (f storeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicStoreTLSReadNoCredentialsRedirectOrUnboundedResponse(t *testing.T) {
	for _, mode := range []string{"healthy", "redirect", "oversize", "chunked_oversize", "gzip", "timeout", "unknown_html"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/appdetail/test.synthetic" || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.ContentLength != 0 {
					t.Error("public request included credential, body or wrong route")
				}
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://example.invalid/collect")
					w.WriteHeader(302)
				case "oversize":
					w.Header().Set("Content-Length", "1048577")
				case "chunked_oversize":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					io.WriteString(w, strings.Repeat("x", publicStoreBodyLimit+1))
				case "gzip":
					w.Header().Set("Content-Encoding", "gzip")
				case "timeout":
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				case "unknown_html":
					io.WriteString(w, `<html><h1>please log in</h1></html>`)
				default:
					w.Header().Set("Set-Cookie", "session=synthetic-cookie; Secure")
					io.WriteString(w, syntheticStorePage("test.synthetic", "1.2.3", nil))
				}
			}))
			defer server.Close()
			wire := storeTestTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Scheme != "https" || r.URL.Host != "sj.qq.com" || r.URL.User != nil || r.URL.RawQuery != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Fatal("fixed public origin boundary violated")
				}
				copy := r.Clone(r.Context())
				endpoint, _ := url.Parse(server.URL + r.URL.RequestURI())
				copy.URL = endpoint
				return server.Client().Transport.RoundTrip(copy)
			})
			store := newTencentPublicStore(wire)
			ctx := context.Background()
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
			}
			v, err := store.version(ctx, "test.synthetic")
			if mode == "healthy" {
				if err != nil || v.Name != "1.2.3" {
					t.Fatal("synthetic TLS public query failed", err)
				}
				if _, err := store.version(ctx, "test.synthetic"); err != nil {
					t.Fatal(err)
				}
				if calls != 2 || store.client.Jar != nil {
					t.Fatal("cookie state retained")
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("unsafe public response accepted or retried: %v calls %d", err, calls)
			}
			before := calls
			for _, invalid := range []string{"test.synthetic?token=x", "../test.synthetic", "https://example.invalid", "test.synthetic/other", "test.synthetic#x"} {
				if _, err := store.version(context.Background(), invalid); err == nil || calls != before {
					t.Fatal("invalid package reached network")
				}
			}
		})
	}
}
