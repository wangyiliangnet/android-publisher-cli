package protocol

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAuthorizationGenerationKeepsExistingRequestsAndLimitsRenewalScope(t *testing.T) {
	q := Request{OperationID: "existing-operation", Channel: "huawei", Kind: "authorize"}
	old := Digest(q)
	b, _ := json.Marshal(q)
	if bytes.Contains(b, []byte("authorization_generation")) || q.MaterialOperationKey() != q.OperationID || !q.ValidAuthorizationGeneration() {
		t.Fatal("generation zero changed the existing wire identity")
	}
	for _, generation := range []int{1, 2} {
		renewed := q
		renewed.AuthorizationGeneration = generation
		if !renewed.ValidAuthorizationGeneration() || Digest(renewed) == old || renewed.MaterialOperationKey() == q.MaterialOperationKey() {
			t.Fatal("renewal generation lacks distinct bounded identity")
		}
	}
	for _, bad := range []Request{{Channel: "huawei", Kind: "upload", AuthorizationGeneration: 1}, {Channel: "oppo", Kind: "authorize", AuthorizationGeneration: 1}, {Channel: "honor", Kind: "authorize", AuthorizationGeneration: 1}, {Channel: "huawei", Kind: "authorize", AuthorizationGeneration: -1}, {Channel: "huawei", Kind: "authorize", AuthorizationGeneration: 3}} {
		if bad.ValidAuthorizationGeneration() {
			t.Fatal("authorization generation escaped supported scope")
		}
	}
}
