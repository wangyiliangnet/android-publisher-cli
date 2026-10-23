package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

var ErrUnknown = errors.New("operation result is unknown; reconcile before retrying")
var ErrEvidence = errors.New("remote evidence is insufficient; keep operation blocked")
var ErrInvalidInput = errors.New("invalid release input or channel selection")

type Engine struct {
	Store     *state.Store
	Inspector artifact.Inspector
	Adapter   protocol.Adapter
	// Hook is an in-process fault-injection seam. It is never populated from CLI environment or production flags.
	Hook func(string, protocol.Request) error
}
type Summary struct {
	SchemaVersion        int                              `json:"schema_version"`
	ReleaseID            string                           `json:"release_id"`
	IntentDigest         string                           `json:"intent_digest"`
	Mode                 string                           `json:"mode"`
	Total                int                              `json:"total_targets"`
	Accepted             int                              `json:"accepted_targets"`
	Approved             int                              `json:"approved_targets"`
	Live                 int                              `json:"live_targets"`
	AllAccepted          bool                             `json:"all_accepted"`
	AllApproved          bool                             `json:"all_approved"`
	AllLive              bool                             `json:"all_live"`
	RecoveryRequired     bool                             `json:"recovery_required"`
	Targets              []state.Target                   `json:"targets"`
	Errors               map[string]string                `json:"errors,omitempty"`
	Operations           []state.OperationStatus          `json:"operations"`
	Diffs                map[string]MetadataDiff          `json:"metadata_diffs,omitempty"`
	ObservationStatuses  map[string]ObservationStatus     `json:"observation_statuses,omitempty"`
	AuthorizationHistory []state.OPPOAuthorizationAttempt `json:"authorization_history,omitempty"`
}
type ObservationStatus struct {
	Status     string `json:"status"`
	NextAction string `json:"next_action"`
}
type MetadataDiff struct {
	Before map[string]string `json:"before"`
	After  map[string]string `json:"after"`
	Policy manifest.Policy   `json:"release_policy"`
}

func (e *Engine) Plan(ctx context.Context, path string) (Summary, error) {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return Summary{}, err
	}
	defer release()
	m, notes, err := manifest.Load(path, e.Store.Mode)
	if err != nil {
		return Summary{}, err
	}
	if err = artifact.Freeze(ctx, e.Store.Root, m, e.Inspector); err != nil {
		return Summary{}, err
	}
	snapshot := manifest.Snapshot{Manifest: m, Notes: notes}
	snapshot.Digest = snapshot.CalculateDigest()
	id, err := e.Store.Save(snapshot)
	if err != nil {
		return Summary{}, err
	}
	return e.Status(id)
}
func (e *Engine) Status(id string) (Summary, error) {
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return Summary{}, err
	}
	recovery, err := e.Store.RecoveryRequired()
	if err != nil {
		return Summary{}, err
	}
	r := Summary{SchemaVersion: 1, ReleaseID: id, IntentDigest: snapshot.Digest, Mode: e.Store.Mode, Total: len(targets), Targets: targets, RecoveryRequired: recovery}
	r.Operations, err = e.Store.OperationStatuses(id)
	if err != nil {
		return Summary{}, err
	}
	r.AuthorizationHistory, err = e.Store.OPPOAuthorizationHistory(id)
	if err != nil {
		return Summary{}, err
	}
	for _, t := range targets {
		if t.Phase == "submitted" {
			r.Accepted++
		}
		if t.Review == "approved" && !t.Disputed {
			r.Approved++
		}
		if t.Availability == "live" && !t.Disputed {
			r.Live++
		}
	}
	r.AllAccepted = r.Total > 0 && r.Accepted == r.Total
	r.AllApproved = r.Total > 0 && r.Approved == r.Total
	r.AllLive = r.Total > 0 && r.Live == r.Total
	return r, nil
}
func selected(targets []state.Target, channels []string) ([]state.Target, error) {
	if len(channels) == 0 {
		return targets, nil
	}
	seen := map[string]bool{}
	out := []state.Target{}
	for _, channel := range channels {
		if seen[channel] {
			return nil, fmt.Errorf("%w: duplicate channel selection", ErrInvalidInput)
		}
		seen[channel] = true
		found := false
		for _, t := range targets {
			if t.Channel == channel {
				out = append(out, t)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: selected channel is outside the immutable release", ErrInvalidInput)
		}
	}
	return out, nil
}
func (e *Engine) execution(id string, write bool) error {
	if e.Adapter != nil && e.Store.Mode == manifest.Simulation {
		return nil
	}
	provider, ok := e.Adapter.(protocol.ProductionBindingProvider)
	if !ok || e.Store.Mode != manifest.Production {
		return fmt.Errorf("production execution requires a frozen production adapter; simulation must use isolated simulation state")
	}
	scope, intent, writable := provider.ProductionScope()
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return err
	}
	if len(targets) != 1 || !slices.Contains(manifest.Channels, targets[0].Channel) || targets[0].Scope != scope || snapshot.Digest != intent || (write && !writable) {
		return fmt.Errorf("production adapter or approval does not match the frozen release")
	}
	return nil
}
func (e *Engine) Preflight(ctx context.Context, id string, channels []string) (Summary, error) {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return Summary{}, err
	}
	defer release()
	if err := e.execution(id, false); err != nil {
		return Summary{}, err
	}
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return Summary{}, err
	}
	if err = artifact.VerifyFrozen(ctx, e.Store.Root, snapshot, e.Inspector); err != nil {
		return Summary{}, err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err = selected(targets, channels)
	if err != nil {
		return Summary{}, err
	}
	failures := map[string]string{}
	diffs := map[string]MetadataDiff{}
	for _, t := range targets {
		err = e.Store.CheckWritable(t.Scope, t.ID)
		var remote protocol.RemoteSnapshot
		if err == nil {
			remote, err = e.Adapter.Inspect(ctx, t.Scope)
		}
		if err == nil && remote.ActiveIntent != "" && remote.ActiveIntent != snapshot.Digest {
			err = fmt.Errorf("remote application has another active intent")
		}
		if err == nil && remote.ActiveIntent == snapshot.Digest && t.Phase != "submitted" {
			err = fmt.Errorf("remote intent exists without a confirmed local submission; reconcile inventory first")
		}
		if err == nil {
			err = e.Store.Preflight(t, remote)
			if err == nil {
				after := manifest.ApplyPatch(remote.Metadata, snapshot.Manifest.Metadata)
				after["release_notes"] = snapshot.Notes
				diffs[t.Channel] = MetadataDiff{Before: remote.Metadata, After: after, Policy: snapshot.Manifest.ReleasePolicy}
			}
		}
		if err != nil {
			failures[t.Channel] = err.Error()
		}
	}
	r, resultErr := e.result(id, failures)
	r.Diffs = diffs
	return r, resultErr
}
func (e *Engine) result(id string, failures map[string]string) (Summary, error) {
	r, err := e.Status(id)
	if err != nil {
		return r, err
	}
	if len(failures) > 0 {
		r.Errors = failures
		return r, fmt.Errorf("one or more targets are blocked")
	}
	return r, nil
}
func Steps(channel string) []string {
	if channel == "xiaomi" {
		return []string{"push"}
	}
	return []string{"upload", "bind", "submit"}
}
func (e *Engine) hook(point string, q protocol.Request) error {
	if e.Hook != nil {
		return e.Hook(point, q)
	}
	return nil
}

func (e *Engine) Submit(ctx context.Context, id string, channels []string) (Summary, error) {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return Summary{}, err
	}
	defer release()
	if err := e.execution(id, true); err != nil {
		return Summary{}, err
	}
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err = selected(targets, channels)
	if err != nil {
		return Summary{}, err
	}
	failures := map[string]string{}
	for _, t := range targets {
		if err := e.submitTarget(ctx, snapshot, t); err != nil {
			failures[t.Channel] = err.Error()
		}
	}
	return e.result(id, failures)
}
func (e *Engine) submitTarget(ctx context.Context, snapshot manifest.Snapshot, t state.Target) error {
	if t.Phase == "submitted" {
		return nil
	}
	if t.Preflight == "" {
		return fmt.Errorf("preflight is required")
	}
	var preflight protocol.RemoteSnapshot
	if err := json.Unmarshal([]byte(t.Preflight), &preflight); err != nil {
		return fmt.Errorf("corrupt preflight")
	}
	previous := ""
	var err error
	steps := Steps(t.Channel)
	if planner, ok := e.Adapter.(protocol.Planner); ok {
		metadata := manifest.ApplyPatch(preflight.Metadata, snapshot.Manifest.Metadata)
		metadata["release_notes"] = snapshot.Notes
		steps, err = planner.BuildPlan(protocol.Request{TargetID: t.ID, Scope: t.Scope, Channel: t.Channel, IntentDigest: snapshot.Digest, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, Policy: snapshot.Manifest.ReleasePolicy, Metadata: metadata, SnapshotDigest: protocol.Digest(preflight.Metadata), AdapterBinding: preflight.AdapterBinding, ArtifactSHA256: snapshot.Manifest.Artifacts[snapshot.Manifest.Targets[t.Channel].Artifact].SHA256})
		if err != nil {
			return err
		}
		if len(steps) == 0 || (steps[len(steps)-1] != "submit" && steps[len(steps)-1] != "push") {
			return fmt.Errorf("invalid adapter plan")
		}
		seen := map[string]bool{}
		for _, step := range steps {
			if seen[step] || !slices.Contains([]string{"authorize", "upload", "bind", "metadata", "submit", "push"}, step) {
				return fmt.Errorf("invalid adapter plan")
			}
			seen[step] = true
		}
	}
	var checkpoints map[string]string
	if _, ok := e.Adapter.(protocol.Planner); ok {
		checkpoints = map[string]string{}
	}
	expected := preflight
	for _, kind := range steps {
		op, err := e.Store.Operation(t.ID, kind)
		if err != nil {
			return err
		}
		if op != nil {
			switch op.Effect {
			case "confirmed_applied":
				previous = op.RemoteRef
				if checkpoints != nil {
					checkpoints[kind] = op.RemoteRef
				}
				if advancer, ok := e.Adapter.(protocol.PreflightAdvancer); ok {
					expected, err = advancer.ExpectedSnapshot(op.Request, expected)
					if err != nil {
						return err
					}
				}
				continue
			case "intent", "unknown":
				if err = e.reconcileOperation(ctx, op.Request); err != nil {
					return err
				}
				op, err = e.Store.Operation(t.ID, kind)
				if err != nil {
					return err
				}
				if op.Effect == "confirmed_applied" {
					previous = op.RemoteRef
					if checkpoints != nil {
						checkpoints[kind] = op.RemoteRef
					}
					if advancer, ok := e.Adapter.(protocol.PreflightAdvancer); ok {
						expected, err = advancer.ExpectedSnapshot(op.Request, expected)
						if err != nil {
							return err
						}
					}
					continue
				}
			case "confirmed_rejected":
				return fmt.Errorf("store rejected this immutable operation; fix the intent before retrying")
			case "confirmed_not_applied":
			default:
				return fmt.Errorf("unknown saved operation effect")
			}
		}
		if err = artifact.VerifyFrozen(ctx, e.Store.Root, snapshot, e.Inspector); err != nil {
			return err
		}
		if err = e.Store.CheckWritable(t.Scope, t.ID); err != nil {
			return err
		}
		remote, err := e.Adapter.Inspect(ctx, t.Scope)
		if err != nil {
			return fmt.Errorf("remote pre-write inspection failed")
		}
		if remote.Scope != t.Scope || remote.AdapterBinding != expected.AdapterBinding || remote.Metadata == nil || protocol.Digest(remote.Metadata) != protocol.Digest(expected.Metadata) || remote.ActiveIntent != "" {
			return fmt.Errorf("remote data changed; refresh preflight and review the diff")
		}
		q := protocol.Request{OperationID: protocol.Digest([]string{snapshot.Digest, t.Channel, kind}), TargetID: t.ID, Scope: t.Scope, Channel: t.Channel, IntentDigest: snapshot.Digest, Kind: kind, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, PreviousRef: previous, SnapshotDigest: protocol.Digest(preflight.Metadata), AdapterBinding: preflight.AdapterBinding, Metadata: manifest.ApplyPatch(preflight.Metadata, snapshot.Manifest.Metadata), Policy: snapshot.Manifest.ReleasePolicy}
		q.ArtifactSHA256 = snapshot.Manifest.Artifacts[snapshot.Manifest.Targets[t.Channel].Artifact].SHA256
		if checkpoints != nil {
			q.Checkpoints = map[string]string{}
			for k, v := range checkpoints {
				q.Checkpoints[k] = v
			}
		}
		q.Metadata["release_notes"] = snapshot.Notes
		if op != nil {
			if kind == "authorize" && t.Channel == "huawei" && op.Request.ValidAuthorizationGeneration() {
				q.AuthorizationGeneration = op.Request.AuthorizationGeneration
			}
			if protocol.Digest(q) != protocol.Digest(op.Request) {
				return fmt.Errorf("retry would change the persisted operation")
			}
			q = op.Request
		}
		if err = e.hook("before_intent", q); err != nil {
			return err
		}
		if err = e.Store.Begin(q); err != nil {
			return err
		}
		if err = e.hook("after_intent", q); err != nil {
			return err
		}
		receipt, executeErr := e.Adapter.ExecuteOne(ctx, q)
		if err = e.hook("after_execute", q); err != nil {
			return err
		}
		if executeErr == nil {
			executeErr = receipt.Validate(q)
		}
		if executeErr != nil {
			effect := "unknown"
			var outcome protocol.WriteOutcome
			if errors.As(executeErr, &outcome) && outcome.WriteEffect() == "not_sent" {
				effect = "confirmed_not_applied"
			}
			if err = e.Store.Complete(q, effect, "", "transport or receipt validation failed"); err != nil {
				return err
			}
			return ErrUnknown
		}
		if err = e.Store.Complete(q, receipt.Effect, receipt.RemoteRef, ""); err != nil {
			return err
		}
		if err = e.hook("after_receipt", q); err != nil {
			return err
		}
		if receipt.Effect != "confirmed_applied" {
			return fmt.Errorf("store explicitly rejected the operation")
		}
		previous = receipt.RemoteRef
		if checkpoints != nil {
			checkpoints[kind] = receipt.RemoteRef
		}
		if advancer, ok := e.Adapter.(protocol.PreflightAdvancer); ok {
			expected, err = advancer.ExpectedSnapshot(q, expected)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) reconcileOperation(ctx context.Context, q protocol.Request) error {
	r, err := e.Adapter.Reconcile(ctx, q)
	if err != nil {
		return ErrEvidence
	}
	if r.OperationID != q.OperationID || r.RequestDigest != protocol.Digest(q) {
		return ErrEvidence
	}
	switch r.Result {
	case "confirmed_applied":
		if r.Receipt == nil || r.Receipt.Effect != "confirmed_applied" || r.Receipt.Validate(q) != nil {
			return ErrEvidence
		}
		return e.Store.Complete(q, "confirmed_applied", r.Receipt.RemoteRef, "")
	case "confirmed_not_applied":
		return e.Store.Complete(q, "confirmed_not_applied", "", "")
	case "inconclusive":
		return ErrEvidence
	default:
		return ErrEvidence
	}
}
func (e *Engine) Reconcile(ctx context.Context, id string, channels []string) (Summary, error) {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return Summary{}, err
	}
	defer release()
	if err := e.execution(id, false); err != nil {
		return Summary{}, err
	}
	if _, err := e.Store.Load(id); err != nil {
		return Summary{}, err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err = selected(targets, channels)
	if err != nil {
		return Summary{}, err
	}
	failures := map[string]string{}
	operations, err := e.Store.OperationStatuses(id)
	if err != nil {
		return Summary{}, err
	}
	for _, t := range targets {
		for _, saved := range operations {
			if saved.Channel != t.Channel {
				continue
			}
			op, err := e.Store.Operation(t.ID, saved.Kind)
			if err == nil && op != nil && slices.Contains([]string{"intent", "unknown"}, op.Effect) {
				err = e.reconcileOperation(ctx, op.Request)
			}
			if err != nil {
				failures[t.Channel] = err.Error()
				break
			}
		}
	}
	return e.result(id, failures)
}
func (e *Engine) Refresh(ctx context.Context, id string, channels []string) (Summary, error) {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return Summary{}, err
	}
	defer release()
	if err := e.execution(id, false); err != nil {
		return Summary{}, err
	}
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err := e.Store.Targets(id)
	if err != nil {
		return Summary{}, err
	}
	targets, err = selected(targets, channels)
	if err != nil {
		return Summary{}, err
	}
	failures := map[string]string{}
	statuses := map[string]ObservationStatus{}
	result := func() (Summary, error) {
		summary, err := e.result(id, failures)
		if len(statuses) > 0 {
			summary.ObservationStatuses = statuses
		}
		return summary, err
	}
	for _, t := range targets {
		if t.Phase != "submitted" {
			continue
		}
		if ctx.Err() != nil {
			summary, _ := result()
			return summary, ctx.Err()
		}
		// A scheduling skip is not a successful query. Preserve an existing
		// query-health error until a later, due observation clears it.
		if t.LastError != "" {
			failures[t.Channel] = "query failed or target evidence insufficient"
		}
		if at, err := time.Parse(time.RFC3339Nano, t.NextCheckAt); err == nil && at.After(time.Now()) {
			continue
		}
		generation, err := e.Store.BeginObservation(t.ID)
		if err != nil {
			return Summary{}, err
		}
		var o protocol.Observation
		var queryErr error
		if provider, ok := e.Adapter.(protocol.BindingProvider); ok {
			var frozen protocol.RemoteSnapshot
			binding, err := provider.AdapterBinding(t.Scope)
			if err != nil || json.Unmarshal([]byte(t.Preflight), &frozen) != nil || binding == "" || binding != frozen.AdapterBinding {
				queryErr = fmt.Errorf("adapter configuration differs from frozen observation scope")
			}
		}
		if queryErr == nil {
			o, queryErr = e.Adapter.Observe(ctx, t.Scope, snapshot.Manifest.VersionCode, t.RemoteRef)
		}
		if queryErr == nil {
			queryErr = o.Validate(t.Scope, snapshot.Manifest.VersionCode, t.RemoteRef)
		}
		if err = e.Store.Observe(t, generation, snapshot.Manifest.VersionCode, o, queryErr); err != nil {
			return Summary{}, err
		}
		updated, readErr := e.Store.Target(t.ID)
		if readErr != nil {
			return Summary{}, readErr
		}
		if updated.LastError != "" {
			failures[t.Channel] = "query failed or target evidence insufficient"
		} else {
			delete(failures, t.Channel)
		}
		if queryErr != nil {
			failures[t.Channel] = "query failed or target evidence insufficient"
			// Observation adapters may provide a fixed, public status. Never
			// expose their raw error strings, stderr or page content here.
			var status interface{ ObservationStatus() string }
			if errors.As(queryErr, &status) {
				code := status.ObservationStatus()
				if action := observationNextAction(code); action != "" {
					statuses[t.Channel] = ObservationStatus{Status: code, NextAction: action}
				}
			}
		} else if o.Context != nil && o.Context.QueryStatus() != "observed" {
			statuses[t.Channel] = ObservationStatus{Status: o.Context.QueryStatus(), NextAction: "retry_read"}
		} else if updated.LastError != "" {
			// State may reject evidence after protocol validation (for example,
			// a missing file or changed digest). Its durable query-health result
			// is authoritative; a nil transport error does not prove observation.
			statuses[t.Channel] = ObservationStatus{Status: "evidence_insufficient", NextAction: "verify_target_evidence"}
		} else {
			statuses[t.Channel] = ObservationStatus{Status: "observed", NextAction: "wait_next_check"}
		}
	}
	return result()
}

func observationNextAction(status string) string {
	switch status {
	case "auth_required":
		return "login_required"
	case "profile_busy":
		return "close_other_observer"
	case "evidence_insufficient":
		return "verify_target_evidence"
	case "blocked_navigation":
		return "check_fixture_origin"
	case "timed_out":
		return "retry_read_after_next_check"
	case "canceled":
		return "retry_read_when_ready"
	case "invalid_input":
		return "fix_config"
	case "browser_unavailable":
		return "install_browser_runtime"
	case "storage_unavailable":
		return "check_private_storage"
	default:
		return ""
	}
}

func ParseChannels(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
