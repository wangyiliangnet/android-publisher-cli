package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type historyCounts struct {
	Total       int  `json:"total_targets"`
	Accepted    int  `json:"accepted_targets"`
	Approved    int  `json:"approved_targets"`
	Live        int  `json:"live_targets"`
	AllAccepted bool `json:"all_accepted"`
	AllApproved bool `json:"all_approved"`
	AllLive     bool `json:"all_live"`
}

type historyRow struct {
	ReleaseID        string   `json:"release_id"`
	PackageName      string   `json:"package_name"`
	VersionCode      int64    `json:"version_code"`
	VersionName      string   `json:"version_name"`
	CreatedAt        string   `json:"created_at"`
	Channels         []string `json:"channels"`
	IntentDigest     string   `json:"intent_digest"`
	RecoveryRequired bool     `json:"recovery_required"`
	historyCounts
}

func countsForTargets(targets []state.Target) historyCounts {
	r := historyCounts{Total: len(targets)}
	for _, t := range targets {
		if t.Phase == "submitted" {
			r.Accepted++
		}
		if t.Review == "approved" && !t.Disputed {
			r.Approved++
		}
		if t.Availability == "live" && !t.Disputed {
			r.Live++
		}
	}
	r.complete()
	return r
}

func (r *historyCounts) complete() {
	r.AllAccepted = r.Total > 0 && r.Accepted == r.Total
	r.AllApproved = r.Total > 0 && r.Approved == r.Total
	r.AllLive = r.Total > 0 && r.Live == r.Total
}

func historyRows(records []state.ReleaseRecord) []historyRow {
	rows := make([]historyRow, 0, len(records))
	for _, record := range records {
		m := record.Snapshot.Manifest
		r := historyRow{ReleaseID: m.ReleaseID, PackageName: m.PackageName, VersionCode: m.VersionCode, VersionName: m.VersionName, CreatedAt: record.CreatedAt, Channels: []string{}, IntentDigest: record.Snapshot.Digest, RecoveryRequired: record.RecoveryRequired, historyCounts: countsForTargets(record.Targets)}
		for _, t := range record.Targets {
			r.Channels = append(r.Channels, t.Channel)
		}
		rows = append(rows, r)
	}
	return rows
}

func runHistory(ctx context.Context, mode, root string, args []string, out, stderr io.Writer) int {
	f := flags("history", stderr)
	pkg := f.String("package", "", "filter package name")
	channel := f.String("channel", "", "filter releases containing a channel")
	limit := f.Int("limit", 20, "maximum releases, 1 to 200")
	format := f.String("format", "table", "table or json")
	if err := parse(f, args); err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	bad := func() int { return failure(out, mode, "fix_config", 2, fmt.Errorf("invalid history options")) }
	if *pkg != "" && !validHistoryPackage(*pkg) || *limit < 1 || *limit > 200 || *format != "table" && *format != "json" || *channel != "" && !slices.Contains(manifest.Channels, *channel) || mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return bad()
	}
	filter := state.HistoryFilter{PackageName: *pkg, Channel: *channel, Limit: *limit}
	// A first invocation has no history; do not create a publishing database.
	rows := []historyRow{}
	if _, err := os.Lstat(filepath.Join(root, "state.sqlite")); err == nil {
		s, err := state.Open(root, mode, true)
		if err != nil {
			return failure(out, mode, "check_local_state", 5, fmt.Errorf("local history is unavailable"))
		}
		records, readErr := s.History(ctx, filter)
		s.Close()
		if readErr != nil {
			return failure(out, mode, "check_local_state", 5, fmt.Errorf("local history could not be read"))
		}
		rows = historyRows(records)
	} else if !os.IsNotExist(err) {
		return failure(out, mode, "check_local_state", 5, fmt.Errorf("local history is unavailable"))
	}
	if *format == "json" {
		return encodeCode(out, struct {
			SchemaVersion int          `json:"schema_version"`
			Mode          string       `json:"mode"`
			Effect        string       `json:"effect"`
			Releases      []historyRow `json:"releases"`
		}{1, mode, "offline", rows})
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "本地发布历史（缓存，未查询渠道）")
	if len(rows) == 0 {
		fmt.Fprintln(w, "暂无符合条件的发布记录。")
	} else {
		fmt.Fprintln(w, "发布\t包名\t版本\t创建时间\t渠道\t受理\t审核通过\t上架")
		for _, r := range rows {
			fmt.Fprintf(w, "%s\t%s\t%s(%d)\t%s\t%s\t%d/%d\t%d/%d\t%d/%d\n", r.ReleaseID, r.PackageName, historyPlain(r.VersionName), r.VersionCode, localHistoryTime(r.CreatedAt), strings.Join(r.Channels, ","), r.Accepted, r.Total, r.Approved, r.Total, r.Live, r.Total)
			fmt.Fprintf(w, "  冻结意图：%s\n", r.IntentDigest)
			if r.RecoveryRequired {
				fmt.Fprintln(w, "  恢复核对未完成：业务写入暂停。")
			}
		}
	}
	if err := w.Flush(); err != nil {
		return 5
	}
	return 0
}

func historyPlain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
}

func localHistoryTime(s string) string {
	if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return at.Local().Format("2006-01-02 15:04:05 -0700")
	}
	return "—"
}

func validHistoryPackage(s string) bool {
	// Same public package grammar as frozen manifests, without accepting an
	// arbitrary SQL fragment or a shell expression as a filter.
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		for i, r := range part {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '_')) {
				return false
			}
		}
		if part == "" {
			return false
		}
	}
	return true
}
