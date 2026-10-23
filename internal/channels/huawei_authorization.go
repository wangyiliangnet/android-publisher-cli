package channels

import (
	"context"
	"reflect"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// CheckExpiredUnusedHuaweiAuthorization proves only that the exact, already
// acknowledged private allocation has expired. Eligibility of every business
// operation and the durable renewal history is checked independently by state.
// No credential resolution, token refresh, upload or allocation occurs here.
func (p *PreparedProduction) CheckExpiredUnusedHuaweiAuthorization(ctx context.Context, q protocol.Request) error {
	if !p.enabled || !p.writable || q.Channel != "huawei" || q.Kind != "authorize" || q.AuthorizationGeneration >= 2 {
		return fail("huawei_authorization_renewal_outside_scope", "not_sent")
	}
	if _, err := p.validateRequest(q); err != nil {
		return err
	}
	return p.runtime.Manager.WithAccount(ctx, p.target.Account, func(session *credentials.Session) error {
		m := keychainMaterials{vault: p.runtime.Manager.Vault, scope: p.target.Account, lease: session.Lease()}
		proof, err := p.receiptProof(ctx, m, q.Kind, q.AuthorizationGeneration)
		if err != nil {
			return err
		}
		ref := "material-" + account.Key(q.MaterialOperationKey())
		if proof == nil || !reflect.DeepEqual(proof.Request, q) || proof.Receipt.RemoteRef != ref {
			return fail("huawei_authorization_receipt_not_verified", "not_sent")
		}
		grant, err := loadGrant(ctx, m, ref)
		if err != nil {
			return err
		}
		if grant.ExpiresAt.IsZero() || time.Now().UTC().Before(grant.ExpiresAt) {
			return fail("huawei_upload_authorization_not_expired", "not_sent")
		}
		if err = validateHuaweiGrant(grant, p.target.APK, grant.ExpiresAt.Add(-time.Nanosecond)); err != nil {
			return fail("huawei_expired_authorization_invalid", "not_sent")
		}
		return nil
	})
}

var _ protocol.ExpiredUnusedHuaweiAuthorization = (*PreparedProduction)(nil)
