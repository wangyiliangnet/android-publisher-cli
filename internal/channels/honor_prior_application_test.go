package channels

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

func TestHonorOwnPendingUpdatesResumeWithExactPriorApprovedApplication(t *testing.T) {
	h := newHonorLiveDraftFixture(t)
	h.mutation = "prior_approved"
	v := h.fixture
	ctx := context.Background()
	id := v.input.Snapshot.Manifest.ReleaseID
	p := v.prepared(t, "", false)
	e := v.engine(p)
	if _, err := e.Preflight(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	target, _ := v.store.Target(id + "/honor")
	originalPreflight := target.Preflight
	p = v.prepared(t, p.BaselineRef(), true)
	e = v.engine(p)
	e.Hook = func(point string, q protocol.Request) error {
		if point == "after_receipt" && q.Kind == "metadata" {
			return errors.New("synthetic restart after four exact confirmations")
		}
		return nil
	}
	result, err := e.Submit(ctx, id, nil)
	if err == nil || len(result.Operations) != 4 || len(v.writes) != 4 || h.draftQueries != 0 {
		t.Fatal("did not stop at four exact confirmations")
	}
	// A new executor resolves the existing receipts and reads the preceding
	// final application. It sends only the pending target's final submission.
	p = v.prepared(t, p.BaselineRef(), true)
	result, err = v.engine(p).Submit(ctx, id, nil)
	if err != nil || !result.AllAccepted || result.AllApproved || result.AllLive || !reflect.DeepEqual(v.writes, []string{"authorize", "upload", "bind", "metadata", "submit"}) || h.draftQueries != 1 {
		t.Fatalf("prior approved application did not resume precisely: %v writes=%v", err, v.writes)
	}
	d := p.LastInspectionDiagnostic()
	if d == nil || d.Reason != "verified_own_pending_updates_with_prior_approved_release" || !d.PriorApprovedReleaseVerified || d.TargetDraftVerified || d.DraftVersionMatches || d.DraftNameMatches || d.DraftEditing || !d.DraftReleaseRefPresent || d.ObservedDraftVersionCode != 247 || d.ObservedDraftAuditResult != 1 || d.MetadataMatches || !d.NotesBaselineMatches {
		t.Fatalf("prior application was confused with the target: %+v", d)
	}
	for _, kind := range []string{"metadata", "submit"} {
		operation, err := v.store.Operation(id+"/honor", kind)
		if err != nil || operation == nil || operation.Effect != "confirmed_applied" || operation.Request.Scope != p.target.Scope || operation.Request.VersionCode != 248 || operation.Request.VersionName != "3.2.7" || operation.Request.Metadata["release_notes"] != v.input.Snapshot.Notes || operation.Request.Policy.Mode != "after_approval" || operation.Request.ArtifactSHA256 != p.target.APK.SHA256() {
			t.Fatal("pending target scope, notes or publishing policy changed")
		}
	}
	target, _ = v.store.Target(id + "/honor")
	if target.Preflight != originalPreflight {
		t.Fatal("prior final application rewrote the frozen baseline")
	}
	b, _ := json.Marshal(d)
	for _, private := range []string{"PRIVATE", "3.2.6", "3.2.7", "旧版说明", v.input.Snapshot.Notes} {
		if strings.Contains(string(b), private) {
			t.Fatal("diagnostic leaked private application content")
		}
	}
	p = v.prepared(t, p.BaselineRef(), true)
	if _, err = v.engine(p).Submit(ctx, id, nil); err != nil || len(v.writes) != 5 || h.draftQueries != 1 {
		t.Fatal("completed application was submitted again")
	}
}

func TestHonorPriorApplicationFallbackRejectsOtherStatusesVersionsAndDrift(t *testing.T) {
	for _, mutation := range []string{"prior_in_review", "prior_rejected", "prior_canceled", "prior_editing", "other_prior_approved", "prior_unknown_audit", "missing_draft_ref", "approved", "submitted", "third_notes", "other_metadata", "new_live_version"} {
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
				t.Fatal("conflicting or unverified application reached submit")
			}
			d := p.LastInspectionDiagnostic()
			if d == nil || d.PriorApprovedReleaseVerified || d.TargetDraftVerified {
				t.Fatal("unverified application marked verified")
			}
			h.mutation = "prior_approved"
			p = v.prepared(t, p.BaselineRef(), true)
			result, err = v.engine(p).Submit(ctx, id, nil)
			if err != nil || !result.AllAccepted || result.AllApproved || result.AllLive || len(v.writes) != 5 {
				t.Fatalf("guarded normal resume failed: %v", err)
			}
		})
	}
}
