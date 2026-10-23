package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// RenewExpiredHuaweiAuthorization only replaces a confirmed, expired, unused
// upload grant. It is a separate action from Submit and never replays an upload.
func (e *Engine) RenewExpiredHuaweiAuthorization(ctx context.Context, id, expected string) error {
	release, err := e.Store.BeginCommand()
	if err != nil {
		return err
	}
	defer release()
	if err = e.execution(id, true); err != nil {
		return err
	}
	targets, err := e.Store.Targets(id)
	if err != nil || len(targets) != 1 {
		return ErrInvalidInput
	}
	t := targets[0]
	op, err := e.Store.CheckHuaweiAuthorizationRenewal(t.ID, expected)
	if err != nil {
		return err
	}
	check, ok := e.Adapter.(protocol.ExpiredUnusedHuaweiAuthorization)
	if !ok {
		return ErrInvalidInput
	}
	snapshot, err := e.Store.Load(id)
	if err != nil {
		return err
	}
	if err = artifact.VerifyFrozen(ctx, e.Store.Root, snapshot, e.Inspector); err != nil {
		return err
	}
	var baseline protocol.RemoteSnapshot
	if json.Unmarshal([]byte(t.Preflight), &baseline) != nil {
		return ErrInvalidInput
	}
	remote, err := e.Adapter.Inspect(ctx, t.Scope)
	if err != nil {
		return err
	}
	if remote.Scope != baseline.Scope || remote.AdapterBinding != baseline.AdapterBinding || remote.ActiveIntent != "" || remote.Metadata == nil || protocol.Digest(remote.Metadata) != protocol.Digest(baseline.Metadata) {
		return fmt.Errorf("remote preflight changed")
	}
	if err = check.CheckExpiredUnusedHuaweiAuthorization(ctx, op.Request); err != nil {
		return err
	}
	if err = e.hook("before_intent", op.Request); err != nil {
		return err
	}
	q, err := e.Store.BeginHuaweiAuthorizationRenewal(t.ID, expected)
	if err != nil {
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
		if err = e.Store.Complete(q, effect, "", "authorization renewal failed; inspect private response evidence"); err != nil {
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
		return ErrUnknown
	}
	return nil
}
