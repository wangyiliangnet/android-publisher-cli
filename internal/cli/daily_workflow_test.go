package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
)

func TestDailyBatchCLIReviewSubmitResumeHistoryAndReport(t *testing.T) {
	v := batchTestSetup(t, []string{"xiaomi", "vivo"}, simulation.Options{})
	ctx := context.Background()
	run := func(args ...string) (string, int) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := Run(ctx, append([]string{"simulate", "--data-dir", v.root}, args...), &out, &stderr)
		return out.String(), code
	}
	file := filepath.Join(t.TempDir(), "approval.json")
	out, code := run("batch", "review", "--batch", v.batch.ID, "--file", file)
	if code != 0 || !strings.Contains(out, v.plan.PlanDigest) {
		t.Fatal("review hook", out, code)
	}
	out, code = run("batch", "submit", "--batch", v.batch.ID, "--approval", file, "--approve-plan", v.plan.PlanDigest, "--store", v.server.URL)
	var result batchExecutionResult
	if json.Unmarshal([]byte(out), &result) != nil || code != 0 || result.Accepted != 2 {
		t.Fatal("submit hook", out, code)
	}
	before, err := v.client.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, code = run("batch", "resume", "--batch", v.batch.ID, "--approval", file, "--approve-plan", v.plan.PlanDigest, "--store", v.server.URL)
	if code != 0 || !strings.Contains(out, "already_accepted") {
		t.Fatal("resume hook", out, code)
	}
	out, code = run("history", "--format", "json")
	if code != 0 || !strings.Contains(out, "team-xiaomi") || !strings.Contains(out, "\"mode\":\"simulation\"") {
		t.Fatal("history hook", out, code)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "report.md")
	out, code = run("report", "--batch", v.batch.ID, "--file", report)
	if code != 0 {
		t.Fatal("report hook", out, code)
	}
	data, err := os.ReadFile(report)
	if err != nil || !bytes.Contains(data, []byte("本次优化")) || bytes.Contains(data, []byte("baseline_ref")) {
		t.Fatal("report contents", err)
	}
	after, err := v.client.Stats(ctx)
	if err != nil || protocol.Digest(before) != protocol.Digest(after) {
		t.Fatal("cached history/report or repeat resume touched store", before, after, err)
	}
	if _, code = run("batch", "review", "--batch", v.batch.ID, "--file", file, "--approve-plan", v.plan.PlanDigest); code != 2 {
		t.Fatal("review accepted write approval flag", code)
	}
}

func TestChannelContextTableClearlyLabelsInferenceAndAssociation(t *testing.T) {
	r := batchResult{ID: "fixture", Total: 2, Targets: []batchRow{
		{Channel: "xiaomi", Review: "unknown", Availability: "unknown", Context: &protocol.ChannelContext{APIVersionCode: 248, APIVersionName: "3.2.7", InferredReview: "approved", InferenceBasis: "xiaomi_api_version_equals_target", Association: "unconfirmed", APIQueryHealth: "healthy", StoreQueryHealth: "not_supported"}},
		{Channel: "tencent", Review: "unknown", Availability: "unknown", Context: &protocol.ChannelContext{AuditStatus: "rejected", RejectionReason: "请补充材料", StoreVersionName: "3.2.6", Association: "unconfirmed", APIQueryHealth: "healthy", StoreQueryHealth: "healthy"}},
	}}
	var out bytes.Buffer
	if err := writeBatchResult(&out, r, "table"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审核通过 0/2", "上架 0/2", "审核推测：通过", "未确认属于本次版本", "驳回原因：请补充材料", "商店当前版本 3.2.6"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing explicit context distinction", want, out.String())
		}
	}
	if strings.Contains(out.String(), "需后台核对") {
		t.Fatal("manual entry became daily prerequisite")
	}
}
