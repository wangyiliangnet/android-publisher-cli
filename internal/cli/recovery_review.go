package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
)

func runRecoveryReview(mode, root string, args []string, out, stderr io.Writer) int {
	f := flags("state "+args[0], stderr)
	file := f.String("file", "", "private human recovery review JSON")
	approved := f.String("approve-review", "", "digest of the exact reviewed recovery case")
	bad := func(err error) int { return failure(out, mode, "review_recovery_inventory", 3, err) }
	if err := parse(f, args[1:]); err != nil {
		return bad(err)
	}
	if *file == "" || (args[0] != "recovery-close" && *approved != "") {
		return bad(fmt.Errorf("invalid recovery review flags"))
	}
	if mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return bad(fmt.Errorf("production recovery review requires the canonical state directory"))
	}
	s, err := state.Open(root, mode, args[0] != "recovery-close")
	if err != nil {
		return bad(err)
	}
	defer s.Close()
	if args[0] == "recovery-template" {
		r, err := s.RecoveryTemplate()
		if err != nil {
			return bad(err)
		}
		data, _ := json.MarshalIndent(r, "", "  ")
		if err = writeSame(*file, data); err != nil {
			return bad(err)
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "recovery_required": true, "template": *file, "next_action": "review_complete_account_history"})
	}
	data, err := privatefs.Read(*file, 256<<10)
	var r state.RecoveryReview
	if err != nil || strictjson.Decode(data, &r) != nil {
		return bad(fmt.Errorf("invalid private recovery review JSON"))
	}
	if args[0] == "recovery-review" {
		digest, err := s.ValidateRecoveryReview(r)
		if err != nil {
			return bad(err)
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "effect": "read_only", "recovery_required": true, "approval_digest": digest, "applications": len(r.Applications), "evidence_source": "human_attestation", "next_action": "approve_exact_recovery_review"})
	}
	if err = s.ApplyRecoveryReview(r, *approved); err != nil {
		return bad(err)
	}
	return encodeCode(out, map[string]any{"schema_version": 1, "effect": "local_recovery_closure", "recovery_required": false, "version_floors_enforced": true, "next_action": "prepare_newer_release_and_preflight"})
}
