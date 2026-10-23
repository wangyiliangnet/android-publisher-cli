package protocol_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

func TestReceiptRequiresPresentEffectAndExactIdentity(t *testing.T) {
	q := protocol.Request{OperationID: "operation", TargetID: "target", Scope: "scope", Kind: "push"}
	valid := protocol.Receipt{OperationID: q.OperationID, TargetID: q.TargetID, Scope: q.Scope, Kind: q.Kind, RequestDigest: protocol.Digest(q), Effect: "confirmed_applied", RemoteRef: "9007199254740993"}
	if err := valid.Validate(q); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"effect", "remote_ref", "request_digest", "target_id", "operation_id", "scope", "kind"} {
		t.Run(field, func(t *testing.T) {
			data, _ := json.Marshal(valid)
			var fields map[string]any
			json.Unmarshal(data, &fields)
			delete(fields, field)
			data, _ = json.Marshal(fields)
			var r protocol.Receipt
			json.Unmarshal(data, &r)
			if r.Validate(q) == nil {
				t.Fatal("missing receipt field accepted")
			}
		})
	}
	valid.Effect = "future_vendor_success"
	if valid.Validate(q) == nil {
		t.Fatal("unknown effect accepted")
	}
	var numeric protocol.Receipt
	if json.Unmarshal([]byte(`{"remote_ref":9007199254740993}`), &numeric) == nil {
		t.Fatal("numeric remote ID accepted as a string")
	}
}
func TestObservationRequiresTargetVersionAndKnownEnums(t *testing.T) {
	valid := protocol.Observation{Scope: "scope", VersionCode: 2, RemoteRef: "task", Source: "simulation", CollectedAt: time.Now(), Review: "approved", Availability: "scheduled"}
	if err := valid.Validate("scope", 2, "task"); err != nil {
		t.Fatal(err)
	}
	if valid.Validate("scope", 3, "task") == nil || valid.Validate("scope", 2, "other-task") == nil {
		t.Fatal("older or unrelated evidence accepted")
	}
	valid.Review = ""
	if valid.Validate("scope", 2, "task") == nil {
		t.Fatal("missing enum defaulted to success")
	}
}
