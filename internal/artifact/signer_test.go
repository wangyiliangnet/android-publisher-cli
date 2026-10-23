package artifact

import (
	"strings"
	"testing"
)

func TestSignerCertificateToolFormats(t *testing.T) {
	const digest = "850d3df10877b2fb5ddf0fd86d8ef56d47e64d513c981e149f68af0e2afded44"
	legacy := "Signer #1 certificate SHA-256 digest: " + digest + "\n"
	// The scheme label is from the actual Build Tools 37.0.0 CI fixture output.
	current := "V3.0 Signer: certificate SHA-256 digest: " + strings.ToUpper(digest) + "\n"
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"legacy", "Number of signers: 1\n" + legacy, true},
		{"build_tools_37", "Number of signers: 1\n" + current, true},
		{"same_certificate_across_schemes", "Number of signers: 1\n" + current + "V2 Signer: certificate SHA-256 digest: " + digest + "\n", true},
		{"multiple_signers", "Number of signers: 2\n" + legacy, false},
		{"missing_count", legacy, false},
		{"conflicting_counts", "Number of signers: 1\nNumber of signers: 2\n" + legacy, false},
		{"different_certificates", "Number of signers: 1\n" + legacy + strings.ReplaceAll(current, strings.ToUpper(digest), strings.Repeat("a", 64)), false},
		{"second_legacy_signer", "Number of signers: 1\n" + legacy + strings.ReplaceAll(legacy, "#1", "#2"), false},
		{"unknown_label", "Number of signers: 1\nOther certificate SHA-256 digest: " + digest, false},
		{"malformed_digest", "Number of signers: 1\n" + strings.ReplaceAll(current, strings.ToUpper(digest), "abcd"), false},
		{"public_key_only", "Number of signers: 1\nSigner #1 public key SHA-256 digest: " + digest, false},
		{"source_stamp_only", "Number of signers: 1\nSource Stamp Signer certificate SHA-256 digest: " + digest, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSignerCertificate([]byte(tc.output))
			if tc.valid && (err != nil || got != digest) {
				t.Fatalf("got %q, %v; want %s", got, err, digest)
			}
			if !tc.valid && err == nil {
				t.Fatalf("unsupported signer evidence accepted: %q", got)
			}
		})
	}
}
