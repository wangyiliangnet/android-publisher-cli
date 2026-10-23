package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestFiveInspectionHonorConfirmedProgressReportsOnlyKnownEquality(t *testing.T) {
	v := newFiveFixture(t, "honor")
	original := v.runtime.sender
	drift := false
	v.runtime.sender = func(lease *account.Lease, scope account.Scope) Sender {
		underlying := original(lease, scope)
		return m5Sender(func(ctx context.Context, q transport.Request) ([]byte, error) {
			u, _ := url.Parse(q.URL)
			if drift && u.Path == "/openapi/v1/publish/get-app-detail" {
				if q.Class != transport.Read || q.Method != "GET" || q.OperationID != "" {
					t.Fatal("inspection reached a business request")
				}
				return raw(map[string]any{"code": 0, "data": map[string]any{
					"basicInfo":   map[string]any{"appId": 123, "packageName": "com.example.app"},
					"releaseInfo": map[string]any{"versionCode": v.version},
					"languageInfo": []any{map[string]string{
						"languageId": "zh_CN", "appName": "合成应用", "intro": "PRIVATE-DRIFTED-INTRO", "briefIntro": "简介",
						"newFeature": strings.ReplaceAll(v.notes, "\n", "\r\n"), "PRIVATE-UNEXPECTED-KEY": "PRIVATE-UNEXPECTED-VALUE",
					}},
				}}), nil
			}
			return underlying.Send(ctx, q)
		})
	}
	p := v.prepared(t, "", false)
	if _, err := v.engine(p).Preflight(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil); err != nil {
		t.Fatal(err)
	}
	initial := p.LastInspectionDiagnostic()
	if initial == nil || initial.Reason != "verified" || !initial.MetadataMatches || len(initial.LangFields) != 6 {
		t.Fatalf("missing successful comparison: %+v", initial)
	}
	if !initial.NotesBaselineMatches || initial.NotesEmpty || !initial.NotesTerminalNewlineMatches {
		t.Fatal("initial notes diagnostic lost the baseline comparison")
	}
	p = v.prepared(t, p.BaselineRef(), true)
	e := v.engine(p)
	e.Hook = func(point string, q protocol.Request) error {
		if point == "before_intent" && q.Kind == "submit" {
			return errors.New("stop synthetic fixture after verified metadata")
		}
		return nil
	}
	if _, err := e.Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil); err == nil {
		t.Fatal("synthetic stopping hook was not reached")
	}
	if got := strings.Join(v.writes, ","); got != "authorize,upload,bind,metadata" {
		t.Fatalf("unexpected pre-inspection writes: %s", got)
	}
	drift = true
	if _, err := p.Inspect(context.Background(), p.target.Scope); err == nil {
		t.Fatal("metadata drift was accepted")
	}
	d := p.LastInspectionDiagnostic()
	if d == nil || d.Reason != "preflight_changed" || !d.PackageMatches || !d.AppMatches || !d.VersionMatches || d.MetadataMatches || d.ObservedVersionCode != 247 {
		t.Fatalf("incorrect bounded comparison: %+v", d)
	}
	if len(d.LangFields) != 6 || d.LangFields["lang_intro"] || d.LangFields["lang_notes"] || !d.LangFields["lang_id"] || !d.LangFields["lang_app_name"] || !d.LangFields["lang_brief"] || !d.LangFields["live_version_code"] {
		t.Fatalf("wrong fixed field equality: %+v", d.LangFields)
	}
	if !d.NotesLFMatches || d.MetadataMatches {
		t.Fatal("CRLF diagnostic either missed equivalent notes or relaxed the gate")
	}
	if d.NotesBaselineMatches || !d.NotesTerminalNewlineMatches || d.NotesEmpty {
		t.Fatal("post-metadata notes diagnostic confused new notes with old baseline")
	}
	b, err := json.Marshal(d)
	if err != nil || strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), v.notes) || strings.Contains(string(b), "SYNTHETIC") || strings.Contains(string(b), "合成应用") {
		t.Fatal("inspection diagnostic exposed private field names or values")
	}
	d.LangFields["lang_intro"] = true
	d.Reason = "caller-mutation"
	if p.LastInspectionDiagnostic().LangFields["lang_intro"] || p.LastInspectionDiagnostic().Reason != "preflight_changed" {
		t.Fatal("caller mutated internal inspection evidence")
	}
	if len(v.writes) != 4 {
		t.Fatal("failed inspection issued another business request")
	}
}

func TestFiveInspectionQueryFailureDoesNotExposeVendorMessage(t *testing.T) {
	v := newFiveFixture(t, "honor")
	p := v.prepared(t, "", false)
	if _, err := p.Inspect(context.Background(), p.target.Scope); err != nil {
		t.Fatal(err)
	}
	p.reader.sender = func(*account.Lease, account.Scope) Sender {
		return m5Sender(func(context.Context, transport.Request) ([]byte, error) {
			return nil, &Error{Code: "PRIVATE-VENDOR-MESSAGE", Effect: "unknown"}
		})
	}
	if _, err := p.Inspect(context.Background(), p.target.Scope); err == nil {
		t.Fatal("query failure was accepted")
	}
	d := p.LastInspectionDiagnostic()
	if d == nil || d.Reason != "query_failed" || d.MetadataMatches || d.LangFields != nil || d.ObservedVersionCode != 0 || d.NotesBaselineMatches || d.NotesTerminalNewlineMatches || d.NotesEmpty {
		t.Fatalf("query failure retained stale comparison: %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatal("vendor message escaped into diagnostic")
	}
	if len(v.writes) != 0 {
		t.Fatal("inspection performed business writes")
	}
}

func TestFiveInspectionFixedFieldComparisonChecksPresence(t *testing.T) {
	d := fiveInspectionFields("honor", map[string]string{}, map[string]string{"lang_notes": ""})
	if len(d) != 6 || d["lang_notes"] || !d["lang_intro"] {
		t.Fatal("missing value was equated with a present empty field")
	}
	if fiveInspectionFields("vivo", map[string]string{"PRIVATE": "VALUE"}, nil) != nil {
		t.Fatal("unrecognized metadata keys entered diagnostic")
	}
}

func TestFiveInspectionNotesLFComparisonIsDiagnosticOnly(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actual, expected map[string]string
		want             bool
	}{
		{"CRLF_to_LF", map[string]string{"lang_notes": "line1\r\nline2"}, map[string]string{"lang_notes": "line1\nline2"}, true},
		{"LF_to_CRLF", map[string]string{"lang_notes": "line1\nline2"}, map[string]string{"lang_notes": "line1\r\nline2"}, true},
		{"same_LF", map[string]string{"lang_notes": "line1\nline2"}, map[string]string{"lang_notes": "line1\nline2"}, true},
		{"changed_text", map[string]string{"lang_notes": "line1\r\nchanged"}, map[string]string{"lang_notes": "line1\nline2"}, false},
		{"bare_CR", map[string]string{"lang_notes": "line1\rline2"}, map[string]string{"lang_notes": "line1\nline2"}, false},
		{"spaces", map[string]string{"lang_notes": "line1 \r\nline2"}, map[string]string{"lang_notes": "line1\nline2"}, false},
		{"trailing_newline", map[string]string{"lang_notes": "line1\r\nline2\r\n"}, map[string]string{"lang_notes": "line1\nline2"}, false},
		{"both_missing", nil, nil, false},
		{"actual_missing", nil, map[string]string{"lang_notes": ""}, false},
		{"expected_missing", map[string]string{"lang_notes": ""}, nil, false},
		{"present_empty", map[string]string{"lang_notes": ""}, map[string]string{"lang_notes": ""}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fiveInspectionNotesLFMatch(tc.actual, tc.expected); got != tc.want {
				t.Fatalf("LF comparison got %t, want %t", got, tc.want)
			}
		})
	}
}

func TestFiveInspectionNotesTerminalNewlineComparisonDoesNotTrimContent(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actual, expected map[string]string
		want             bool
	}{
		{"removed_trailing_LF", map[string]string{"lang_notes": "line1\nline2"}, map[string]string{"lang_notes": "line1\nline2\n"}, true},
		{"removed_trailing_CRLF", map[string]string{"lang_notes": "line1\r\nline2"}, map[string]string{"lang_notes": "line1\nline2\r\n"}, true},
		{"trailing_CR_LF_only", map[string]string{"lang_notes": "line1\r\nline2\r\n\r\n"}, map[string]string{"lang_notes": "line1\nline2\r"}, true},
		{"changed_text", map[string]string{"lang_notes": "line1\nchanged"}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"bare_internal_CR", map[string]string{"lang_notes": "line1\rline2"}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"internal_blank_line", map[string]string{"lang_notes": "line1\n\nline2"}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"leading_newline", map[string]string{"lang_notes": "\nline1\nline2"}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"trailing_space", map[string]string{"lang_notes": "line1\nline2 "}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"spaces_after_newline", map[string]string{"lang_notes": "line1\nline2\n "}, map[string]string{"lang_notes": "line1\nline2\n"}, false},
		{"both_missing", nil, nil, false},
		{"actual_missing", nil, map[string]string{"lang_notes": ""}, false},
		{"expected_missing", map[string]string{"lang_notes": ""}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fiveInspectionNotesTerminalNewlineMatch(tc.actual, tc.expected); got != tc.want {
				t.Fatalf("terminal newline comparison got %t, want %t", got, tc.want)
			}
		})
	}
}

func TestFiveInspectionNotesBaselineAndEmptyComparisonsStayExact(t *testing.T) {
	old := map[string]string{"lang_notes": "old\nnotes\n"}
	if !fiveInspectionNotesBaselineMatch(old, old) || fiveInspectionNotesBaselineMatch(map[string]string{"lang_notes": "old\r\nnotes\r\n"}, old) || fiveInspectionNotesBaselineMatch(map[string]string{"lang_notes": "old\nnotes"}, old) || fiveInspectionNotesBaselineMatch(nil, nil) {
		t.Fatal("baseline diagnostic normalized old notes or accepted missing values")
	}
	if !fiveInspectionNotesEmpty(map[string]string{"lang_notes": ""}) || fiveInspectionNotesEmpty(nil) || fiveInspectionNotesEmpty(map[string]string{"lang_notes": "\n"}) || fiveInspectionNotesEmpty(map[string]string{"lang_notes": " "}) {
		t.Fatal("empty notes diagnostic trimmed content or accepted a missing key")
	}
}
