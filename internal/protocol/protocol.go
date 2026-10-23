// Package protocol describes one explicit business operation, not vendor endpoints.
package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
)

type Request struct {
	OperationID             string            `json:"operation_id"`
	TargetID                string            `json:"target_id"`
	Scope                   string            `json:"scope"`
	Channel                 string            `json:"channel"`
	IntentDigest            string            `json:"intent_digest"`
	Kind                    string            `json:"kind"`
	ArtifactSHA256          string            `json:"artifact_sha256"`
	PackageName             string            `json:"package_name"`
	VersionCode             int64             `json:"version_code"`
	VersionName             string            `json:"version_name"`
	PreviousRef             string            `json:"previous_ref,omitempty"`
	Checkpoints             map[string]string `json:"checkpoints,omitempty"`
	SnapshotDigest          string            `json:"snapshot_digest"`
	AdapterBinding          string            `json:"adapter_binding,omitempty"`
	Metadata                map[string]string `json:"metadata"`
	Policy                  manifest.Policy   `json:"release_policy"`
	AuthorizationGeneration int               `json:"authorization_generation,omitempty"`
}
type Receipt struct {
	OperationID   string `json:"operation_id"`
	TargetID      string `json:"target_id"`
	Scope         string `json:"scope"`
	Kind          string `json:"kind"`
	RequestDigest string `json:"request_digest"`
	Effect        string `json:"effect"`
	RemoteRef     string `json:"remote_ref"`
}
type RemoteSnapshot struct {
	Scope          string            `json:"scope"`
	AdapterBinding string            `json:"adapter_binding,omitempty"`
	Metadata       map[string]string `json:"metadata"`
	ActiveIntent   string            `json:"active_intent"`
}
type Observation struct {
	Scope          string          `json:"scope"`
	AdapterBinding string          `json:"adapter_binding,omitempty"`
	VersionCode    int64           `json:"version_code"`
	RemoteRef      string          `json:"remote_ref"`
	Review         string          `json:"review_state"`
	Availability   string          `json:"availability_state"`
	Source         string          `json:"source"`
	CollectedAt    time.Time       `json:"collected_at"`
	Evidence       []Evidence      `json:"evidence,omitempty"`
	Context        *ChannelContext `json:"channel_context,omitempty"`
}
type Evidence struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Reconciliation struct {
	OperationID   string   `json:"operation_id"`
	RequestDigest string   `json:"request_digest"`
	Result        string   `json:"result"`
	Receipt       *Receipt `json:"receipt,omitempty"`
}
type Adapter interface {
	Inspect(context.Context, string) (RemoteSnapshot, error)
	ExecuteOne(context.Context, Request) (Receipt, error)
	Reconcile(context.Context, Request) (Reconciliation, error)
	Observe(context.Context, string, int64, string) (Observation, error)
}

// Planner declares a static vendor-specific sequence. Older kernel simulation
// adapters retain their original three-step fixture contract.
type Planner interface {
	BuildPlan(Request) ([]string, error)
}

// PreflightAdvancer derives expected own remote changes from a confirmed
// operation. It is pure: no HTTP and no mutation of the frozen preflight.
type PreflightAdvancer interface {
	ExpectedSnapshot(Request, RemoteSnapshot) (RemoteSnapshot, error)
}
type WriteOutcome interface{ WriteEffect() string }

// UnusedOPPOAuthorization checks local private checkpoints without allocating a
// grant. It is only used by the explicit, bounded OPPO configuration replacement.
type UnusedOPPOAuthorization interface {
	CheckUnusedOPPOAuthorization(context.Context, Request) error
}

// ExpiredUnusedHuaweiAuthorization verifies an already applied allocation's
// private expiry and receipt. It never allocates or uploads during the check.
type ExpiredUnusedHuaweiAuthorization interface {
	CheckExpiredUnusedHuaweiAuthorization(context.Context, Request) error
}
type BindingProvider interface{ AdapterBinding(string) (string, error) }

// ProductionBindingProvider opts a reviewed adapter into one exact frozen
// production scope. The boolean grants business writes, not reads/preflight.
type ProductionBindingProvider interface {
	ProductionScope() (scope, intent string, writable bool)
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (r Receipt) Validate(q Request) error {
	if r.OperationID != q.OperationID || r.TargetID != q.TargetID || r.Scope != q.Scope || r.Kind != q.Kind || r.RequestDigest != Digest(q) {
		return fmt.Errorf("receipt does not match operation identity")
	}
	if !slices.Contains([]string{"confirmed_applied", "confirmed_rejected"}, r.Effect) {
		return fmt.Errorf("missing or unknown receipt effect")
	}
	if r.Effect == "confirmed_applied" && r.RemoteRef == "" {
		return fmt.Errorf("receipt has no remote reference")
	}
	return nil
}
func (o Observation) Validate(scope string, version int64, ref string) error {
	if err := o.Context.Validate(); err != nil {
		return err
	}
	if o.Context != nil && (o.Review != "unknown" || o.Availability != "unknown") {
		return fmt.Errorf("channel context cannot assert target states")
	}
	if o.Scope != scope || o.VersionCode != version || ref == "" || o.RemoteRef != ref || o.Source == "" || o.CollectedAt.IsZero() {
		return fmt.Errorf("observation lacks exact target evidence")
	}
	if !slices.Contains([]string{"unknown", "pending", "in_review", "rejected", "approved", "withdrawn"}, o.Review) || !slices.Contains([]string{"unknown", "not_observed_live", "scheduled", "live", "superseded"}, o.Availability) {
		return fmt.Errorf("unknown observation enum")
	}
	if o.Availability == "live" && o.Review != "approved" {
		return fmt.Errorf("inconsistent live evidence")
	}
	return nil
}
