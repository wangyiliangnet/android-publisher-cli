package simulation

import (
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

// This page is an explicitly synthetic observation contract. It does not model
// any vendor's real DOM, login flow, selectors or production status vocabulary.
var browserPage = template.Must(template.New("simulation-observation").Parse(`<!doctype html>
<html lang="zh-CN" data-fixture-schema="publisher-observation-v1"><head><meta charset="utf-8"><title>Publisher local observation fixture</title></head><body>
<aside>Local synthetic fixture only. Synthetic token: fixture-secret-must-not-enter-evidence</aside>
<main data-channel="{{.Channel}}" data-account="{{.Account}}" data-app-id="{{.AppID}}" data-package-name="{{.Package}}" data-scope="{{.Scope}}">
<article data-testid="release-row"><span data-field="version-code">{{.Version}}</span><span data-field="reference">{{.Reference}}</span><time data-field="submitted-at" datetime="{{.SubmittedAt}}">{{.SubmittedAt}}</time><span data-field="review">{{.Review}}</span><span data-field="availability">{{.Availability}}</span></article>
</main></body></html>`))

type browserRow struct {
	Channel, Account, AppID, Package, Scope string
	Version                                 int64
	Reference, SubmittedAt                  string
	Review, Availability                    string
}

func (s *Server) browser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Publisher-Simulation", "1")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	channel := strings.TrimPrefix(r.URL.Path, "/browser/")
	scope := r.URL.Query().Get("scope")
	if r.Method != http.MethodGet || !slices.Contains(manifest.Channels, channel) || !manifest.ValidHash(scope) || len(r.URL.Query()) != 1 || len(r.URL.Query()["scope"]) != 1 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var raw string
	var row browserRow
	// The independently persisted app and last confirmed submission determine
	// the visible version and reference. Expected target data never enters this
	// query, so an older or different submission cannot become a matching row.
	err := s.db.QueryRow(`SELECT a.version_code,a.remote_ref,c.request,c.created_at
FROM apps a JOIN calls c ON c.scope=a.scope AND c.ref=a.remote_ref
WHERE a.scope=? AND c.channel=? AND c.kind IN ('submit','push') ORDER BY c.id DESC LIMIT 1`, scope, channel).
		Scan(&row.Version, &row.Reference, &raw, &row.SubmittedAt)
	var request protocol.Request
	if err != nil || json.Unmarshal([]byte(raw), &request) != nil {
		http.NotFound(w, r)
		return
	}
	identity := manifest.Target{AccountRef: "simulation-account", AppRef: "simulation-app"}
	if request.Channel != channel || request.Scope != scope || request.PackageName == "" || request.VersionCode != row.Version || scope != state.Scope(channel, identity, request.PackageName) {
		http.NotFound(w, r)
		return
	}
	if submitted, err := time.Parse(time.RFC3339Nano, row.SubmittedAt); err != nil || submitted.IsZero() {
		http.NotFound(w, r)
		return
	}
	row.Channel, row.Account, row.AppID = channel, identity.AccountRef, identity.AppRef
	row.Package, row.Scope = request.PackageName, scope
	row.Review = browserReview(s.options.Review)
	row.Availability = browserAvailability(s.options.Availability)
	// html/template escapes all stored fields; no stored value becomes markup.
	if err := browserPage.Execute(w, row); err != nil {
		return
	}
}

func browserReview(value string) string {
	switch value {
	case "pending":
		return "待审核"
	case "in_review":
		return "审核中"
	case "approved":
		return "审核通过"
	case "rejected":
		return "审核未通过"
	case "withdrawn":
		return "已撤回"
	default:
		return "fixture-unknown-review"
	}
}

func browserAvailability(value string) string {
	switch value {
	case "not_observed_live":
		return "待上架"
	case "scheduled":
		return "定时发布"
	case "live":
		return "已上线"
	case "superseded":
		return "已被新版本替代"
	default:
		return "fixture-unknown-availability"
	}
}
