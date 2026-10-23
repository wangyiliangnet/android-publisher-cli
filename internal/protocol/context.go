package protocol

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChannelContext contains useful current-channel facts which do not identify
// the immutable release. It must never settle writes or count as verified
// target review/availability evidence.
type ChannelContext struct {
	APIVersionCode   int64  `json:"api_version_code,omitempty"`
	APIVersionName   string `json:"api_version_name,omitempty"`
	StoreVersionCode int64  `json:"store_version_code,omitempty"`
	StoreVersionName string `json:"store_version_name,omitempty"`
	AuditStatus      string `json:"audit_status,omitempty"`
	AuditCode        string `json:"audit_code,omitempty"`
	RejectionReason  string `json:"rejection_reason,omitempty"`
	Association      string `json:"association"`
	InferredReview   string `json:"inferred_review,omitempty"`
	InferenceBasis   string `json:"inference_basis,omitempty"`
	APIQueryHealth   string `json:"api_query_health"`
	StoreQueryHealth string `json:"store_query_health"`
	SourceURL        string `json:"source_url,omitempty"`
}

var contextVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._+()\-]{0,79}$`)
var contextPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
var contextDigits = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)
var sensitiveContextText = regexp.MustCompile(`(?i)(https?://[^\s<>]+|\b(?:bearer|basic)\s+[^\s,;]+|\b(?:access[_-]?token|refresh[_-]?token|client[_-]?secret|api[_-]?(?:key|secret)|password|authorization|cookie|sign|sig|token|secret)\b["']?\s*[=:：]\s*(?:"[^"]*"|'[^']*'|[^\s,;，；]+)|[A-Za-z0-9_+/=\-]{24,})`)

// SanitizeContextReason is a bounded, plain-text projection of a vendor's
// rejection explanation. Raw responses and links never enter normal output.
func SanitizeContextReason(s string) string {
	if !utf8.ValidString(s) || len(s) > 8192 {
		return "[redacted explanation]"
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		if r == '<' || r == '>' || r == '`' {
			return ' '
		}
		return r
	}, s)
	s = sensitiveContextText.ReplaceAllString(s, "[redacted]")
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > 320 {
		s = string(r[:320]) + "…"
	}
	return s
}

func ValidContextVersion(s string) bool {
	return s == "" || contextVersion.MatchString(s) && !sensitiveContextText.MatchString(s)
}

// QueryStatus describes source collection health, independently of target
// review/availability. A useful public fact does not erase an API failure.
func (c *ChannelContext) QueryStatus() string {
	if c == nil {
		return "observed"
	}
	failed := c.APIQueryHealth == "failed" || c.StoreQueryHealth == "failed"
	healthy := c.APIQueryHealth == "healthy" || c.StoreQueryHealth == "healthy"
	if failed && healthy {
		return "partial"
	}
	if failed {
		return "query_failed"
	}
	return "observed"
}

func (c *ChannelContext) Validate() error {
	if c == nil {
		return nil
	}
	invalid := func() error { return fmt.Errorf("invalid non-target channel context") }
	if c.APIVersionCode < 0 || c.StoreVersionCode < 0 || !ValidContextVersion(c.APIVersionName) || !ValidContextVersion(c.StoreVersionName) || c.Association != "unconfirmed" {
		return invalid()
	}
	if !slices.Contains([]string{"healthy", "failed", "not_queried", "not_supported"}, c.APIQueryHealth) || !slices.Contains([]string{"healthy", "failed", "not_queried", "not_supported"}, c.StoreQueryHealth) {
		return invalid()
	}
	if c.AuditStatus != "" && !slices.Contains([]string{"unknown", "in_review", "rejected", "approved", "withdrawn"}, c.AuditStatus) || c.InferredReview != "" && !slices.Contains([]string{"unknown", "in_review", "approved"}, c.InferredReview) {
		return invalid()
	}
	if c.AuditCode != "" && !contextDigits.MatchString(c.AuditCode) || c.RejectionReason != SanitizeContextReason(c.RejectionReason) {
		return invalid()
	}
	if c.AuditCode != "" {
		status := map[string]string{"1": "in_review", "2": "rejected", "3": "approved", "8": "withdrawn"}[c.AuditCode]
		if status == "" {
			status = "unknown"
		}
		if c.AuditStatus != status {
			return invalid()
		}
	}
	if !slices.Contains([]string{"", "xiaomi_api_version_before_target", "xiaomi_api_version_equals_target", "xiaomi_api_version_after_target", "submission_not_confirmed"}, c.InferenceBasis) {
		return invalid()
	}
	if (c.APIVersionCode > 0 || c.APIVersionName != "" || c.AuditCode != "" || c.AuditStatus != "" || c.RejectionReason != "") && c.APIQueryHealth != "healthy" {
		return invalid()
	}
	if (c.StoreVersionCode > 0 || c.StoreVersionName != "") && c.StoreQueryHealth != "healthy" {
		return invalid()
	}
	if c.RejectionReason != "" && c.AuditStatus != "rejected" {
		return invalid()
	}
	if c.InferredReview != "" && c.InferredReview != "unknown" && (c.APIVersionCode <= 0 || c.InferenceBasis == "") {
		return invalid()
	}
	if c.InferredReview == "in_review" && c.InferenceBasis != "xiaomi_api_version_before_target" || c.InferredReview == "approved" && c.InferenceBasis != "xiaomi_api_version_equals_target" {
		return invalid()
	}
	if c.SourceURL != "" {
		u, e := url.Parse(c.SourceURL)
		if e != nil || u.Scheme != "https" || u.Host != "sj.qq.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || !strings.HasPrefix(u.Path, "/appdetail/") || !contextPackage.MatchString(strings.TrimPrefix(u.Path, "/appdetail/")) {
			return invalid()
		}
	}
	return nil
}
