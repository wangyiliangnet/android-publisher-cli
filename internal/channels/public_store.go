package channels

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

const publicStoreBodyLimit = 1 << 20

var publicPackagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

type publicStoreVersion struct {
	Code int64
	Name string
}

type tencentPublicStore struct{ client *http.Client }

// A separate client prevents a signed API credential, cookie, proxy setting or
// redirected request from reaching the unauthenticated page. The transport seam
// is in-package and exists solely for a local synthetic TLS server.
func newTencentPublicStore(wire http.RoundTripper) *tencentPublicStore {
	if wire == nil {
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		wire = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second, DisableKeepAlives: true, DisableCompression: true, Protocols: protocols, MaxResponseHeaderBytes: 32 << 10}
	}
	return &tencentPublicStore{client: &http.Client{Transport: wire, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func tencentStoreURL(pkg string) string {
	if !publicPackagePattern.MatchString(pkg) || len(pkg) > 255 {
		return ""
	}
	return "https://sj.qq.com/appdetail/" + pkg
}

func (s *tencentPublicStore) version(ctx context.Context, pkg string) (publicStoreVersion, error) {
	endpoint := tencentStoreURL(pkg)
	if endpoint == "" || s == nil || s.client == nil || s.client.Jar != nil {
		return publicStoreVersion{}, fail("invalid_public_store_request", "not_sent")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return publicStoreVersion{}, fail("invalid_public_store_request", "not_sent")
	}
	r.Header.Set("Accept", "text/html")
	r.Header.Set("User-Agent", "AndroidPublisherCLI/1.0 (+https://sj.qq.com)")
	response, err := s.client.Do(r)
	if err != nil {
		return publicStoreVersion{}, fail("public_store_query_failed", "not_sent")
	}
	defer response.Body.Close()
	contentType, _, typeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || response.ContentLength > publicStoreBodyLimit || response.Header.Get("Content-Encoding") != "" || typeErr != nil || contentType != "text/html" {
		return publicStoreVersion{}, fail("public_store_response_refused", "not_sent")
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, publicStoreBodyLimit+1))
	defer clear(b)
	if err != nil || len(b) > publicStoreBodyLimit {
		return publicStoreVersion{}, fail("public_store_response_incomplete", "not_sent")
	}
	return parseTencentStorePage(b, pkg)
}

// Only the canonical detail page's basic-info card is evidence. Recommended
// applications, arbitrary recursively nested records and executable JavaScript
// are deliberately excluded.
func parseTencentStorePage(b []byte, pkg string) (publicStoreVersion, error) {
	if tencentStoreURL(pkg) == "" || len(b) > publicStoreBodyLimit || !utf8.Valid(b) {
		return publicStoreVersion{}, unknown()
	}
	ssr, ok := nextDataJSON(b)
	if !ok || !uniqueJSON(ssr) {
		return publicStoreVersion{}, unknown()
	}
	var document struct {
		Props struct {
			PageProps struct {
				CanonicalURL string `json:"canonicalUrl"`
				Dynamic      struct {
					Data struct {
						Components []struct {
							CardID string `json:"cardId"`
							Data   struct {
								Items []struct {
									Package string          `json:"pkg_name"`
									ID      json.RawMessage `json:"app_id"`
									Name    string          `json:"version_name"`
									Code    json.RawMessage `json:"version_code"`
								} `json:"itemData"`
							} `json:"data"`
						} `json:"components"`
					} `json:"data"`
				} `json:"dynamicCardResponse"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if json.Unmarshal(ssr, &document) != nil || document.Props.PageProps.CanonicalURL != tencentStoreURL(pkg) {
		return publicStoreVersion{}, unknown()
	}
	var result publicStoreVersion
	found := 0
	for _, card := range document.Props.PageProps.Dynamic.Data.Components {
		if card.CardID != "yybn_game_basic_info" {
			continue
		}
		for _, item := range card.Data.Items {
			if item.Package != pkg {
				continue
			}
			found++
			id := object{"id": item.ID}.id("id")
			if id == "" || len(id) > 32 || strings.Trim(id, "0123456789") != "" || !protocol.ValidContextVersion(item.Name) || item.Name == "" {
				return publicStoreVersion{}, unknown()
			}
			result.Name = item.Name
			if len(item.Code) > 0 {
				s := string(item.Code)
				if len(s) > 0 && s[0] == '"' {
					if json.Unmarshal(item.Code, &s) != nil {
						return publicStoreVersion{}, unknown()
					}
				}
				n, err := strconv.ParseInt(s, 10, 64)
				if err != nil || n <= 0 || decimal(n) != s {
					return publicStoreVersion{}, unknown()
				}
				result.Code = n
			}
		}
	}
	if found != 1 {
		return publicStoreVersion{}, unknown()
	}
	return result, nil
}

// This small scanner locates one inert JSON script without evaluating HTML or
// JavaScript. It skips comments, full quoted tags and raw-text elements, so a
// fake script inside an attribute/comment/textarea is never accepted.
func nextDataJSON(b []byte) ([]byte, bool) {
	lower := bytes.Clone(b)
	for i, ch := range lower {
		if ch >= 'A' && ch <= 'Z' {
			lower[i] = ch + ('a' - 'A')
		}
	}
	var result []byte
	for i := 0; i < len(b); {
		rel := bytes.IndexByte(b[i:], '<')
		if rel < 0 {
			break
		}
		i += rel
		if bytes.HasPrefix(lower[i:], []byte("<!--")) {
			end := bytes.Index(lower[i+4:], []byte("-->"))
			if end < 0 {
				return nil, false
			}
			i += end + 7
			continue
		}
		end := i + 1
		var quote byte
		for ; end < len(b); end++ {
			ch := b[end]
			if quote != 0 {
				if ch == quote {
					quote = 0
				}
			} else if ch == '\'' || ch == '"' {
				quote = ch
			} else if ch == '>' {
				break
			}
		}
		if end == len(b) {
			return nil, false
		}
		tag := strings.TrimSpace(string(lower[i+1 : end]))
		nameEnd := strings.IndexAny(tag, " \t\r\n/")
		if nameEnd < 0 {
			nameEnd = len(tag)
		}
		name := tag[:nameEnd]
		if name == "script" || name == "style" || name == "textarea" || name == "title" || name == "template" || name == "iframe" || name == "noscript" || name == "xmp" || name == "noembed" || name == "noframes" {
			closing := bytes.Index(lower[end+1:], []byte("</"+name))
			if closing < 0 {
				return nil, false
			}
			contentEnd := end + 1 + closing
			afterName := contentEnd + 2 + len(name)
			if afterName >= len(b) || !strings.ContainsRune(" \t\r\n>", rune(b[afterName])) {
				return nil, false
			}
			if name == "script" {
				attrs, valid := inertScriptAttributes(string(b[i+1 : end]))
				if !valid {
					return nil, false
				}
				if attrs["id"] == "__NEXT_DATA__" {
					if result != nil || attrs["type"] != "application/json" || attrs["src"] != "" {
						return nil, false
					}
					result = b[end+1 : contentEnd]
				}
			}
			i = contentEnd
			continue
		}
		i = end + 1
	}
	return result, result != nil
}

var inertAttribute = regexp.MustCompile(`^\s+([A-Za-z_:][A-Za-z0-9_:.-]*)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+)))?`)

func inertScriptAttributes(tag string) (map[string]string, bool) {
	if len(tag) < len("script") {
		return nil, false
	}
	remaining := tag[len("script"):]
	attrs := map[string]string{}
	for strings.TrimSpace(remaining) != "" {
		m := inertAttribute.FindStringSubmatch(remaining)
		if m == nil {
			return nil, false
		}
		name := strings.ToLower(m[1])
		if _, duplicate := attrs[name]; duplicate {
			return nil, false
		}
		attrs[name] = m[2] + m[3] + m[4]
		remaining = remaining[len(m[0]):]
	}
	return attrs, true
}
