package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
)

// batchApproval contains the public publishing meaning and local binding
// digests. Private baseline metadata and credential values never enter it.
type batchApproval struct {
	SchemaVersion int                  `json:"schema_version"`
	BatchID       string               `json:"batch_id"`
	Mode          string               `json:"mode"`
	BatchDigest   string               `json:"batch_digest"`
	Channels      []string             `json:"selected_channels"`
	Entries       []batchApprovalEntry `json:"entries"`
	PlanDigest    string               `json:"approval_plan"`
}

type batchApprovalEntry struct {
	ReleaseID        string          `json:"release_id"`
	Channel          string          `json:"channel"`
	AccountRef       string          `json:"account_ref"`
	ApplicationRef   string          `json:"application_ref"`
	ClientID         string          `json:"client_id,omitempty"`
	ChannelAppID     string          `json:"channel_app_id,omitempty"`
	PackageName      string          `json:"package_name"`
	VersionName      string          `json:"version_name"`
	VersionCode      int64           `json:"version_code"`
	IntentDigest     string          `json:"intent_digest"`
	ArtifactSHA256   string          `json:"apk_sha256"`
	SignerSHA256     string          `json:"signer_sha256"`
	Notes            string          `json:"release_notes"`
	PreviousNotes    *string         `json:"previous_release_notes,omitempty"`
	Policy           manifest.Policy `json:"release_policy"`
	ConfigSHA256     string          `json:"account_config_sha256,omitempty"`
	IconSHA256       string          `json:"icon_sha256,omitempty"`
	PreflightSHA256  string          `json:"saved_preflight_sha256,omitempty"`
	PreflightDigest  string          `json:"approval_preflight,omitempty"`
	ProjectionDigest string          `json:"preflight_snapshot_digest,omitempty"`
}

func (a batchApproval) digest() string {
	a.PlanDigest = ""
	return protocol.Digest(a)
}

// batchSelected preserves immutable batch order, independently of flag order.
// In particular, an empty component is never interpreted as "all channels".
func batchSelected(b batchFile, subset []string) ([]string, error) {
	if len(subset) == 0 {
		for _, e := range b.Entries {
			subset = append(subset, e.Channel)
		}
	}
	seen := map[string]bool{}
	for _, channel := range subset {
		if !slices.Contains(manifest.Channels, channel) || seen[channel] {
			return nil, fmt.Errorf("invalid or duplicate batch channel")
		}
		seen[channel] = true
	}
	selected := []string{}
	for _, e := range b.Entries {
		if seen[e.Channel] {
			selected = append(selected, e.Channel)
			delete(seen, e.Channel)
		}
	}
	if len(seen) != 0 {
		return nil, fmt.Errorf("selected channel is outside the immutable batch")
	}
	return selected, nil
}

func parseBatchChannels(value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	seen := map[string]bool{}
	for _, p := range parts {
		if !slices.Contains(manifest.Channels, p) || seen[p] {
			return nil, fmt.Errorf("invalid or duplicate batch channel")
		}
		seen[p] = true
	}
	return parts, nil
}

func batchBinding(root, mode string, entry batchEntry, snapshot manifest.Snapshot, target state.Target) (string, string, error) {
	if target.Preflight == "" {
		return "", "", fmt.Errorf("selected target requires a completed preflight")
	}
	var preflight protocol.RemoteSnapshot
	if strictjson.Decode([]byte(target.Preflight), &preflight) != nil || preflight.Scope != target.Scope || preflight.Metadata == nil {
		return "", "", fmt.Errorf("invalid saved preflight")
	}
	if mode == manifest.Simulation {
		return protocol.Digest(preflight), protocol.Digest([]string{target.Preflight}), nil
	}
	data, err := privatefs.Read(filepath.Join(root, "production", entry.Channel, entry.ReleaseID+".json"), 8192)
	if err != nil {
		return "", "", fmt.Errorf("selected target requires a saved production preflight")
	}
	defer clear(data)
	var digest string
	if entry.Channel == "oppo" {
		var binding oppoBinding
		if strictjson.Decode(data, &binding) != nil || binding.SchemaVersion != 1 || binding.IntentDigest != snapshot.Digest || binding.Scope != target.Scope || !credentials.ValidCredentialName(binding.CredentialName) || (entry.CredentialName != "" && entry.CredentialName != binding.CredentialName) || !strings.HasPrefix(binding.BaselineRef, "material-") || !manifest.ValidHash(strings.TrimPrefix(binding.BaselineRef, "material-")) {
			return "", "", fmt.Errorf("invalid saved production preflight")
		}
		digest = binding.AdapterBinding
	} else {
		var binding fiveBinding
		if decodeFiveConfiguration(data, &binding) != nil || binding.SchemaVersion != 1 || binding.IntentDigest != snapshot.Digest || binding.Scope != target.Scope || !strings.HasPrefix(binding.BaselineRef, "material-") || !manifest.ValidHash(strings.TrimPrefix(binding.BaselineRef, "material-")) {
			return "", "", fmt.Errorf("invalid saved production preflight")
		}
		digest = binding.AdapterBinding
	}
	if !manifest.ValidHash(digest) || preflight.AdapterBinding != digest {
		return "", "", fmt.Errorf("saved preflight does not match its adapter binding")
	}
	return digest, protocol.Digest(data), nil
}

// inspectBatchApproval performs only local reads. Every artifact and selected
// preflight is verified before the caller is allowed to dispatch any channel.
func inspectBatchApproval(ctx context.Context, root, mode string, b batchFile, subset []string, inspector artifact.Inspector) (batchApproval, error) {
	a := batchApproval{SchemaVersion: 1, BatchID: b.ID, Mode: mode, BatchDigest: protocol.Digest(b), Entries: []batchApprovalEntry{}}
	if mode != manifest.Production && mode != manifest.Simulation {
		return a, fmt.Errorf("invalid batch mode")
	}
	selected, err := batchSelected(b, subset)
	if err != nil {
		return a, err
	}
	a.Channels = selected
	saved, err := readBatch(batchPath(root, b.ID))
	if err != nil || protocol.Digest(saved) != a.BatchDigest {
		return a, fmt.Errorf("saved batch changed or is unavailable")
	}
	s, err := state.Open(root, mode, true)
	if err != nil {
		return a, err
	}
	defer s.Close()
	for _, e := range b.Entries {
		if ctx.Err() != nil {
			return a, ctx.Err()
		}
		snapshot, err := s.Load(e.ReleaseID)
		if err != nil || snapshot.Digest != e.IntentDigest || len(snapshot.Manifest.Targets) != 1 {
			return a, fmt.Errorf("batch differs from its frozen releases")
		}
		m := snapshot.Manifest
		t, ok := m.Targets[e.Channel]
		if !ok || m.ReleasePolicy.Mode != "after_approval" || m.ReleasePolicy.At != "" || (mode == manifest.Production && len(m.Metadata) != 0) {
			return a, fmt.Errorf("unsupported batch publishing scope")
		}
		if artifact.VerifyFrozen(ctx, root, snapshot, inspector) != nil {
			return a, fmt.Errorf("frozen batch APK verification failed")
		}
		var config channels.PreparationConfig
		var iconSHA256 string
		if mode == manifest.Production && e.Channel != "oppo" {
			data, err := privatefs.Read(e.AccountConfig, 65536)
			if err != nil {
				return a, fmt.Errorf("frozen account configuration is unavailable")
			}
			err = decodeFiveConfiguration(data, &config)
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			clear(data)
			if err != nil || !manifest.ValidHash(e.ConfigSHA256) || digest != e.ConfigSHA256 || config.Channel != e.Channel || config.Account != t.AccountRef || config.PackageName != m.PackageName || !credentials.ValidCredentialName(config.SecretName) {
				return a, fmt.Errorf("frozen account configuration changed")
			}
			if e.Channel == "xiaomi" {
				iconSHA256, _, err = privatefs.Hash(config.IconPath)
				if err != nil {
					return a, fmt.Errorf("frozen Xiaomi icon is unavailable")
				}
			}
		}
		apk := m.Artifacts[t.Artifact]
		preview := batchApprovalEntry{ReleaseID: e.ReleaseID, Channel: e.Channel, AccountRef: t.AccountRef, ApplicationRef: t.AppRef, ClientID: config.ClientID, ChannelAppID: config.AppID, PackageName: m.PackageName, VersionName: m.VersionName, VersionCode: m.VersionCode, IntentDigest: snapshot.Digest, ArtifactSHA256: apk.SHA256, SignerSHA256: apk.SignerSHA256, Notes: snapshot.Notes, Policy: m.ReleasePolicy, ConfigSHA256: e.ConfigSHA256, IconSHA256: iconSHA256}
		if slices.Contains(selected, e.Channel) {
			targets, err := s.Targets(e.ReleaseID)
			if err != nil || len(targets) != 1 || targets[0].Channel != e.Channel {
				return a, fmt.Errorf("invalid frozen batch target inventory")
			}
			preview.PreflightDigest, preview.PreflightSHA256, err = batchBinding(root, mode, e, snapshot, targets[0])
			if err != nil {
				return a, err
			}
			preview.ProjectionDigest = protocol.Digest([]string{targets[0].Preflight})
			var before protocol.RemoteSnapshot
			if json.Unmarshal([]byte(targets[0].Preflight), &before) == nil {
				if notes, known := before.Metadata["release_notes"]; known {
					preview.PreviousNotes = &notes
				}
			}
		}
		a.Entries = append(a.Entries, preview)
	}
	if ctx.Err() != nil {
		return a, ctx.Err()
	}
	a.PlanDigest = a.digest()
	return a, nil
}

func reviewBatch(ctx context.Context, root, mode string, b batchFile, subset []string, file string, inspector artifact.Inspector) (batchApproval, error) {
	if !filepath.IsAbs(file) {
		return batchApproval{}, fmt.Errorf("approval file must be an absolute path")
	}
	a, err := inspectBatchApproval(ctx, root, mode, b, subset, inspector)
	if err != nil {
		return a, err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return a, err
	}
	if len(data) > 8<<20 {
		return a, fmt.Errorf("batch approval exceeds the 8 MiB limit")
	}
	if old, readErr := privatefs.Read(file, 8<<20); readErr == nil {
		if bytes.Equal(old, data) {
			return a, nil
		}
		return a, fmt.Errorf("approval file already differs; choose a new file")
	} else if !os.IsNotExist(readErr) {
		return a, fmt.Errorf("approval file cannot be read")
	}
	if err = writeSame(file, data); err != nil {
		return a, fmt.Errorf("approval file already differs or cannot be written; choose a new file")
	}
	return a, nil
}

func writeBatchReview(out io.Writer, a batchApproval) error {
	if _, err := fmt.Fprintf(out, "批次 %s：%d 个渠道，本次选择 %s。\n", a.BatchID, len(a.Entries), strings.Join(a.Channels, ", ")); err != nil {
		return err
	}
	for _, e := range a.Entries {
		if !slices.Contains(a.Channels, e.Channel) {
			continue
		}
		label := map[string]string{"oppo": "OPPO", "vivo": "vivo", "xiaomi": "小米", "huawei": "华为", "honor": "荣耀", "tencent": "应用宝"}[e.Channel]
		if _, err := fmt.Fprintf(out, "%s · %s · %s(%d) · 审核通过后立即发布\n账号：%s · 应用：%s\nAPK SHA-256：%s\n", label, e.PackageName, safeBatchPreview(e.VersionName), e.VersionCode, safeBatchPreview(e.AccountRef), safeBatchPreview(e.ApplicationRef), e.ArtifactSHA256); err != nil {
			return err
		}
		if e.PreviousNotes == nil {
			if _, err := fmt.Fprintln(out, "原更新说明：本地公开预检未提供。"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(out, "原更新说明：\n%s\n", safeBatchPreview(*e.PreviousNotes)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "本次更新说明：\n%s\n", safeBatchPreview(e.Notes)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(out, "批准摘要：%s\n此步骤只生成审阅文件；提交时需明确提供 --approve-plan。\n", a.PlanDigest)
	return err
}

func safeBatchPreview(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '�'
		}
		return r
	}, value)
}

type batchExecutionRunner func(context.Context, string, batchEntry, string, string, io.Writer) int

type batchExecutionRow struct {
	ReleaseID  string `json:"release_id"`
	Channel    string `json:"channel"`
	Action     string `json:"action"`
	ExitCode   int    `json:"exit_code"`
	NextAction string `json:"next_action,omitempty"`
}

type batchExecutionResult struct {
	batchResult
	Command    string              `json:"command"`
	PlanDigest string              `json:"approval_plan,omitempty"`
	Selected   []string            `json:"selected_channels"`
	Results    []batchExecutionRow `json:"execution_results"`
	Error      string              `json:"error,omitempty"`
	NextAction string              `json:"next_action,omitempty"`
}

func batchExitCode(current, next int) int {
	if next == 130 {
		return 130
	}
	if current == 4 || next == 4 {
		return 4
	}
	if current == 5 || next == 5 {
		return 5
	}
	if current != 0 || next != 0 {
		return 3
	}
	return 0
}

func executeBatch(ctx context.Context, command, root, mode string, b batchFile, approvalFile, approvedPlan string, subset []string, inspector artifact.Inspector, run batchExecutionRunner) (batchExecutionResult, int) {
	r := batchExecutionResult{batchResult: batchResult{SchemaVersion: 1, ID: b.ID, Mode: mode, Effect: "not_sent", Targets: []batchRow{}}, Command: command, Selected: []string{}, Results: []batchExecutionRow{}}
	invalid := func() (batchExecutionResult, int) {
		r.batchResult = batchResult{SchemaVersion: 1, ID: b.ID, Mode: mode, Effect: "not_sent", Targets: []batchRow{}}
		r.Error, r.NextAction = "batch_approval_invalid_or_stale", "review_batch_again"
		return r, 2
	}
	if !slices.Contains([]string{"submit", "resume", "reconcile"}, command) || run == nil {
		return invalid()
	}
	var approval batchApproval
	var err error
	if command == "reconcile" {
		if approvalFile != "" || approvedPlan != "" {
			return invalid()
		}
		r.Selected, err = batchSelected(b, subset)
		if err != nil {
			return invalid()
		}
		// Reconciliation needs the same immutable inputs but grants no writes.
		approval, err = inspectBatchApproval(ctx, root, mode, b, r.Selected, inspector)
	} else {
		if !filepath.IsAbs(approvalFile) || !manifest.ValidHash(approvedPlan) {
			return invalid()
		}
		data, readErr := privatefs.Read(approvalFile, 8<<20)
		if readErr != nil || strictjson.Decode(data, &approval) != nil || approval.SchemaVersion != 1 || approval.PlanDigest != approvedPlan || approval.digest() != approvedPlan || len(approval.Channels) == 0 {
			return invalid()
		}
		if len(subset) == 0 {
			subset = approval.Channels
		}
		r.Selected, err = batchSelected(b, subset)
		if err != nil || !slices.Equal(r.Selected, approval.Channels) {
			return invalid()
		}
		current, inspectErr := inspectBatchApproval(ctx, root, mode, b, r.Selected, inspector)
		if ctx.Err() != nil {
			r.NextAction = "inspect_saved_checkpoints"
			return r, 130
		}
		if inspectErr != nil || current.PlanDigest != approvedPlan {
			return invalid()
		}
		r.PlanDigest = approvedPlan
	}
	if err != nil {
		if ctx.Err() != nil {
			r.NextAction = "inspect_saved_checkpoints"
			return r, 130
		}
		return invalid()
	}
	code := 0
	for _, entry := range b.Entries {
		if !slices.Contains(r.Selected, entry.Channel) {
			continue
		}
		if ctx.Err() != nil {
			code = 130
			break
		}
		row := batchExecutionRow{ReleaseID: entry.ReleaseID, Channel: entry.Channel, Action: command}
		s, openErr := state.Open(root, mode, true)
		if openErr != nil {
			row.ExitCode, row.NextAction = 5, "retry_local_state"
		} else {
			targets, targetErr := s.Targets(entry.ReleaseID)
			if targetErr != nil || len(targets) != 1 {
				row.ExitCode, row.NextAction = 5, "retry_local_state"
			} else if command != "reconcile" {
				if targets[0].Phase == "submitted" {
					row.Action = "already_accepted"
				} else if err := s.CheckWritable(targets[0].Scope, targets[0].ID); err != nil {
					row.ExitCode, row.NextAction = 3, "reconcile_before_resume"
				}
			}
			s.Close()
		}
		if row.ExitCode == 0 && row.Action != "already_accepted" {
			var preflight string
			for _, e := range approval.Entries {
				if e.Channel == entry.Channel {
					preflight = e.PreflightDigest
				}
			}
			var output bytes.Buffer
			intent := entry.IntentDigest
			if command == "reconcile" {
				intent, preflight = "", ""
			}
			row.ExitCode = run(ctx, command, entry, intent, preflight, &output)
			// Output is intentionally not copied: production helpers can include
			// diagnostics. The batch reports only persisted, typed state below.
			if row.ExitCode != 0 {
				row.NextAction = "inspect_channel_status_and_reconcile"
			}
		}
		r.Results = append(r.Results, row)
		code = batchExitCode(code, row.ExitCode)
		if row.ExitCode == 130 {
			break
		}
	}
	// Cancellation must not erase checkpoints already recorded by earlier
	// channels. A local cached read uses an independent context and no network.
	result, statusCode := collectBatch(context.Background(), root, mode, b, false, nil)
	r.batchResult = result
	if command != "reconcile" {
		r.Effect = "explicit_business_writes"
	}
	if code != 130 {
		code = batchExitCode(code, statusCode)
	}
	return r, code
}
