package protocol

import "strconv"

// Authorization generations are a narrowly bounded Huawei allocation identity.
// Omitting generation zero preserves all already frozen request digests.
func (q Request) ValidAuthorizationGeneration() bool {
	return q.AuthorizationGeneration == 0 || q.Channel == "huawei" && q.Kind == "authorize" && q.AuthorizationGeneration > 0 && q.AuthorizationGeneration < 3
}

// MaterialOperationKey keeps renewed allocation checkpoints immutable while the
// business operation ID remains stable in the ledger. No vendor parameter is
// added or retried merely because this local generation changes.
func (q Request) MaterialOperationKey() string {
	if q.AuthorizationGeneration == 0 {
		return q.OperationID
	}
	return q.OperationID + "-authorization-generation-" + strconv.Itoa(q.AuthorizationGeneration)
}
