package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// ReplaceUnusedOPPOAuthorization is deliberately separate from Submit/Resume.
// It never retries upload or submit, and never claims the old allocation failed.
func (e *Engine) ReplaceUnusedOPPOAuthorization(ctx context.Context, id, expected string) error {
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
	op, err := e.Store.CheckOPPOAuthorizationReplacement(t.ID, expected)
	if err != nil {
		return err
	}
	check, ok := e.Adapter.(protocol.UnusedOPPOAuthorization)
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
	if remote.Scope != baseline.Scope || remote.AdapterBinding != baseline.AdapterBinding || remote.ActiveIntent != "" || protocol.Digest(remote.Metadata) != protocol.Digest(baseline.Metadata) {
		return fmt.Errorf("remote preflight changed")
	}
	if err = check.CheckUnusedOPPOAuthorization(ctx, op.Request); err != nil {
		return err
	}
	q, err := e.Store.BeginOPPOAuthorizationReplacement(t.ID, expected)
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
		if err = e.Store.Complete(q, effect, "", "authorization replacement failed; inspect private response evidence"); err != nil {
			return err
		}
		return ErrUnknown
	}
	if err = e.Store.Complete(q, receipt.Effect, receipt.RemoteRef, ""); err != nil {
		return err
	}
	if receipt.Effect != "confirmed_applied" {
		return ErrUnknown
	}
	return nil
}
