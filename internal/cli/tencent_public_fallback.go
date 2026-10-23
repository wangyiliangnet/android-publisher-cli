package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type publicContextReader func(context.Context, string) (*protocol.ChannelContext, error)

// Even when the authenticated baseline cannot be opened, the public store is
// independent evidence. This path is status-only and cannot construct writes.
func tencentPublicFallback(ctx context.Context, s *state.Store, snapshot manifest.Snapshot, target state.Target, binding string, read publicContextReader, out io.Writer) int {
	var preflight protocol.RemoteSnapshot
	if target.Channel != "tencent" || json.Unmarshal([]byte(target.Preflight), &preflight) != nil || preflight.Scope != target.Scope || preflight.AdapterBinding != binding || !manifest.ValidHash(binding) {
		return failure(out, manifest.Production, "review_production_preflight", 2, fmt.Errorf("invalid saved observation binding"))
	}
	status := "not_due"
	if due, err := time.Parse(time.RFC3339Nano, target.NextCheckAt); err != nil || !due.After(time.Now()) {
		c, err := read(ctx, snapshot.Manifest.PackageName)
		if err != nil {
			return productionFailure(out, err)
		}
		if c == nil {
			return failure(out, manifest.Production, "retry_read", 3, fmt.Errorf("public store context unavailable"))
		}
		c.APIQueryHealth = "failed"
		c.APIVersionCode, c.APIVersionName, c.AuditCode, c.AuditStatus, c.RejectionReason = 0, "", "", "", ""
		c.InferredReview, c.InferenceBasis = "", ""
		if c.Validate() != nil {
			return failure(out, manifest.Production, "retry_read", 3, fmt.Errorf("public store context unavailable"))
		}
		generation, err := s.BeginObservation(target.ID)
		if err != nil {
			return productionFailure(out, err)
		}
		o := protocol.Observation{Scope: target.Scope, AdapterBinding: binding, VersionCode: snapshot.Manifest.VersionCode, RemoteRef: target.RemoteRef, Review: "unknown", Availability: "unknown", Source: "tencent_automatic_context", CollectedAt: time.Now().UTC(), Context: c}
		if err = s.Observe(target, generation, snapshot.Manifest.VersionCode, o, nil); err != nil {
			return productionFailure(out, err)
		}
		status = c.QueryStatus()
	}
	r, err := (&engine.Engine{Store: s}).Status(snapshot.Manifest.ReleaseID)
	if err != nil {
		return productionFailure(out, err)
	}
	r.Errors = map[string]string{"tencent": "authenticated query unavailable; public store facts are unassociated"}
	r.ObservationStatuses = map[string]engine.ObservationStatus{"tencent": {Status: status, NextAction: "check_keychain_network_or_channel_permission"}}
	if encode(out, r) != nil {
		return 5
	}
	return 3
}
