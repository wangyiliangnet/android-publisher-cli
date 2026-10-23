package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

// ConsoleEvidence is a human attestation, never an API or browser verification.
// The operator fills observed identity separately from the frozen expectation.
type ConsoleIdentity struct {
	Channel       string `json:"channel"`
	Account       string `json:"account_ref"`
	PackageName   string `json:"package_name"`
	VersionCode   int64  `json:"version_code"`
	VersionName   string `json:"version_name"`
	SubmissionRef string `json:"submission_ref"`
}
type ConsoleEvidence struct {
	SchemaVersion int             `json:"schema_version"`
	ReleaseID     string          `json:"release_id"`
	IntentDigest  string          `json:"intent_digest"`
	Expected      ConsoleIdentity `json:"expected"`
	Observed      ConsoleIdentity `json:"observed"`
	Review        string          `json:"review_state"`
	Availability  string          `json:"availability_state"`
	CollectedAt   string          `json:"collected_at"`
	CollectedBy   string          `json:"collected_by"`
	SourceURL     string          `json:"source_url"`
	Confirmed     bool            `json:"confirmed_exact_target_in_console"`
}

func consoleIdentity(snapshot manifest.Snapshot, t Target) ConsoleIdentity {
	return ConsoleIdentity{Channel: t.Channel, Account: snapshot.Manifest.Targets[t.Channel].AccountRef, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, SubmissionRef: t.RemoteRef}
}
func (s *Store) ConsoleTemplate(id, channel string) (ConsoleEvidence, error) {
	snapshot, err := s.Load(id)
	if err != nil {
		return ConsoleEvidence{}, err
	}
	t, err := s.Target(id + "/" + channel)
	if err != nil || t.Phase != "submitted" || t.RemoteRef == "" {
		return ConsoleEvidence{}, fmt.Errorf("console evidence requires an accepted target")
	}
	return ConsoleEvidence{SchemaVersion: 1, ReleaseID: id, IntentDigest: snapshot.Digest, Expected: consoleIdentity(snapshot, t), Review: "unknown", Availability: "unknown"}, nil
}
func (s *Store) ImportConsoleEvidence(e ConsoleEvidence) error {
	release, err := s.BeginCommand()
	if err != nil {
		return err
	}
	defer release()
	template, err := s.ConsoleTemplate(e.ReleaseID, e.Expected.Channel)
	if err != nil {
		return err
	}
	if e.SchemaVersion != 1 || e.IntentDigest != template.IntentDigest || e.Expected != template.Expected || e.Observed != e.Expected || !e.Confirmed {
		return fmt.Errorf("console evidence does not attest the exact frozen target")
	}
	at, err := time.Parse(time.RFC3339Nano, e.CollectedAt)
	if err != nil || at.After(time.Now().Add(time.Minute)) || strings.TrimSpace(e.CollectedBy) == "" || len(e.CollectedBy) > 80 || strings.ContainsAny(e.CollectedBy, "\r\n\x00") {
		return fmt.Errorf("console evidence needs a valid collector and collection time")
	}
	u, err := url.Parse(e.SourceURL)
	hosts := map[string][]string{"xiaomi": {"dev.mi.com"}, "tencent": {"app.open.qq.com", "open.qq.com"}, "oppo": {"open.oppomobile.com"}, "vivo": {"dev.vivo.com.cn"}, "huawei": {"developer.huawei.com", "developer.huawei.com.cn"}, "honor": {"developer.honor.com"}}
	allowed := false
	if err == nil {
		for _, host := range hosts[e.Expected.Channel] {
			if u.Host == host {
				allowed = true
			}
		}
	}
	if !allowed || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return fmt.Errorf("use the official console URL without credentials, query or fragment")
	}
	t, err := s.Target(e.ReleaseID + "/" + e.Expected.Channel)
	if err != nil {
		return err
	}
	kind := "submit"
	if t.Channel == "xiaomi" {
		kind = "push"
	}
	op, err := s.Operation(t.ID, kind)
	if err != nil || op == nil || op.Effect != "confirmed_applied" || op.RemoteRef != t.RemoteRef || op.Request.IntentDigest != e.IntentDigest {
		return fmt.Errorf("matching final submission receipt is required")
	}
	var created string
	if err = s.DB.QueryRow("SELECT created_at FROM operations WHERE id=?", op.Request.OperationID).Scan(&created); err != nil {
		return err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil || at.Before(createdAt) {
		return fmt.Errorf("console evidence predates submission")
	}
	var frozen protocol.RemoteSnapshot
	if json.Unmarshal([]byte(t.Preflight), &frozen) != nil || (s.Mode == manifest.Production && frozen.AdapterBinding == "") {
		return fmt.Errorf("frozen preflight binding is required")
	}
	o := protocol.Observation{Scope: t.Scope, AdapterBinding: frozen.AdapterBinding, VersionCode: e.Expected.VersionCode, RemoteRef: t.RemoteRef, Review: e.Review, Availability: e.Availability, Source: "manual_console", CollectedAt: at}
	if err = o.Validate(t.Scope, e.Expected.VersionCode, t.RemoteRef); err != nil {
		return err
	}
	if e.Review == "unknown" && e.Availability == "unknown" {
		return fmt.Errorf("no target state was observed")
	}
	data, _ := json.Marshal(e)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	ref := protocol.Evidence{Kind: "text", Path: "evidence/" + digest + "/evidence.json", SHA256: digest, Size: int64(len(data))}
	if last, parseErr := time.Parse(time.RFC3339Nano, t.LastSuccessAt); parseErr == nil && (at.Before(last) || (at.Equal(last) && (o.Review != t.Review || o.Availability != t.Availability))) {
		return fmt.Errorf("console evidence is older than or conflicts with current evidence")
	}
	err = withEvidenceFile(s.Root, ref, true, func(f *os.File) error {
		if _, err := f.Write(data); err != nil {
			return err
		}
		return f.Sync()
	})
	if errors.Is(err, os.ErrExist) {
		_, err = readEvidence(s.Root, ref)
	}
	if err != nil {
		return err
	}
	o.Evidence = []protocol.Evidence{ref}
	// Single-text manual evidence is a new reader contract. Reject old binaries
	// rather than allowing their backup validator to omit this provenance.
	if _, err = s.DB.Exec("PRAGMA user_version=3"); err != nil {
		return err
	}
	generation, err := s.BeginObservation(t.ID)
	if err != nil {
		return err
	}
	return s.Observe(t, generation, e.Expected.VersionCode, o, nil)
}
