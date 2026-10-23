package channels

import (
	"context"
	"reflect"
)

// Honor get-app-detail's releaseInfo describes the live version. After this
// executor has verified both its binding and exact language-write responses,
// unchanged live notes are acceptable only alongside either the exact editable
// target draft or the original version's already approved application. The
// latter is evidence of the preceding final application, not a target draft or
// target approval. Pending target changes are established by the exact verified
// write receipts. This query contains no notes and never replaces the baseline
// or independently establishes a language-write outcome.
func (p *PreparedProduction) honorLiveBaselineDraft(ctx context.Context, c *Client, d Details, metadata map[string]string, progress map[string]bool) (bool, error) {
	if !p.enabled || p.target.Account.Channel != "honor" || !progress["bind"] || !progress["metadata"] || d.PackageName != p.target.PackageName || d.AppID != p.target.AppID || d.VersionCode != p.target.Existing.VersionCode {
		return false, nil
	}
	baseline := copyFields(p.target.Metadata)
	delete(baseline, "live_version_code")
	if !reflect.DeepEqual(metadata, baseline) {
		return false, nil
	}
	// At this point every live field, including the old notes, still equals
	// the immutable baseline. Any third value or other drift rejects earlier.
	draft, err := c.HonorCurrentRelease(ctx, p.target.AppID)
	if err != nil {
		p.inspectionDiagnostic.Reason = "target_draft_query_failed"
		return false, err
	}
	p.inspectionDiagnostic.ObservedDraftVersionCode = draft.VersionCode
	p.inspectionDiagnostic.ObservedDraftAuditResult = draft.AuditResult
	p.inspectionDiagnostic.DraftVersionMatches = draft.VersionCode == p.target.VersionCode
	p.inspectionDiagnostic.DraftNameMatches = draft.VersionName == p.target.VersionName
	p.inspectionDiagnostic.DraftEditing = draft.AuditResult == 4
	p.inspectionDiagnostic.DraftReleaseRefPresent = draft.ReleaseID != ""
	if draft.VersionCode == p.target.Existing.VersionCode && draft.AuditResult == 1 && draft.ReleaseID != "" {
		p.inspectionDiagnostic.Reason = "verified_own_pending_updates_with_prior_approved_release"
		p.inspectionDiagnostic.PriorApprovedReleaseVerified = true
		return true, nil
	}
	if draft.VersionCode != p.target.VersionCode || draft.VersionName != p.target.VersionName || draft.AuditResult != 4 || draft.ReleaseID == "" {
		p.inspectionDiagnostic.Reason = "target_draft_not_verified"
		return false, nil
	}
	p.inspectionDiagnostic.Reason = "verified_target_draft_with_live_baseline_notes"
	p.inspectionDiagnostic.TargetDraftVerified = true
	return true, nil
}
