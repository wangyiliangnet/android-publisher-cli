package state

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

const oppoAuthorizationPolicy = "oppo-unused-upload-configuration-v1"
const oppoAuthorizationHistorySchema = `CREATE TABLE authorization_attempts (
 operation_id TEXT NOT NULL REFERENCES operations(id), attempt INTEGER NOT NULL,
 request_digest TEXT NOT NULL, effect TEXT NOT NULL, remote_ref TEXT NOT NULL,
 error TEXT NOT NULL, created_at TEXT NOT NULL, archived_at TEXT NOT NULL,
 policy TEXT NOT NULL, PRIMARY KEY(operation_id,attempt)); PRAGMA user_version=2;`

type OPPOAuthorizationAttempt struct {
	OperationID string `json:"operation_id"`
	Attempt     int    `json:"attempt"`
	Effect      string `json:"effect"`
	Policy      string `json:"replacement_policy"`
	ArchivedAt  string `json:"archived_at"`
}

type rowQuery interface{ QueryRow(string, ...any) *sql.Row }

// checkOPPOAuthorization permits replacing a disposable configuration only.
// An upload intent, even one known not applied, makes this path ineligible.
func (s *Store) checkOPPOAuthorization(db rowQuery, targetID, expected string) (*Operation, error) {
	if s.Mode != manifest.Production || !manifest.ValidHash(expected) {
		return nil, ErrConflict
	}
	var recovery string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='recovery_required'").Scan(&recovery); err != nil {
		return nil, err
	}
	if recovery != "0" {
		return nil, ErrRecovery
	}
	var data, digest, snapshotData, intent, scope, channel, preflight string
	var op Operation
	err := db.QueryRow(`SELECT o.request,o.request_digest,o.effect,o.remote_ref,o.error,o.attempt,r.snapshot,r.digest,t.scope,t.channel,t.preflight
 FROM operations o JOIN targets t ON t.id=o.target_id JOIN releases r ON r.id=t.release_id
 WHERE o.target_id=? AND o.id=? AND o.kind='authorize'`, targetID, expected).Scan(&data, &digest, &op.Effect, &op.RemoteRef, &op.Error, &op.Attempt, &snapshotData, &intent, &scope, &channel, &preflight)
	if err != nil {
		return nil, ErrConflict
	}
	var snapshot manifest.Snapshot
	var baseline protocol.RemoteSnapshot
	if json.Unmarshal([]byte(data), &op.Request) != nil || protocol.Digest(op.Request) != digest || json.Unmarshal([]byte(snapshotData), &snapshot) != nil || snapshot.Verify(manifest.Production) != nil || json.Unmarshal([]byte(preflight), &baseline) != nil {
		return nil, ErrConflict
	}
	q, m := op.Request, snapshot.Manifest
	if channel != "oppo" || len(m.Targets) != 1 || len(m.Metadata) != 0 || protocol.Digest(q.Policy) != protocol.Digest(m.ReleasePolicy) || m.ReleasePolicy.Mode != "after_approval" || q.Channel != "oppo" || q.Kind != "authorize" || q.Scope != scope || q.TargetID != targetID || q.IntentDigest != intent || snapshot.Digest != intent || q.OperationID != protocol.Digest([]string{intent, "oppo", "authorize"}) || q.PackageName != m.PackageName || q.VersionCode != m.VersionCode || q.VersionName != m.VersionName || q.ArtifactSHA256 != m.Artifacts[m.Targets["oppo"].Artifact].SHA256 || q.Metadata["release_notes"] != snapshot.Notes || q.PreviousRef != "" || len(q.Checkpoints) != 0 || q.SnapshotDigest != protocol.Digest(baseline.Metadata) || q.AdapterBinding != baseline.AdapterBinding || (op.Effect != "unknown" && op.Effect != "intent") || op.Attempt < 1 || op.Attempt >= 3 || op.RemoteRef != "" {
		return nil, ErrConflict
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM operations WHERE target_id=?", targetID).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, ErrConflict
	}
	// Preserve the normal application's ownership barrier for every other task.
	err = db.QueryRow(`SELECT COUNT(*) FROM operations o JOIN targets t ON t.id=o.target_id WHERE t.scope=? AND o.id<>? AND
 (o.effect IN ('intent','unknown') OR (t.id<>? AND o.effect='confirmed_applied' AND o.kind NOT IN ('submit','push') AND NOT EXISTS
 (SELECT 1 FROM operations final WHERE final.target_id=t.id AND final.effect='confirmed_applied' AND final.kind IN ('submit','push'))))`, scope, expected, targetID).Scan(&count)
	if err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, ErrConflict
	}
	return &op, nil
}

func (s *Store) CheckOPPOAuthorizationReplacement(targetID, expected string) (*Operation, error) {
	return s.checkOPPOAuthorization(s.DB, targetID, expected)
}

// BeginOPPOAuthorizationReplacement archives the old outcome verbatim. UNKNOWN
// is never rewritten as not-applied. The next attempt's intent is committed
// before the caller is allowed to request a new one-time configuration.
func (s *Store) BeginOPPOAuthorizationReplacement(targetID, expected string) (protocol.Request, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return protocol.Request{}, err
	}
	defer tx.Rollback()
	op, err := s.checkOPPOAuthorization(tx, targetID, expected)
	if err != nil {
		return protocol.Request{}, err
	}
	_, err = tx.Exec(`INSERT INTO authorization_attempts(operation_id,attempt,request_digest,effect,remote_ref,error,created_at,archived_at,policy)
 SELECT id,attempt,request_digest,effect,remote_ref,error,created_at,?,? FROM operations WHERE id=?`, now(), oppoAuthorizationPolicy, expected)
	if err != nil {
		return protocol.Request{}, err
	}
	_, err = tx.Exec("UPDATE operations SET effect='intent',attempt=attempt+1,error='',created_at=? WHERE id=?", now(), expected)
	if err != nil {
		return protocol.Request{}, err
	}
	if _, err = tx.Exec("UPDATE targets SET phase='submitting' WHERE id=?", targetID); err != nil {
		return protocol.Request{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.Request{}, err
	}
	return op.Request, nil
}

func (s *Store) OPPOAuthorizationHistory(releaseID string) ([]OPPOAuthorizationAttempt, error) {
	var version int
	if err := s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version < 2 {
		return nil, nil
	}
	rows, err := s.DB.Query(`SELECT a.operation_id,a.attempt,a.effect,a.policy,a.archived_at FROM authorization_attempts a
 JOIN operations o ON o.id=a.operation_id JOIN targets t ON t.id=o.target_id WHERE t.release_id=? ORDER BY a.attempt`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OPPOAuthorizationAttempt
	for rows.Next() {
		var a OPPOAuthorizationAttempt
		if err = rows.Scan(&a.OperationID, &a.Attempt, &a.Effect, &a.Policy, &a.ArchivedAt); err != nil {
			return nil, fmt.Errorf("invalid authorization history")
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
