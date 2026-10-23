package simulation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func seedBrowserRow(t *testing.T, s *Server, channel, packageName string, version int64) (string, protocol.Receipt) {
	t.Helper()
	scope := state.Scope(channel, manifest.Target{AccountRef: "simulation-account", AppRef: "simulation-app"}, packageName)
	remote, err := s.inspect(scope)
	if err != nil {
		t.Fatal(err)
	}
	q := protocol.Request{OperationID: protocol.Digest([]any{channel, version}), IntentDigest: protocol.Digest("fixture"), ArtifactSHA256: protocol.Digest("artifact"), TargetID: "fixture-target", Scope: scope, Channel: channel, Kind: "push", VersionCode: version, PackageName: packageName, Metadata: remote.Metadata, SnapshotDigest: protocol.Digest(remote.Metadata)}
	r, _, err := s.execute(q)
	if err != nil || r.Effect != "confirmed_applied" {
		t.Fatalf("seed: %+v %v", r, err)
	}
	return scope, r
}

func TestBrowserFixtureRendersIndependentAppState(t *testing.T) {
	s, err := New(t.TempDir(), Options{Review: "approved", Availability: "scheduled"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, channel := range manifest.Channels {
		scope, receipt := seedBrowserRow(t, s, channel, "com.example.synthetic", 19)
		before, err := s.stats()
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "/browser/"+channel+"?scope="+scope, nil)
		response := httptest.NewRecorder()
		s.browser(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, receipt.RemoteRef) || !strings.Contains(body, `data-field="version-code">19</span>`) || !strings.Contains(body, "审核通过") || !strings.Contains(body, "定时发布") {
			t.Fatalf("%s invalid page: %d %s", channel, response.Code, body)
		}
		for key, expected := range map[string]string{"X-Publisher-Simulation": "1", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"} {
			if response.Header().Get(key) != expected {
				t.Fatalf("missing %s", key)
			}
		}
		if !strings.Contains(response.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatal("fixture must not permit active page content")
		}
		after, _ := s.stats()
		if before.Total != after.Total || before.Applied != after.Applied {
			t.Fatal("browser observation caused a business write")
		}
	}
}

func TestBrowserFixtureRefusesUnprovenOrChangedIdentity(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope, _ := seedBrowserRow(t, s, "honor", "com.example.synthetic", 9)
	for _, path := range []string{
		"/browser/honor?scope=" + protocol.Digest("missing"),
		"/browser/oppo?scope=" + scope,
		"/browser/honor?scope=" + scope + "&version=9",
		"/browser/honor?scope=" + scope + "&scope=" + scope,
		"/browser/no-such-channel?scope=" + scope,
		"/browser/honor?scope=not-a-hash",
	} {
		response := httptest.NewRecorder()
		s.browser(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("accepted unproven path %s", path)
		}
	}
	var raw string
	if err = s.db.QueryRow("SELECT request FROM calls WHERE scope=?", scope).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var request protocol.Request
	if err = json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatal(err)
	}
	request.PackageName = "com.example.other"
	data, _ := json.Marshal(request)
	if _, err = s.db.Exec("UPDATE calls SET request=? WHERE scope=?", string(data), scope); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.browser(response, httptest.NewRequest(http.MethodGet, "/browser/honor?scope="+scope, nil))
	if response.Code != http.StatusNotFound {
		t.Fatal("stored package mismatch accepted")
	}
}

func TestBrowserFixtureUnknownEnumsAndEscaping(t *testing.T) {
	s, err := New(t.TempDir(), Options{Review: "new-unreviewed-state", Availability: "new-unreviewed-state"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	packageName := `com.example.<script>alert("fixture")</script>`
	scope, _ := seedBrowserRow(t, s, "xiaomi", packageName, 1)
	response := httptest.NewRecorder()
	s.browser(response, httptest.NewRequest(http.MethodGet, "/browser/xiaomi?scope="+url.QueryEscape(scope), nil))
	body := response.Body.String()
	if response.Code != 200 || strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "fixture-unknown-review") || !strings.Contains(body, "fixture-unknown-availability") {
		t.Fatalf("unsafe or success-defaulting rendering: %s", body)
	}
}

func TestBrowserRouteIsReadOnlyAndNoCache(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope, _ := seedBrowserRow(t, s, "vivo", "com.example.fixture", 2)
	server := httptest.NewServer(s)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/browser/vivo?scope="+scope, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("route not hooked: %d", response.StatusCode)
	}
	request, _ = http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/browser/vivo?scope="+scope, strings.NewReader("{}"))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("browser fixture accepted a write")
	}
}
