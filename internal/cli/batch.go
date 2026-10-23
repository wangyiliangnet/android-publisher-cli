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
	"text/tabwriter"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
)

type batchEntry struct {
	ReleaseID      string `json:"release_id"`
	IntentDigest   string `json:"intent_digest,omitempty"`
	Channel        string `json:"channel"`
	AccountConfig  string `json:"account_config,omitempty"`
	ConfigSHA256   string `json:"account_config_sha256,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
}
type batchFile struct {
	SchemaVersion int          `json:"schema_version"`
	ID            string       `json:"batch_id"`
	Entries       []batchEntry `json:"entries"`
}
type batchRow struct {
	ReleaseID               string                   `json:"release_id"`
	Channel                 string                   `json:"channel"`
	VersionName             string                   `json:"version_name"`
	VersionCode             int64                    `json:"version_code"`
	Execution               string                   `json:"execution_phase"`
	Review                  string                   `json:"review_state"`
	Availability            string                   `json:"availability_state"`
	LastSuccessAt           string                   `json:"last_success_at"`
	LastError               string                   `json:"last_error,omitempty"`
	QueryStatus             string                   `json:"query_status"`
	RecoveryRequired        bool                     `json:"recovery_required"`
	ReviewSource            string                   `json:"review_source,omitempty"`
	AvailabilitySource      string                   `json:"availability_source,omitempty"`
	EvidenceDisputed        bool                     `json:"evidence_disputed"`
	Context                 *protocol.ChannelContext `json:"channel_context,omitempty"`
	ContextObservedAt       string                   `json:"channel_context_observed_at,omitempty"`
	CachedContext           *protocol.ChannelContext `json:"cached_channel_context,omitempty"`
	CachedContextObservedAt string                   `json:"cached_channel_context_observed_at,omitempty"`
}
type batchResult struct {
	SchemaVersion int        `json:"schema_version"`
	ID            string     `json:"batch_id"`
	Mode          string     `json:"mode"`
	Effect        string     `json:"effect"`
	Sequence      int        `json:"sequence,omitempty"`
	Targets       []batchRow `json:"targets"`
	Total         int        `json:"total_targets"`
	Accepted      int        `json:"accepted_targets"`
	Approved      int        `json:"approved_targets"`
	Live          int        `json:"live_targets"`
	AllAccepted   bool       `json:"all_accepted"`
	AllApproved   bool       `json:"all_approved"`
	AllLive       bool       `json:"all_live"`
}

func batchPath(root, id string) string { return filepath.Join(root, "batches", id+".json") }
func readBatch(path string) (batchFile, error) {
	var b batchFile
	data, err := privatefs.Read(path, 65536)
	if err != nil || strictjson.Decode(data, &b) != nil || b.SchemaVersion != 1 || !manifest.ValidID(b.ID) || len(b.Entries) == 0 || len(b.Entries) > 6 {
		return b, fmt.Errorf("invalid batch configuration")
	}
	seen := map[string]bool{}
	for _, e := range b.Entries {
		if !manifest.ValidID(e.ReleaseID) || !slices.Contains(manifest.Channels, e.Channel) || seen[e.Channel] || (e.IntentDigest != "" && !manifest.ValidHash(e.IntentDigest)) {
			return b, fmt.Errorf("invalid batch identity")
		}
		seen[e.Channel] = true
	}
	return b, nil
}
func createBatch(root, mode, file string) (batchFile, error) {
	b, err := readBatch(file)
	if err != nil {
		return b, err
	}
	s, err := state.Open(root, mode, false)
	if err != nil {
		return b, err
	}
	defer s.Close()
	pkg, name := "", ""
	var version int64
	for i := range b.Entries {
		e := &b.Entries[i]
		snapshot, err := s.Load(e.ReleaseID)
		if err != nil {
			return b, fmt.Errorf("batch release not found")
		}
		if len(snapshot.Manifest.Targets) != 1 {
			return b, fmt.Errorf("batch requires single-channel frozen releases")
		}
		if _, ok := snapshot.Manifest.Targets[e.Channel]; !ok {
			return b, fmt.Errorf("batch channel differs from release")
		}
		if e.IntentDigest != "" && e.IntentDigest != snapshot.Digest {
			return b, fmt.Errorf("batch intent differs from release")
		}
		if (e.Channel != "oppo" && e.CredentialName != "") || (e.CredentialName != "" && !credentials.ValidCredentialName(e.CredentialName)) {
			return b, fmt.Errorf("invalid channel credential reference")
		}
		e.IntentDigest = snapshot.Digest
		if pkg != "" && (pkg != snapshot.Manifest.PackageName || version != snapshot.Manifest.VersionCode || name != snapshot.Manifest.VersionName) {
			return b, fmt.Errorf("batch package and version must match")
		}
		pkg = snapshot.Manifest.PackageName
		version = snapshot.Manifest.VersionCode
		name = snapshot.Manifest.VersionName
		if e.Channel != "oppo" && mode == manifest.Production {
			e.AccountConfig, err = filepath.Abs(e.AccountConfig)
			if err != nil {
				return b, fmt.Errorf("invalid account configuration")
			}
			data, err := privatefs.Read(e.AccountConfig, 65536)
			if err != nil {
				return b, fmt.Errorf("account configuration is unavailable")
			}
			var config channels.PreparationConfig
			if decodeFiveConfiguration(data, &config) != nil || config.Channel != e.Channel || config.Account != snapshot.Manifest.Targets[e.Channel].AccountRef || config.PackageName != snapshot.Manifest.PackageName {
				return b, fmt.Errorf("account configuration differs from the frozen target")
			}
			// The production executor validates account identity and canonical fields.
			// Freeze the exact bytes here so later file edits cannot change the batch.
			// Hash raw bytes, including their canonical whitespace, rather than
			// letting a source edit between parsing and hashing alter the binding.
			sum := sha256.Sum256(data)
			digest := hex.EncodeToString(sum[:])
			if e.ConfigSHA256 != "" && e.ConfigSHA256 != digest {
				return b, fmt.Errorf("account configuration changed")
			}
			e.ConfigSHA256 = digest
			frozenConfig := filepath.Join(root, "batches", b.ID+"-accounts", e.Channel+".json")
			if err := privatefs.Dir(filepath.Dir(frozenConfig)); err != nil {
				return b, err
			}
			if err := writeSame(frozenConfig, data); err != nil {
				return b, err
			}
			e.AccountConfig = frozenConfig
		} else if e.AccountConfig != "" || e.ConfigSHA256 != "" {
			return b, fmt.Errorf("unexpected account configuration")
		}
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return b, err
	}
	path := batchPath(root, b.ID)
	if old, err := privatefs.Read(path, 65536); err == nil {
		if !bytes.Equal(old, data) {
			return b, fmt.Errorf("batch ID is immutable; use a new batch ID")
		}
		return b, nil
	} else if !os.IsNotExist(err) {
		return b, err
	}
	// O_EXCL publication under the installation writer lock; never overwrite a batch.
	if err = privatefs.Dir(filepath.Dir(path)); err != nil {
		return b, err
	}
	f, err := privatefs.Open(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return b, err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = privatefs.SyncDir(filepath.Dir(path))
	}
	return b, err
}

type batchRefresh func(context.Context, batchEntry, io.Writer) int

func collectBatch(ctx context.Context, root, mode string, b batchFile, refresh bool, run batchRefresh) (batchResult, int) {
	result := batchResult{SchemaVersion: 1, ID: b.ID, Mode: mode, Effect: "read_only", Targets: []batchRow{}}
	exitCode := 0
	for _, e := range b.Entries {
		if ctx.Err() != nil {
			return result, 130
		}
		reader, err := state.Open(root, mode, true)
		if err != nil {
			return result, 5
		}
		before, err := reader.Load(e.ReleaseID)
		reader.Close()
		if err != nil || before.Digest != e.IntentDigest || len(before.Manifest.Targets) != 1 {
			return result, 2
		}
		if _, ok := before.Manifest.Targets[e.Channel]; !ok {
			return result, 2
		}
		queryCode := 0
		queryStatus := "cached"
		if refresh {
			queryStatus = "not_due"
			if e.ConfigSHA256 != "" {
				digest, _, err := privatefs.Hash(e.AccountConfig)
				if err != nil || digest != e.ConfigSHA256 {
					queryCode = 2
				}
			}
			if queryCode == 0 {
				var response bytes.Buffer
				queryCode = run(ctx, e, &response)
				var summary engine.Summary
				if err := json.Unmarshal(response.Bytes(), &summary); err != nil && queryCode == 0 {
					queryCode = 5
				}
				if status, ok := summary.ObservationStatuses[e.Channel]; ok {
					queryStatus = status.Status
				}
			}
		}
		s, err := state.Open(root, mode, true)
		if err != nil {
			return result, 5
		}
		snapshot, err := s.Load(e.ReleaseID)
		if err != nil || snapshot.Digest != e.IntentDigest {
			s.Close()
			return result, 2
		}
		summary, err := (&engine.Engine{Store: s}).Status(e.ReleaseID)
		s.Close()
		if err != nil {
			return result, 5
		}
		t := summary.Targets[0]
		status := queryStatus
		if queryCode != 0 {
			if queryStatus != "partial" {
				status = "query_failed"
			}
			if exitCode != 4 {
				exitCode = 3
			}
			if queryCode == 4 {
				exitCode = 4
			}
		}
		row := batchRow{ReleaseID: e.ReleaseID, Channel: e.Channel, VersionName: snapshot.Manifest.VersionName, VersionCode: snapshot.Manifest.VersionCode, Execution: t.Phase, Review: t.Review, Availability: t.Availability, LastSuccessAt: t.LastSuccessAt, LastError: t.LastError, QueryStatus: status, RecoveryRequired: summary.RecoveryRequired}
		row.ReviewSource, row.AvailabilitySource = t.ReviewSource, t.AvailabilitySource
		row.EvidenceDisputed = t.Disputed
		row.Context, row.ContextObservedAt = t.Context, t.ContextObservedAt
		row.CachedContext, row.CachedContextObservedAt = t.CachedContext, t.CachedContextObservedAt
		result.Targets = append(result.Targets, row)
		result.Total++
		result.Accepted += summary.Accepted
		result.Approved += summary.Approved
		result.Live += summary.Live
	}
	result.AllAccepted = result.Total > 0 && result.Accepted == result.Total
	result.AllApproved = result.Total > 0 && result.Approved == result.Total
	result.AllLive = result.Total > 0 && result.Live == result.Total
	return result, exitCode
}
func writeBatchResult(out io.Writer, r batchResult, format string) error {
	if format == "json" {
		return encode(out, r)
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "批次 %s  受理 %d/%d  审核通过 %d/%d  上架 %d/%d\n", r.ID, r.Accepted, r.Total, r.Approved, r.Total, r.Live, r.Total)
	for _, t := range r.Targets {
		if t.RecoveryRequired {
			fmt.Fprintln(w, "恢复核对未完成：业务写入暂停。")
			break
		}
	}
	fmt.Fprintln(w, "渠道\t版本\t执行\t审核\t上架\t证据\t最近成功查询\t查询")
	translate := func(v string) string {
		if label, ok := map[string]string{"oppo": "OPPO", "huawei": "华为", "honor": "荣耀", "xiaomi": "小米", "vivo": "vivo", "tencent": "应用宝", "submitted": "已受理", "in_review": "审核中", "approved": "通过", "rejected": "拒绝", "unknown": "未知", "live": "已上架", "not_observed_live": "未观察到上架", "scheduled": "待发布", "cached": "缓存", "not_due": "未到查询时间", "observed": "查询成功", "partial": "部分来源失败", "evidence_insufficient": "证据不足", "query_failed": "查询失败"}[v]; ok {
			return label
		}
		return v
	}
	for _, t := range r.Targets {
		source := "—"
		if t.ReviewSource != "" || t.AvailabilitySource != "" {
			source = "API"
		}
		if t.ReviewSource == "manual_console" || t.AvailabilitySource == "manual_console" {
			source = "人工后台"
		}
		review := translate(t.Review)
		if t.EvidenceDisputed {
			review += "（证据有争议）"
		}
		at := "—"
		if parsed, err := time.Parse(time.RFC3339Nano, t.LastSuccessAt); err == nil {
			at = parsed.Local().Format("01-02 15:04:05 -0700")
		}
		fmt.Fprintf(w, "%s\t%s(%d)\t%s\t%s\t%s\t%s\t%s\t%s\n", translate(t.Channel), t.VersionName, t.VersionCode, translate(t.Execution), review, translate(t.Availability), source, at, translate(t.QueryStatus))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, t := range r.Targets {
		if t.Context != nil {
			if _, err := fmt.Fprintf(out, "%s：%s（采集于 %s）\n", translate(t.Channel), channelContextText(t.Context), t.ContextObservedAt); err != nil {
				return err
			}
		}
		if t.CachedContext != nil {
			if _, err := fmt.Fprintf(out, "%s 上次可用信息（缓存，采集于 %s）：%s\n", translate(t.Channel), t.CachedContextObservedAt, channelContextText(t.CachedContext)); err != nil {
				return err
			}
		}
	}
	return nil
}

func channelContextText(c *protocol.ChannelContext) string {
	parts := []string{}
	version := func(name string, code int64) string {
		if name == "" {
			name = "未知名称"
		}
		if code > 0 {
			return fmt.Sprintf("%s(%d)", name, code)
		}
		return name
	}
	label := func(v string) string {
		if s, ok := map[string]string{"in_review": "审核中", "approved": "通过", "rejected": "驳回", "withdrawn": "已撤回", "unknown": "未知"}[v]; ok {
			return s
		}
		return "未知"
	}
	if c.APIVersionCode > 0 || c.APIVersionName != "" {
		parts = append(parts, "API 版本 "+version(c.APIVersionName, c.APIVersionCode))
	}
	if c.InferredReview != "" && c.InferredReview != "unknown" {
		parts = append(parts, "审核推测："+label(c.InferredReview)+"（仅依据版本，非已确认结果）")
	}
	if c.AuditStatus != "" {
		parts = append(parts, "审核接口："+label(c.AuditStatus)+"（未确认属于本次版本）")
	}
	if c.RejectionReason != "" {
		parts = append(parts, "驳回原因："+c.RejectionReason)
	}
	if c.StoreVersionName != "" || c.StoreVersionCode > 0 {
		parts = append(parts, "商店当前版本 "+version(c.StoreVersionName, c.StoreVersionCode)+"（未确认属于本次版本）")
	}
	if c.APIQueryHealth == "failed" {
		parts = append(parts, "API 查询失败，可检查钥匙串、网络或渠道权限")
	}
	if c.StoreQueryHealth == "failed" {
		parts = append(parts, "商店版本暂未取得")
	}
	return strings.Join(parts, "；")
}
func runBatch(ctx context.Context, mode, root string, args []string, inspector artifact.Inspector, out, stderr io.Writer) int {
	if len(args) > 0 && slices.Contains([]string{"review", "submit", "resume", "reconcile"}, args[0]) {
		return runBatchAction(ctx, mode, root, args, inspector, out, stderr)
	}
	if len(args) == 0 {
		return failure(out, mode, "fix_config", 2, fmt.Errorf("batch requires init, create, status or watch"))
	}
	f := flags("batch "+args[0], stderr)
	file := f.String("file", "", "batch specification JSON")
	id := f.String("batch", "", "saved batch ID")
	refresh := f.Bool("refresh", false, "read-only API refresh")
	format := f.String("format", "table", "table or json")
	interval := f.Duration("interval", time.Minute, "watch wake interval")
	maxPolls := f.Int("max-polls", 0, "watch cycles; zero runs until interrupted")
	if err := parse(f, args[1:]); err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	fail := func(err error) int { return failure(out, mode, "fix_config", 2, err) }
	if mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return fail(fmt.Errorf("production batch requires the canonical data directory"))
	}
	switch args[0] {
	case "init":
		if *file == "" || *id != "" {
			return fail(fmt.Errorf("batch init requires --file"))
		}
		template := batchFile{SchemaVersion: 1, ID: "example-1", Entries: []batchEntry{{ReleaseID: "example-1-oppo", Channel: "oppo"}, {ReleaseID: "example-1-xiaomi", Channel: "xiaomi", AccountConfig: "/absolute/private/xiaomi-account.json"}}}
		data, _ := json.MarshalIndent(template, "", "  ")
		p, err := filepath.Abs(*file)
		if err == nil {
			var fd *os.File
			fd, err = privatefs.Open(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
			if err == nil {
				_, err = fd.Write(data)
				fd.Close()
			}
		}
		if err != nil {
			return fail(fmt.Errorf("could not create batch template; file must not exist"))
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "template": p, "effect": "offline"})
	case "create":
		if *file == "" || *id != "" || *refresh {
			return fail(fmt.Errorf("batch create requires --file"))
		}
		b, err := createBatch(root, mode, *file)
		if err != nil {
			return fail(err)
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "batch_id": b.ID, "total_targets": len(b.Entries), "effect": "offline"})
	case "status", "watch", "preflight":
	default:
		return fail(fmt.Errorf("unknown batch command"))
	}
	if !manifest.ValidID(*id) || *file != "" || (*format != "table" && *format != "json") || *interval < time.Second || *interval > 24*time.Hour || *maxPolls < 0 {
		return fail(fmt.Errorf("invalid batch observation flags"))
	}
	b, err := readBatch(batchPath(root, *id))
	if err != nil || b.ID != *id {
		return fail(fmt.Errorf("saved batch is unavailable"))
	}
	if args[0] == "preflight" {
		if mode != manifest.Production {
			return fail(fmt.Errorf("batch preflight requires production mode"))
		}
		code := 0
		for _, entry := range b.Entries {
			if ctx.Err() != nil {
				return 130
			}
			if entry.ConfigSHA256 != "" {
				digest, _, err := privatefs.Hash(entry.AccountConfig)
				if err != nil || digest != entry.ConfigSHA256 {
					return fail(fmt.Errorf("account configuration changed"))
				}
			}
			var step int
			if entry.Channel == "oppo" {
				step = runOPPOProduction(ctx, "preflight", root, entry.ReleaseID, entry.CredentialName, "", "", false, nil, inspector, out)
			} else {
				step = runFiveProduction(ctx, "preflight", root, entry.ReleaseID, entry.AccountConfig, "", "", false, nil, inspector, out)
			}
			if step != 0 {
				code = step
			}
		}
		return code
	}
	run := func(ctx context.Context, e batchEntry, w io.Writer) int {
		if mode != manifest.Production {
			return fail(fmt.Errorf("simulation batch refresh requires single-release simulation status"))
		}
		if e.Channel == "oppo" {
			return runOPPOProduction(ctx, "status", root, e.ReleaseID, e.CredentialName, "", "", false, nil, inspector, w)
		}
		return runFiveProduction(ctx, "status", root, e.ReleaseID, e.AccountConfig, "", "", false, nil, inspector, w)
	}
	for cycle := 1; ; cycle++ {
		r, code := collectBatch(ctx, root, mode, b, *refresh || args[0] == "watch", run)
		r.Sequence = cycle
		if err := writeBatchResult(out, r, *format); err != nil {
			return 5
		}
		if code == 130 || ctx.Err() != nil {
			return 130
		}
		if code == 4 || code == 5 {
			return code
		}
		if args[0] != "watch" || (*maxPolls > 0 && cycle >= *maxPolls) {
			return code
		}
		timer := time.NewTimer(*interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 130
		case <-timer.C:
		}
	}
}
func encodeCode(w io.Writer, v any) int {
	if encode(w, v) != nil {
		return 5
	}
	return 0
}
