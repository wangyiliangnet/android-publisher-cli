package channels

import "strings"

// FiveInspectionDiagnostic describes comparison results using only fixed keys,
// fixed reasons and booleans. No metadata values or vendor messages are exposed.
type FiveInspectionDiagnostic struct {
	Reason                       string          `json:"reason"`
	PackageMatches               bool            `json:"package_matches"`
	AppMatches                   bool            `json:"app_matches"`
	VersionMatches               bool            `json:"version_matches"`
	MetadataMatches              bool            `json:"metadata_matches"`
	TargetDraftVerified          bool            `json:"target_draft_verified"`
	PriorApprovedReleaseVerified bool            `json:"prior_approved_release_verified"`
	ObservedDraftVersionCode     int64           `json:"observed_draft_version_code"`
	ObservedDraftAuditResult     int64           `json:"observed_draft_audit_result"`
	DraftVersionMatches          bool            `json:"draft_version_matches"`
	DraftNameMatches             bool            `json:"draft_name_matches"`
	DraftEditing                 bool            `json:"draft_editing"`
	DraftReleaseRefPresent       bool            `json:"draft_release_ref_present"`
	NotesLFMatches               bool            `json:"notes_lf_matches"`
	NotesBaselineMatches         bool            `json:"notes_baseline_matches"`
	NotesTerminalNewlineMatches  bool            `json:"notes_terminal_newline_matches"`
	NotesEmpty                   bool            `json:"notes_empty"`
	ObservedVersionCode          int64           `json:"observed_version_code"`
	LangFields                   map[string]bool `json:"lang_fields,omitempty"`
}

// This comparison is diagnostic only. It neither rewrites the frozen notes nor
// changes the strict metadata equality required by the publishing gate.
func fiveInspectionNotesLFMatch(actual, expected map[string]string) bool {
	value, present := actual["lang_notes"]
	want, wanted := expected["lang_notes"]
	return present && wanted && strings.ReplaceAll(value, "\r\n", "\n") == strings.ReplaceAll(want, "\r\n", "\n")
}

func fiveInspectionNotesBaselineMatch(actual, baseline map[string]string) bool {
	value, present := actual["lang_notes"]
	want, wanted := baseline["lang_notes"]
	return present && wanted && value == want
}

func fiveInspectionNotesTerminalNewlineMatch(actual, expected map[string]string) bool {
	value, present := actual["lang_notes"]
	want, wanted := expected["lang_notes"]
	if !present || !wanted {
		return false
	}
	normalized := func(s string) string {
		return strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\r\n")
	}
	return normalized(value) == normalized(want)
}

func fiveInspectionNotesEmpty(actual map[string]string) bool {
	value, present := actual["lang_notes"]
	return present && value == ""
}

func (p *PreparedProduction) LastInspectionDiagnostic() *FiveInspectionDiagnostic {
	if p == nil || p.inspectionDiagnostic == nil {
		return nil
	}
	out := *p.inspectionDiagnostic
	if out.LangFields != nil {
		out.LangFields = make(map[string]bool, len(p.inspectionDiagnostic.LangFields))
		for key, match := range p.inspectionDiagnostic.LangFields {
			out.LangFields[key] = match
		}
	}
	return &out
}

func fiveInspectionFields(channel string, actual, expected map[string]string) map[string]bool {
	if channel != "huawei" && channel != "honor" {
		return nil
	}
	out := make(map[string]bool, 6)
	for _, key := range []string{"lang_id", "lang_app_name", "lang_intro", "lang_brief", "lang_notes", "live_version_code"} {
		value, present := actual[key]
		want, wanted := expected[key]
		out[key] = present == wanted && value == want
	}
	return out
}
