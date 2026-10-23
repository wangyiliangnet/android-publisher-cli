package channels

import (
	"context"
	"errors"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// publicStoreReader is implemented by the production Runtime independently of
// credential access. Synthetic executors opt in explicitly, so fixtures cannot
// accidentally contact a real public store.
type publicStoreReader interface {
	tencentPublicVersion(context.Context, string) (publicStoreVersion, error)
}

var publicStoreNotQueried = errors.New("synthetic public store not configured")

func (r Runtime) tencentPublicVersion(ctx context.Context, pkg string) (publicStoreVersion, error) {
	if r.publicStore != nil {
		return r.publicStore(ctx, pkg)
	}
	if r.sender != nil {
		return publicStoreVersion{}, publicStoreNotQueried
	}
	return newTencentPublicStore(nil).version(ctx, pkg)
}

func (r Runtime) withPublicStoreReader() Runtime {
	if r.sender == nil && r.publicStore == nil {
		r.publicStore = func(ctx context.Context, pkg string) (publicStoreVersion, error) {
			return newTencentPublicStore(nil).version(ctx, pkg)
		}
	}
	return r
}

// TencentPublicContext can enrich status output when constructing a credentialed
// adapter was impossible. It is strictly package-scoped public information; the
// caller must keep target states unknown and separately report any API failure.
func TencentPublicContext(ctx context.Context, pkg string) (*protocol.ChannelContext, error) {
	return tencentPublicContext(ctx, pkg, newTencentPublicStore(nil).version)
}

func tencentPublicContext(ctx context.Context, pkg string, read func(context.Context, string) (publicStoreVersion, error)) (*protocol.ChannelContext, error) {
	if tencentStoreURL(pkg) == "" {
		return nil, fail("invalid_public_store_request", "not_sent")
	}
	details := &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "not_queried", StoreQueryHealth: "failed", SourceURL: tencentStoreURL(pkg)}
	if v, err := read(ctx, pkg); err == nil {
		details.StoreQueryHealth = "healthy"
		details.StoreVersionCode = v.Code
		details.StoreVersionName = v.Name
	}
	if ctx.Err() != nil {
		return nil, fail("observation_cancelled", "not_sent")
	}
	return details, details.Validate()
}

// observeAutomaticContext never assigns target review/availability. The
// immutable target identifies the question, while Context explicitly describes
// unassociated channel facts, inference, and independent source health.
func (a *Adapter) observeAutomaticContext(ctx context.Context, t Target, ref string) (protocol.Observation, error) {
	contextInfo := &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "failed", StoreQueryHealth: "not_supported"}
	at := time.Now().UTC()
	if t.Account.Channel == "tencent" {
		contextInfo.StoreQueryHealth = "not_queried"
	}
	apiErr := a.runtime.WithClient(ctx, t, func(c *Client, _ Materials) error {
		at = c.now()
		if t.Account.Channel == "xiaomi" {
			d, err := c.XiaomiDetails(ctx, t.PackageName)
			if err != nil {
				return err
			}
			contextInfo.APIQueryHealth = "healthy"
			contextInfo.APIVersionCode = d.VersionCode
			if protocol.ValidContextVersion(d.VersionName) {
				contextInfo.APIVersionName = d.VersionName
			}
			contextInfo.InferredReview = "unknown"
			contextInfo.InferenceBasis = "submission_not_confirmed"
			accepted := manifest.ValidHash(t.IntentDigest) && ref == ack(protocol.Digest([]string{t.IntentDigest, "xiaomi", "push"})).Ref
			if accepted {
				switch {
				case d.VersionCode < t.VersionCode:
					contextInfo.InferredReview = "in_review"
					contextInfo.InferenceBasis = "xiaomi_api_version_before_target"
				case d.VersionCode == t.VersionCode:
					contextInfo.InferredReview = "approved"
					contextInfo.InferenceBasis = "xiaomi_api_version_equals_target"
				default:
					contextInfo.InferenceBasis = "xiaomi_api_version_after_target"
				}
			}
			return nil
		}
		s, err := c.TencentAudit(ctx, t.PackageName, t.AppID)
		if err == nil {
			contextInfo = s.Context
		}
		return err
	})
	if apiErr != nil {
		// Even a late account-session failure must not masquerade as a healthy
		// API query. Retain a successful public page query below independently.
		contextInfo = &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "failed", StoreQueryHealth: contextInfo.StoreQueryHealth}
	}
	if t.Account.Channel == "tencent" {
		if reader, ok := a.runtime.(publicStoreReader); ok {
			contextInfo.SourceURL = tencentStoreURL(t.PackageName)
			contextInfo.StoreQueryHealth = "failed"
			if v, err := reader.tencentPublicVersion(ctx, t.PackageName); err == nil {
				contextInfo.StoreQueryHealth = "healthy"
				contextInfo.StoreVersionCode = v.Code
				contextInfo.StoreVersionName = v.Name
			} else if errors.Is(err, publicStoreNotQueried) {
				contextInfo.StoreQueryHealth = "not_queried"
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return protocol.Observation{}, fail("observation_cancelled", "not_sent")
	}
	if err := contextInfo.Validate(); err != nil {
		return protocol.Observation{}, fail("invalid_channel_context", "not_sent")
	}
	return protocol.Observation{Scope: t.Scope, AdapterBinding: adapterBinding(t), VersionCode: t.VersionCode, RemoteRef: ref, Review: "unknown", Availability: "unknown", Source: t.Account.Channel + "_automatic_context", CollectedAt: at, Context: contextInfo}, nil
}
