package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type honorDraftFixture struct {
	fixture      *fiveFixture
	draftQueries int
	mutation     string
}

func newHonorLiveDraftFixture(t *testing.T) *honorDraftFixture {
	t.Helper()
	v := newFiveFixture(t, "honor")
	h := &honorDraftFixture{fixture: v}
	original := v.runtime.sender
	v.runtime.sender = func(lease *account.Lease, scope account.Scope) Sender {
		underlying := original(lease, scope)
		return m5Sender(func(ctx context.Context, q transport.Request) ([]byte, error) {
			u, _ := url.Parse(q.URL)
			if u.Path == "/openapi/v1/publish/get-app-detail" {
				notes, intro := "旧版说明", "SYNTHETIC-PRIVATE"
				version := int64(247)
				if len(v.writes) >= 4 {
					if h.mutation == "third_notes" {
						notes = "PRIVATE-UNREVIEWED-THIRD-NOTES"
					}
					if h.mutation == "other_metadata" {
						intro = "PRIVATE-UNREVIEWED-THIRD-INTRO"
					}
					if h.mutation == "new_live_version" {
						version = 248
					}
				}
				return raw(map[string]any{"code": 0, "data": map[string]any{"basicInfo": map[string]any{"appId": 123, "packageName": "com.example.app"}, "releaseInfo": map[string]any{"versionCode": version}, "languageInfo": []any{map[string]string{"languageId": "zh_CN", "appName": "合成应用", "intro": intro, "briefIntro": "简介", "newFeature": notes}}}}), nil
			}
			if u.Path == "/openapi/v1/publish/get-app-current-release" {
				h.draftQueries++
				if q.Method != "GET" || q.Class != transport.Read || q.OperationID != "" || q.Body != nil || u.Query().Get("appId") != "123" || len(u.Query()) != 1 || len(v.writes) != 4 {
					t.Fatal("draft query escaped read-only frozen post-write scope")
				}
				data := map[string]any{"releaseId": "PRIVATE-DRAFT-REFERENCE", "versionCode": 248, "versionName": "3.2.7", "auditResult": 4, "appId": 123}
				switch h.mutation {
				case "missing_draft":
					return []byte(`{"code":0}`), nil
				case "wrong_version":
					data["versionCode"] = 247
				case "wrong_name":
					data["versionName"] = "3.2.6"
				case "wrong_app":
					data["appId"] = 124
				case "submitted":
					data["auditResult"] = 0
				case "approved":
					data["auditResult"] = 1
				case "unknown_audit":
					data["auditResult"] = 99
				case "missing_audit":
					delete(data, "auditResult")
				case "query_failure":
					return nil, &transport.Error{Code: "network_error", Effect: "not_sent"}
				case "prior_approved":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 1
				case "prior_in_review":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 0
				case "prior_rejected":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 2
				case "prior_canceled":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 3
				case "prior_editing":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 4
				case "other_prior_approved":
					data["versionCode"], data["versionName"], data["auditResult"] = 246, "3.2.5", 1
				case "missing_draft_ref":
					delete(data, "releaseId")
				case "prior_unknown_audit":
					data["versionCode"], data["versionName"], data["auditResult"] = 247, "3.2.6", 99
				}
				return raw(map[string]any{"code": 0, "data": data}), nil
			}
			return underlying.Send(ctx, q)
		})
	}
	return h
}

func TestHonorTargetDraftPermitsExactLiveBaselineNotesAfterVerifiedOwnWrites(t *testing.T) {
	h := newHonorLiveDraftFixture(t)
	v := h.fixture
	p := v.prepared(t, "", false)
	ctx := context.Background()
	id := v.input.Snapshot.Manifest.ReleaseID
	if _, err := v.engine(p).Preflight(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if h.draftQueries != 0 {
		t.Fatal("preflight queried a draft without own writes")
	}
	p = v.prepared(t, p.BaselineRef(), true)
	result, err := v.engine(p).Submit(ctx, id, nil)
	if err != nil || !result.AllAccepted || !reflect.DeepEqual(v.writes, []string{"authorize", "upload", "bind", "metadata", "submit"}) || h.draftQueries != 1 {
		t.Fatalf("exact target draft chain failed: %v writes=%v reads=%d", err, v.writes, h.draftQueries)
	}
	d := p.LastInspectionDiagnostic()
	if d == nil || d.Reason != "verified_target_draft_with_live_baseline_notes" || !d.TargetDraftVerified || d.MetadataMatches || !d.NotesBaselineMatches || d.ObservedVersionCode != 247 {
		t.Fatalf("ambiguous draft diagnosis: %+v", d)
	}
	b, _ := json.Marshal(d)
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "旧版说明") || strings.Contains(string(b), v.input.Snapshot.Notes) {
		t.Fatal("draft query leaked private notes or reference")
	}
	p = v.prepared(t, p.BaselineRef(), true)
	if _, err = v.engine(p).Submit(ctx, id, nil); err != nil || len(v.writes) != 5 || h.draftQueries != 1 {
		t.Fatal("completed draft sequence repeated writes or queries")
	}
}

func TestHonorLiveBaselineFallbackRejectsWrongDraftAndMetadataDrift(t *testing.T) {
	for _, mutation := range []string{"missing_draft", "wrong_version", "wrong_name", "wrong_app", "submitted", "approved", "unknown_audit", "missing_audit", "query_failure", "third_notes", "other_metadata", "new_live_version"} {
		t.Run(mutation, func(t *testing.T) {
			h := newHonorLiveDraftFixture(t)
			h.mutation = mutation
			v := h.fixture
			ctx := context.Background()
			id := v.input.Snapshot.Manifest.ReleaseID
			p := v.prepared(t, "", false)
			if _, err := v.engine(p).Preflight(ctx, id, nil); err != nil {
				t.Fatal(err)
			}
			p = v.prepared(t, p.BaselineRef(), true)
			result, err := v.engine(p).Submit(ctx, id, nil)
			if err == nil || result.AllAccepted || len(v.writes) != 4 || len(result.Operations) != 4 {
				t.Fatalf("unverified draft reached submit: %v writes=%v", err, v.writes)
			}
			d := p.LastInspectionDiagnostic()
			if d == nil || d.TargetDraftVerified {
				t.Fatal("unverified target draft marked verified")
			}
			if mutation == "third_notes" || mutation == "other_metadata" || mutation == "new_live_version" {
				if h.draftQueries != 0 {
					t.Fatal("other drift queried a draft as bypass")
				}
			} else if h.draftQueries != 1 {
				t.Fatal("required exact draft query missing")
			}
			// Only rejected read evidence changed. A fresh instance can continue
			// from the original four confirmations and issue one final submission.
			h.mutation = ""
			p = v.prepared(t, p.BaselineRef(), true)
			if result, err = v.engine(p).Submit(ctx, id, nil); err != nil || !result.AllAccepted || len(v.writes) != 5 {
				t.Fatalf("normal guarded resume failed: %v", err)
			}
		})
	}
}

func TestHonorCurrentDraftReadGuardRejectsChangedIdentityAndRequestClass(t *testing.T) {
	guard := &accountReadSender{query: AccountQuery{Channel: "honor", Account: "fixture", PackageName: "com.example.app", AppID: "123"}}
	calls := 0
	underlying := m5Sender(func(context.Context, transport.Request) ([]byte, error) { calls++; return []byte(`{"code":0}`), nil })
	for _, tc := range []struct {
		suffix, method string
		class          transport.Class
		op             string
	}{{"?appId=124", "GET", transport.Read, ""}, {"?appId=123&appId=124", "GET", transport.Read, ""}, {"?appId=123&extra=1", "GET", transport.Read, ""}, {"?appId=123", "POST", transport.Read, ""}, {"?appId=123", "GET", transport.Write, ""}, {"?appId=123", "GET", transport.Read, "business-operation"}} {
		request := transport.Request{URL: origins["honor"] + "/openapi/v1/publish/get-app-current-release" + tc.suffix, Method: tc.method, Class: tc.class, OperationID: tc.op, Headers: http.Header{"Authorization": {"Bearer SYNTHETIC"}, "Content-Type": {"application/json; charset=utf-8"}}}
		if _, err := guard.send(context.Background(), underlying, request); err == nil || calls != 0 {
			t.Fatal("unscoped current-release query crossed boundary")
		}
	}
}

func TestHonorLiveBaselineFallbackRequiresBothVerifiedWriteProofs(t *testing.T) {
	for _, kind := range []string{"bind", "metadata"} {
		for _, application := range []string{"", "prior_approved"} {
			t.Run(kind+"/"+application, func(t *testing.T) {
				h := newHonorLiveDraftFixture(t)
				h.mutation = "wrong_version"
				v := h.fixture
				ctx := context.Background()
				id := v.input.Snapshot.Manifest.ReleaseID
				p := v.prepared(t, "", false)
				if _, err := v.engine(p).Preflight(ctx, id, nil); err != nil {
					t.Fatal(err)
				}
				p = v.prepared(t, p.BaselineRef(), true)
				if _, err := v.engine(p).Submit(ctx, id, nil); err == nil {
					t.Fatal("invalid draft accepted")
				}
				h.mutation = application
				before := h.draftQueries
				op := protocol.Digest([]string{v.input.Snapshot.Digest, "honor", kind})
				ref := credentials.Ref{Scope: p.target.Account, Name: materialName("material-" + account.Key(fiveReceiptKey(op)))}
				v.runtime.Manager.Vault.Delete(ctx, ref)
				p = v.prepared(t, p.BaselineRef(), true)
				if _, err := v.engine(p).Submit(ctx, id, nil); err == nil || len(v.writes) != 4 || h.draftQueries != before {
					t.Fatal("missing verified write proof reached draft fallback or submit")
				}
			})
		}
	}
}

func TestHonorTargetDraftDiagnosticKeepsFixedScalarsAndStrictGate(t *testing.T) {
	for _, tc := range []struct {
		mutation                             string
		version, audit                       int64
		versionMatches, nameMatches, editing bool
	}{{"wrong_version", 247, 4, false, true, true}, {"wrong_name", 248, 4, true, false, true}, {"approved", 248, 1, true, true, false}, {"submitted", 248, 0, true, true, false}} {
		t.Run(tc.mutation, func(t *testing.T) {
			h := newHonorLiveDraftFixture(t)
			h.mutation = tc.mutation
			v := h.fixture
			ctx := context.Background()
			id := v.input.Snapshot.Manifest.ReleaseID
			p := v.prepared(t, "", false)
			if _, err := v.engine(p).Preflight(ctx, id, nil); err != nil {
				t.Fatal(err)
			}
			p = v.prepared(t, p.BaselineRef(), true)
			result, err := v.engine(p).Submit(ctx, id, nil)
			if err == nil || result.AllAccepted || len(v.writes) != 4 || len(result.Operations) != 4 {
				t.Fatal("diagnostic change weakened the submission gate")
			}
			d := p.LastInspectionDiagnostic()
			if d == nil || d.Reason != "target_draft_not_verified" || d.TargetDraftVerified || d.ObservedDraftVersionCode != tc.version || d.ObservedDraftAuditResult != tc.audit || d.DraftVersionMatches != tc.versionMatches || d.DraftNameMatches != tc.nameMatches || d.DraftEditing != tc.editing || !d.DraftReleaseRefPresent {
				t.Fatalf("incorrect bounded draft diagnosis: %+v", d)
			}
			b, _ := json.Marshal(d)
			if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "3.2.7") || strings.Contains(string(b), "3.2.6") || strings.Contains(string(b), "旧版说明") {
				t.Fatal("draft reference, name or notes escaped into diagnosis")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(b, &fields) != nil {
				t.Fatal("invalid diagnostic schema")
			}
			for _, key := range []string{"observed_draft_version_code", "observed_draft_audit_result", "draft_version_matches", "draft_name_matches", "draft_editing", "draft_release_ref_present"} {
				if _, exists := fields[key]; !exists {
					t.Fatal("fixed draft diagnostic field missing")
				}
			}
		})
	}
}
