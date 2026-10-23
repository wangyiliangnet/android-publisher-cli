package channels

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

type automaticTestExecutor struct {
	Executor
	apiErr error
	store  func(context.Context, string) (publicStoreVersion, error)
}

func (e automaticTestExecutor) WithClient(ctx context.Context, target Target, f func(*Client, Materials) error) error {
	if e.apiErr != nil {
		return e.apiErr
	}
	return e.Executor.WithClient(ctx, target, f)
}
func (e automaticTestExecutor) tencentPublicVersion(ctx context.Context, pkg string) (publicStoreVersion, error) {
	return e.store(ctx, pkg)
}

func TestSyntheticRuntimeCannotFallThroughToRealPublicStore(t *testing.T) {
	run := Runtime{sender: nil}
	// The seam deliberately returns nil: the guard must check its presence,
	// without constructing an account lease or touching either network.
	run.sender = func(_ *account.Lease, _ account.Scope) Sender { return nil }
	if _, err := run.tencentPublicVersion(context.Background(), "test.synthetic"); !errors.Is(err, publicStoreNotQueried) {
		t.Fatal("synthetic API runtime fell through to a real public host")
	}
}

func TestPublicReaderCapturedBeforeProductionSenderWrapper(t *testing.T) {
	production := (Runtime{}).withPublicStoreReader()
	if production.publicStore == nil {
		t.Fatal("production transport wrapping would disable automatic public read")
	}
	synthetic := Runtime{sender: func(*account.Lease, account.Scope) Sender { return nil }}
	if synthetic.withPublicStoreReader().publicStore != nil {
		t.Fatal("synthetic sender acquired a real public reader")
	}
	calls := 0
	synthetic.publicStore = func(context.Context, string) (publicStoreVersion, error) {
		calls++
		return publicStoreVersion{Name: "1.0"}, nil
	}
	if _, err := synthetic.withPublicStoreReader().tencentPublicVersion(context.Background(), "test.synthetic"); err != nil || calls != 1 {
		t.Fatal("explicit synthetic public seam was not preserved")
	}
}

func TestTencentPublicFallbackNeedsNoCredentialedAdapter(t *testing.T) {
	for _, failed := range []bool{false, true} {
		calls := 0
		c, err := tencentPublicContext(context.Background(), "test.synthetic", func(_ context.Context, pkg string) (publicStoreVersion, error) {
			calls++
			if pkg != "test.synthetic" {
				t.Fatal("fallback changed application")
			}
			if failed {
				return publicStoreVersion{}, errors.New("synthetic store failure")
			}
			return publicStoreVersion{Name: "1.0"}, nil
		})
		if err != nil || calls != 1 || c.APIQueryHealth != "not_queried" || c.Association != "unconfirmed" || c.Validate() != nil {
			t.Fatal("public-only fallback required API or target evidence", err)
		}
		if failed && c.StoreQueryHealth != "failed" || !failed && c.StoreQueryHealth != "healthy" {
			t.Fatal("public fallback fabricated success")
		}
	}
	if _, err := TencentPublicContext(context.Background(), "../invalid"); err == nil {
		t.Fatal("invalid fallback package accepted")
	}
}

func TestAutomaticXiaomiVersionInferenceNeverBecomesTargetEvidence(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version, expected, basis string
	}{
		{"1", "in_review", "xiaomi_api_version_before_target"},
		{"2", "approved", "xiaomi_api_version_equals_target"},
		{"9007199254740993", "unknown", "xiaomi_api_version_after_target"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			target := runtimeTarget(t, "xiaomi")
			calls := 0
			run := &StaticRuntime{Auth: map[string]Auth{"xiaomi": {ID: "synthetic", Secret: runtimeSecret(t, "synthetic-secret"), PublicKey: &key.PublicKey}}, Grants: &MemoryGrants{}, Wire: runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
				calls++
				if q.Class != transport.Query || q.OperationID != "" || q.URL != origins["xiaomi"]+"/devupload/dev/query" || q.Method != "POST" {
					t.Fatal("observation attempted a publishing effect")
				}
				io.Copy(io.Discard, q.Body)
				return []byte(`{"result":0,"packageInfo":{"packageName":"test.synthetic","versionCode":` + tc.version + `,"versionName":"1.0"}}`), nil
			})}
			adapter, err := NewAdapter([]Target{target}, run)
			if err != nil {
				t.Fatal(err)
			}
			ref := ack(runtimeRequest(target, "push").OperationID).Ref
			observation, err := adapter.Observe(context.Background(), target.Scope, 2, ref)
			if err != nil || observation.Validate(target.Scope, 2, ref) != nil || observation.Review != "unknown" || observation.Availability != "unknown" || observation.Context.InferredReview != tc.expected || observation.Context.InferenceBasis != tc.basis {
				t.Fatalf("unsegregated or missing inference: %#v %v", observation, err)
			}
			if tc.version == "9007199254740993" && observation.Context.APIVersionCode != 9007199254740993 {
				t.Fatal("large integer lost precision")
			}
			other, err := adapter.Observe(context.Background(), target.Scope, 2, "unverified-reference")
			if err != nil || other.Context.InferredReview != "unknown" || other.Context.InferenceBasis != "submission_not_confirmed" {
				t.Fatal("unconfirmed submission inferred as accepted")
			}
			before := calls
			result, err := adapter.Reconcile(context.Background(), runtimeRequest(target, "push"))
			if err != nil || result.Result != "inconclusive" || calls != before {
				t.Fatal("inference settled or resent unresolved push")
			}
		})
	}
}

func TestAutomaticTencentRetainsIndependentAPIAndStoreFacts(t *testing.T) {
	for _, tc := range []struct {
		name, body, audit, apiHealth, storeHealth string
		apiErr, storeErr                          bool
	}{
		{"approved_unassociated", `{"ret":0,"audit_status":3}`, "approved", "healthy", "healthy", false, false},
		{"rejected_store_failed", `{"ret":0,"audit_status":2,"audit_reason":"补充说明 token=synthetic-private-value https://example.invalid/?secret=x"}`, "rejected", "healthy", "failed", false, true},
		{"unknown_code", `{"ret":0,"audit_status":99}`, "unknown", "healthy", "healthy", false, false},
		{"malformed_api", `{"ret":0,"audit_status":"3"}`, "", "failed", "healthy", false, false},
		{"credential_failure", "", "", "failed", "healthy", true, false},
		{"both_failed", "", "", "failed", "failed", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := runtimeTarget(t, "tencent")
			apiCalls, storeCalls := 0, 0
			base := &StaticRuntime{Auth: map[string]Auth{"tencent": {ID: "synthetic", Secret: runtimeSecret(t, "synthetic-secret")}}, Grants: &MemoryGrants{}, Wire: runtimeSender(func(_ context.Context, q transport.Request) ([]byte, error) {
				apiCalls++
				if q.URL != origins["tencent"]+"/open_file/developer_api/query_app_update_status" || q.Class != transport.Query || q.OperationID != "" || q.Grant != nil {
					t.Fatal("automatic observation attempted business effect")
				}
				io.Copy(io.Discard, q.Body)
				return []byte(tc.body), nil
			})}
			run := automaticTestExecutor{Executor: base, store: func(_ context.Context, pkg string) (publicStoreVersion, error) {
				storeCalls++
				if pkg != target.PackageName {
					t.Fatal("public page for wrong package")
				}
				if tc.storeErr {
					return publicStoreVersion{}, errors.New("synthetic store failure")
				}
				return publicStoreVersion{Code: 1, Name: "1.0"}, nil
			}}
			if tc.apiErr {
				run.apiErr = errors.New("synthetic credential failure")
			}
			adapter, err := NewAdapter([]Target{target}, run)
			if err != nil {
				t.Fatal(err)
			}
			ref := ack(runtimeRequest(target, "submit").OperationID).Ref
			o, err := adapter.Observe(context.Background(), target.Scope, 2, ref)
			if err != nil || o.Validate(target.Scope, 2, ref) != nil {
				t.Fatal("partial facts lost", err)
			}
			c := o.Context
			if c.AuditStatus != tc.audit || c.APIQueryHealth != tc.apiHealth || c.StoreQueryHealth != tc.storeHealth || c.Association != "unconfirmed" || o.Review != "unknown" || o.Availability != "unknown" || storeCalls != 1 {
				t.Fatalf("source health or evidence mixed: %#v", c)
			}
			if !tc.storeErr && c.StoreVersionName != "1.0" || tc.storeErr && c.StoreVersionName != "" {
				t.Fatal("store failure overwrote or fabricated fact")
			}
			if strings.Contains(c.RejectionReason, "synthetic-private-value") || strings.Contains(c.RejectionReason, "https://") {
				t.Fatal("raw rejection text leaked")
			}
			before := apiCalls
			r, err := adapter.Reconcile(context.Background(), runtimeRequest(target, "submit"))
			if err != nil || r.Result != "inconclusive" || apiCalls != before || storeCalls != 1 {
				t.Fatal("unassociated audit settled or resent unknown write")
			}
		})
	}
}

func TestTencentAuditKnownCodesAndMissingBinding(t *testing.T) {
	for _, tc := range []struct{ code, status string }{{"1", "in_review"}, {"2", "rejected"}, {"3", "approved"}, {"8", "withdrawn"}, {"9007199254740993", "unknown"}} {
		w := localTLS(t, func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			io.WriteString(w, `{"ret":0,"audit_status":`+tc.code+`}`)
		})
		client := &Client{Channel: "tencent", Wire: w, Auth: Auth{ID: "fixture", Secret: runtimeSecret(t, "synthetic-secret")}}
		s, err := client.TencentAudit(context.Background(), "test.synthetic", "9007199254740993")
		if err != nil || s.VersionCode != 0 || s.Ref != "" || s.Review != "unknown" || s.Availability != "unknown" || s.Context.AuditCode != tc.code || s.Context.AuditStatus != tc.status {
			t.Fatal("audit slot promoted into exact target or integer truncated", err)
		}
	}
}
