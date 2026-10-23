package channels

// Catalog is offline evidence about implementation, never account permission.
type ProtocolCapability struct {
	Channel              string   `json:"channel"`
	Steps                []string `json:"steps"`
	ProtocolMockVerified bool     `json:"protocol_mock_verified"`
	AccountVerified      bool     `json:"account_verified"`
	LiveVerified         bool     `json:"live_verified"`
	Limitations          []string `json:"limitations"`
}

func Catalog() []ProtocolCapability {
	return []ProtocolCapability{
		{Channel: "xiaomi", Steps: []string{"push"}, ProtocolMockVerified: true, Limitations: []string{"required icon and structured test account", "no API review/live evidence or lost-response reconciliation"}},
		{Channel: "tencent", Steps: []string{"authorize", "upload", "submit"}, ProtocolMockVerified: true, Limitations: []string{"main developer account only", "unversioned audit slot cannot identify target", "no lost-response reconciliation"}},
		{Channel: "huawei", Steps: []string{"authorize", "upload", "bind", "metadata", "submit"}, ProtocolMockVerified: true, Limitations: []string{"OBS host requires account evidence", "APK asynchronous processing waits at least two minutes", "no lost-response reconciliation"}},
		{Channel: "honor", Steps: []string{"authorize", "upload", "bind", "metadata", "submit"}, ProtocolMockVerified: true, Limitations: []string{"releaseId required for audit query", "unverified clear semantics blocked", "no lost-response reconciliation"}},
		{Channel: "oppo", Steps: []string{"authorize", "upload", "submit"}, ProtocolMockVerified: true, Limitations: []string{"single after_approval update requires frozen intent and preflight approvals", "publishing permission and actual upload host require account validation", "one-time upload sign has no documented TTL", "no lost-response reconciliation"}},
		{Channel: "vivo", Steps: []string{"upload", "submit"}, ProtocolMockVerified: true, Limitations: []string{"sign_method conflict requires account confirmation", "single APK only; separate 32/64 packages deferred", "no lost-response reconciliation"}},
	}
}
