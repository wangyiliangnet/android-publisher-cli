package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type releaseReport struct {
	SchemaVersion int               `json:"schema_version"`
	Mode          string            `json:"mode"`
	Effect        string            `json:"effect"`
	GeneratedAt   string            `json:"generated_at"`
	BatchID       string            `json:"batch_id,omitempty"`
	FullScope     bool              `json:"full_scope"`
	Releases      []reportedRelease `json:"releases"`
	historyCounts
}

type reportedRelease struct {
	ReleaseID        string                   `json:"release_id"`
	IntentDigest     string                   `json:"intent_digest"`
	PackageName      string                   `json:"package_name"`
	VersionName      string                   `json:"version_name"`
	VersionCode      int64                    `json:"version_code"`
	CreatedAt        string                   `json:"created_at"`
	ReleasePolicy    manifest.Policy          `json:"release_policy"`
	Notes            string                   `json:"release_notes"`
	RecoveryRequired bool                     `json:"recovery_required"`
	Targets          []reportedTarget         `json:"targets"`
	Operations       []state.HistoryOperation `json:"operations"`
	historyCounts
}

type reportedTarget struct {
	Channel                 string                   `json:"channel"`
	ArtifactSHA256          string                   `json:"artifact_sha256"`
	SignerSHA256            string                   `json:"signer_sha256"`
	ABI                     []string                 `json:"abi"`
	Execution               string                   `json:"execution_phase"`
	Review                  string                   `json:"review_state"`
	Availability            string                   `json:"availability_state"`
	EvidenceDisputed        bool                     `json:"evidence_disputed"`
	QueryHealth             string                   `json:"query_health"`
	QueryFailures           int                      `json:"query_failures"`
	LastSuccessAt           string                   `json:"last_success_at,omitempty"`
	NextCheckAt             string                   `json:"next_check_at,omitempty"`
	ReviewSource            string                   `json:"review_source,omitempty"`
	ReviewObservedAt        string                   `json:"review_observed_at,omitempty"`
	AvailabilitySource      string                   `json:"availability_source,omitempty"`
	AvailabilityObservedAt  string                   `json:"availability_observed_at,omitempty"`
	Context                 *protocol.ChannelContext `json:"channel_context,omitempty"`
	ContextObservedAt       string                   `json:"context_observed_at,omitempty"`
	CachedContext           *protocol.ChannelContext `json:"cached_channel_context,omitempty"`
	CachedContextObservedAt string                   `json:"cached_context_observed_at,omitempty"`
}

func runReport(ctx context.Context, mode, root string, args []string, out, stderr io.Writer) int {
	f := flags("report", stderr)
	batchID := f.String("batch", "", "saved batch ID")
	releaseID := f.String("release", "", "frozen release ID")
	file := f.String("file", "", "new absolute private report file")
	format := f.String("format", "markdown", "markdown or json")
	if err := parse(f, args); err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	bad := func(message string) int { return failure(out, mode, "fix_config", 2, fmt.Errorf("%s", message)) }
	if (*batchID == "") == (*releaseID == "") || !filepath.IsAbs(*file) || *format != "markdown" && *format != "json" || mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return bad("report requires one --batch or --release, a new absolute --file, and markdown or json format")
	}
	ids := []string{*releaseID}
	var batch batchFile
	if *batchID != "" {
		if !manifest.ValidID(*batchID) {
			return bad("invalid report batch")
		}
		var err error
		batch, err = readBatch(batchPath(root, *batchID))
		if err != nil || batch.ID != *batchID {
			return bad("saved report batch is unavailable")
		}
		ids = make([]string, 0, len(batch.Entries))
		for _, entry := range batch.Entries {
			ids = append(ids, entry.ReleaseID)
		}
	} else if !manifest.ValidID(*releaseID) {
		return bad("invalid report release")
	}
	if _, err := os.Lstat(filepath.Join(root, "state.sqlite")); err != nil {
		return bad("saved report releases are unavailable")
	}
	s, err := state.Open(root, mode, true)
	if err != nil {
		return failure(out, mode, "check_local_state", 5, fmt.Errorf("report state is unavailable"))
	}
	records, err := s.ReleaseRecords(ctx, ids)
	s.Close()
	if err != nil {
		return failure(out, mode, "check_local_state", 5, fmt.Errorf("report state could not be read"))
	}
	if *batchID != "" {
		for i, entry := range batch.Entries {
			r := records[i]
			if len(r.Targets) != 1 || r.Targets[0].Channel != entry.Channel || entry.IntentDigest != r.Snapshot.Digest {
				return bad("saved batch differs from its frozen releases")
			}
		}
	}
	r := makeReleaseReport(mode, *batchID, records)
	var data []byte
	if *format == "json" {
		data, err = json.MarshalIndent(r, "", "  ")
		data = append(data, '\n')
	} else {
		data = renderReleaseReport(r)
	}
	if err != nil || writeNewReport(*file, data) != nil {
		return bad("report could not be created; its parent must exist without symlinks and the file must be new")
	}
	return encodeCode(out, struct {
		SchemaVersion int    `json:"schema_version"`
		Mode          string `json:"mode"`
		Effect        string `json:"effect"`
		File          string `json:"file"`
		Format        string `json:"format"`
	}{1, mode, "offline", filepath.Clean(*file), *format})
}

func makeReleaseReport(mode, batchID string, records []state.ReleaseRecord) releaseReport {
	r := releaseReport{SchemaVersion: 1, Mode: mode, Effect: "offline", GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), BatchID: batchID, FullScope: true, Releases: []reportedRelease{}}
	for _, record := range records {
		m := record.Snapshot.Manifest
		release := reportedRelease{ReleaseID: m.ReleaseID, IntentDigest: record.Snapshot.Digest, PackageName: m.PackageName, VersionName: m.VersionName, VersionCode: m.VersionCode, CreatedAt: record.CreatedAt, ReleasePolicy: m.ReleasePolicy, Notes: record.Snapshot.Notes, RecoveryRequired: record.RecoveryRequired, Targets: []reportedTarget{}, Operations: []state.HistoryOperation{}, historyCounts: countsForTargets(record.Targets)}
		for _, t := range record.Targets {
			a := m.Artifacts[m.Targets[t.Channel].Artifact]
			health := "not_queried"
			if t.LastError != "" || t.QueryFailures > 0 {
				health = "failed"
			} else if t.LastSuccessAt != "" {
				health = "healthy"
			}
			if t.Context != nil && (t.LastError == "" || t.LastError == "one or more channel sources failed") {
				if status := t.Context.QueryStatus(); status == "partial" {
					health = "partial"
				} else if status == "query_failed" {
					health = "failed"
				}
			}
			target := reportedTarget{Channel: t.Channel, ArtifactSHA256: a.SHA256, SignerSHA256: a.SignerSHA256, ABI: slices.Clone(a.ABI), Execution: reportEnum(t.Phase, "planned", "ready", "submitting", "staged", "bound", "submission_unknown", "blocked", "submitted"), Review: reportEnum(t.Review, "unknown", "pending", "in_review", "approved", "rejected", "withdrawn"), Availability: reportEnum(t.Availability, "unknown", "live", "not_observed_live", "scheduled", "superseded"), EvidenceDisputed: t.Disputed, QueryHealth: health, QueryFailures: t.QueryFailures, LastSuccessAt: reportTime(t.LastSuccessAt), NextCheckAt: reportTime(t.NextCheckAt), ReviewSource: reportSource(t.ReviewSource), ReviewObservedAt: reportTime(t.ReviewObservedAt), AvailabilitySource: reportSource(t.AvailabilitySource), AvailabilityObservedAt: reportTime(t.AvailabilityObservedAt), ContextObservedAt: reportTime(t.ContextObservedAt)}
			if t.Context != nil && t.Context.Validate() == nil {
				contextCopy := *t.Context
				target.Context = &contextCopy
			}
			if t.CachedContext != nil && t.CachedContext.Validate() == nil {
				cachedCopy := *t.CachedContext
				target.CachedContext, target.CachedContextObservedAt = &cachedCopy, reportTime(t.CachedContextObservedAt)
			}
			release.Targets = append(release.Targets, target)
		}
		for _, op := range record.Operations {
			op.Kind = reportEnum(op.Kind, "authorize", "upload", "bind", "metadata", "submit", "push")
			op.Effect = reportEnum(op.Effect, "intent", "unknown", "confirmed_applied", "confirmed_rejected", "confirmed_not_applied")
			op.CreatedAt = reportTime(op.CreatedAt)
			release.Operations = append(release.Operations, op)
		}
		r.Releases = append(r.Releases, release)
		r.Total += release.Total
		r.Accepted += release.Accepted
		r.Approved += release.Approved
		r.Live += release.Live
	}
	r.complete()
	return r
}

func reportEnum(value string, allowed ...string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return "unknown"
}

func reportTime(value string) string {
	if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return at.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

func reportSource(source string) string {
	if slices.Contains([]string{"simulation", "browser_fixture", "manual_console"}, source) {
		return source
	}
	for _, channel := range manifest.Channels {
		if slices.Contains([]string{channel + "_api", channel + "_api_exact_version_after_ack", channel + "_automatic_context"}, source) {
			return source
		}
	}
	return ""
}

func reportMarkdown(s string) string {
	s = html.EscapeString(historyPlain(s))
	return strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "|", "\\|", "#", "\\#", "!", "\\!", "~", "\\~").Replace(s)
}

func reportLabel(s string) string {
	if label, ok := map[string]string{
		"planned": "已规划", "ready": "待执行", "submitting": "执行中", "staged": "制品已暂存", "bound": "资料已绑定", "submission_unknown": "结果未知", "blocked": "已阻塞", "submitted": "已受理",
		"unknown": "未知", "pending": "待审核", "in_review": "审核中", "approved": "审核通过", "rejected": "已驳回", "withdrawn": "已撤回", "live": "已上架", "not_observed_live": "未观察到上架", "scheduled": "待发布", "superseded": "已被替代",
		"healthy": "正常", "failed": "失败", "not_queried": "未查询", "not_supported": "不支持", "partial": "部分来源失败",
		"authorize": "领取上传授权", "upload": "上传", "bind": "绑定制品", "metadata": "更新资料", "submit": "提交审核", "push": "上传并提交审核",
		"intent": "已记录意图", "confirmed_applied": "已确认生效", "confirmed_rejected": "已确认拒绝", "confirmed_not_applied": "已确认未生效", "reconcile": "核对结果", "resume": "恢复执行", "observe": "查询状态", "fix_config": "修正配置",
	}[s]; ok {
		return label
	}
	return s
}

func renderReleaseReport(r releaseReport) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# 发布报告\n\n")
	fmt.Fprintf(&out, "生成时间：%s；数据来源：本地缓存，未查询渠道。\n\n", reportMarkdown(r.GeneratedAt))
	fmt.Fprintf(&out, "运行范围：%s；受理 %d/%d，审核通过 %d/%d，上架 %d/%d。\n\n", reportMarkdown(r.Mode), r.Accepted, r.Total, r.Approved, r.Total, r.Live, r.Total)
	fmt.Fprintf(&out, "统计覆盖所选发布的全部渠道；审核推测和未关联本次版本的信息不计入审核通过、上架统计。\n\n")
	if r.BatchID != "" {
		fmt.Fprintf(&out, "批次：%s\n\n", reportMarkdown(r.BatchID))
	}
	for _, release := range r.Releases {
		fmt.Fprintf(&out, "## %s\n\n", reportMarkdown(release.ReleaseID))
		fmt.Fprintf(&out, "应用：%s；版本：%s（%d）；创建：%s。\n\n", reportMarkdown(release.PackageName), reportMarkdown(release.VersionName), release.VersionCode, reportMarkdown(release.CreatedAt))
		fmt.Fprintf(&out, "冻结意图：%s\n\n", release.IntentDigest)
		policy := "审核通过后立即发布"
		if release.ReleasePolicy.Mode == "scheduled" {
			policy = "定时发布：" + release.ReleasePolicy.At
		}
		fmt.Fprintf(&out, "发布方式：%s。\n\n", reportMarkdown(policy))
		if release.RecoveryRequired {
			fmt.Fprintf(&out, "恢复核对未完成：业务写入暂停。\n\n")
		}
		fmt.Fprintf(&out, "更新说明：\n\n")
		for _, line := range strings.Split(release.Notes, "\n") {
			fmt.Fprintf(&out, "> %s\n", reportMarkdown(line))
		}
		fmt.Fprintln(&out)
		for _, target := range release.Targets {
			fmt.Fprintf(&out, "### %s\n\n", target.Channel)
			fmt.Fprintf(&out, "执行：%s；本次版本审核：%s；本次版本上架：%s。\n\n", reportLabel(target.Execution), reportLabel(target.Review), reportLabel(target.Availability))
			if target.EvidenceDisputed {
				fmt.Fprintf(&out, "证据有争议，需核对；未计入审核通过和上架统计。\n\n")
			}
			fmt.Fprintf(&out, "查询健康：%s；连续失败：%d；最近成功：%s。\n\n", reportLabel(target.QueryHealth), target.QueryFailures, reportMarkdown(target.LastSuccessAt))
			fmt.Fprintf(&out, "审核证据：%s（%s）；上架证据：%s（%s）。\n\n", reportMarkdown(target.ReviewSource), reportMarkdown(target.ReviewObservedAt), reportMarkdown(target.AvailabilitySource), reportMarkdown(target.AvailabilityObservedAt))
			fmt.Fprintf(&out, "APK SHA-256：%s\n\n签名证书 SHA-256：%s\n\n", target.ArtifactSHA256, target.SignerSHA256)
			if c := target.Context; c != nil {
				fmt.Fprintf(&out, "自动查询补充信息（未确认属于本次版本，不计入审核或上架统计）：\n\n")
				fmt.Fprintf(&out, "- 采集时间：%s；API 查询：%s；商店查询：%s。\n", reportMarkdown(target.ContextObservedAt), reportLabel(c.APIQueryHealth), reportLabel(c.StoreQueryHealth))
				if c.APIVersionCode > 0 || c.APIVersionName != "" {
					fmt.Fprintf(&out, "- API 版本：%s（%d）。\n", reportMarkdown(c.APIVersionName), c.APIVersionCode)
				}
				if c.InferredReview != "" {
					fmt.Fprintf(&out, "- 审核推测：%s；依据：%s。此为推测，未取得本次版本审核证据。\n", reportLabel(c.InferredReview), c.InferenceBasis)
				}
				if c.AuditStatus != "" {
					fmt.Fprintf(&out, "- 审核槽位：%s（代码 %s）；未确认属于本次版本。\n", reportLabel(c.AuditStatus), c.AuditCode)
				}
				if c.RejectionReason != "" {
					fmt.Fprintf(&out, "- 驳回原因：%s（未确认属于本次版本）。\n", reportMarkdown(c.RejectionReason))
				}
				if c.StoreVersionCode > 0 || c.StoreVersionName != "" {
					fmt.Fprintf(&out, "- 商店当前版本：%s（%d）；未确认属于本次版本。\n", reportMarkdown(c.StoreVersionName), c.StoreVersionCode)
				}
				fmt.Fprintln(&out)
			}
			if target.CachedContext != nil {
				fmt.Fprintf(&out, "上次可用信息（缓存，采集于 %s；不代表本轮查询成功）：%s\n\n", reportMarkdown(target.CachedContextObservedAt), reportMarkdown(channelContextText(target.CachedContext)))
			}
		}
		if len(release.Operations) > 0 {
			fmt.Fprintf(&out, "操作记录：\n\n")
			fmt.Fprintln(&out, "| 序号 | 渠道 | 操作 | 结果 | 尝试次数 | 下一步 |\n| --- | --- | --- | --- | --- | --- |")
			for _, op := range release.Operations {
				fmt.Fprintf(&out, "| %d | %s | %s | %s | %d | %s |\n", op.Sequence, op.Channel, reportLabel(op.Kind), reportLabel(op.Effect), op.Attempt, reportLabel(op.NextAction))
			}
			fmt.Fprintln(&out)
		}
	}
	return out.Bytes()
}

func writeNewReport(path string, data []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("invalid report path")
	}
	parent := filepath.Dir(path)
	for dir := parent; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("invalid report parent")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	f, err := os.CreateTemp(parent, ".publisher-report-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Hard-link publication is exclusive and atomic, including against a
	// pre-existing symlink. Existing exports are never replaced or truncated.
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	return privatefs.SyncDir(parent)
}
