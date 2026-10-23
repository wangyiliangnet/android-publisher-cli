package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func runBatchAction(ctx context.Context, mode, root string, args []string, inspector artifact.Inspector, out, stderr io.Writer) int {
	command := args[0]
	f := flags("batch "+command, stderr)
	id := f.String("batch", "", "saved batch ID")
	channels := f.String("channels", "", "explicit channel subset")
	var file, approval, approved, endpoint string
	if command == "review" {
		f.StringVar(&file, "file", "", "absolute new immutable approval file")
	} else if command != "reconcile" {
		f.StringVar(&approval, "approval", "", "reviewed immutable approval file")
		f.StringVar(&approved, "approve-plan", "", "exact reviewed approval plan digest")
	}
	if mode == manifest.Simulation && command != "review" {
		f.StringVar(&endpoint, "store", "", "loopback fake-store URL")
	}
	bad := func(err error) int { return failure(out, mode, "review_batch", 2, err) }
	if err := parse(f, args[1:]); err != nil {
		return bad(err)
	}
	if !manifest.ValidID(*id) || (mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode)) {
		return bad(fmt.Errorf("batch requires a valid saved ID and the canonical production directory"))
	}
	subset, err := parseBatchChannels(*channels)
	if err != nil {
		return bad(err)
	}
	b, err := readBatch(batchPath(root, *id))
	if err != nil || b.ID != *id {
		return bad(fmt.Errorf("saved batch is unavailable"))
	}
	if mode == manifest.Simulation {
		inspector = artifact.SimulationInspector{}
	}
	if command == "review" {
		a, err := reviewBatch(ctx, root, mode, b, subset, file, inspector)
		if err != nil {
			return bad(err)
		}
		if err := writeBatchReview(stderr, a); err != nil {
			return 5
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "mode": mode, "effect": "offline", "batch_id": b.ID, "approval_file": file, "approval_plan": a.PlanDigest, "selected_channels": a.Channels, "next_action": "review_file_then_explicitly_approve"})
	}
	var fake *simulation.Client
	if mode == manifest.Simulation {
		fake, err = simulation.NewClient(endpoint)
		if err != nil {
			return bad(err)
		}
	}
	run := func(ctx context.Context, command string, entry batchEntry, intent, preflight string, w io.Writer) int {
		if mode == manifest.Simulation {
			s, err := state.Open(root, mode, false)
			if err != nil {
				return failure(w, mode, "retry_local_state", 3, err)
			}
			defer s.Close()
			e := engine.Engine{Store: s, Inspector: inspector, Adapter: fake}
			var result engine.Summary
			if command == "reconcile" {
				result, err = e.Reconcile(ctx, entry.ReleaseID, []string{entry.Channel})
			} else {
				result, err = e.Submit(ctx, entry.ReleaseID, []string{entry.Channel})
			}
			if err != nil {
				return failure(w, mode, "reconcile_before_resume", 3, err)
			}
			if err := encode(w, result); err != nil {
				return 5
			}
			if len(result.Errors) != 0 {
				return 3
			}
			return 0
		}
		if entry.Channel == "oppo" {
			return runOPPOProduction(ctx, command, root, entry.ReleaseID, entry.CredentialName, intent, preflight, false, nil, inspector, w)
		}
		return runFiveProduction(ctx, command, root, entry.ReleaseID, entry.AccountConfig, intent, preflight, false, nil, inspector, w)
	}
	result, code := executeBatch(ctx, command, root, mode, b, approval, approved, subset, inspector, run)
	if encode(out, result) != nil {
		return 5
	}
	return code
}
