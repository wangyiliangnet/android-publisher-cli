package state

import (
	"encoding/json"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
)

const huaweiAuthorizationPolicy = "huawei-expired-unused-upload-grant-v1"

// The existing history table keeps the original authorization columns. This
// private payload additionally preserves its exact request and any upload row
// that is conclusively known not to have sent bytes. No unknown outcome is reset.
type huaweiAuthorizationArchive struct {
	SchemaVersion      int                   `json:"schema_version"`
	OriginalError      string                `json:"original_error"`
	AuthorizeRequest   protocol.Request      `json:"authorize_request"`
	KnownNotSentUpload *huaweiArchivedUpload `json:"known_not_sent_upload,omitempty"`
}

type huaweiArchivedUpload struct {
	OperationID   string           `json:"operation_id"`
	TargetID      string           `json:"target_id"`
	Kind          string           `json:"kind"`
	Request       protocol.Request `json:"request"`
	RequestDigest string           `json:"request_digest"`
	Effect        string           `json:"effect"`
	RemoteRef     string           `json:"remote_ref"`
	Error         string           `json:"error"`
	Attempt       int              `json:"attempt"`
	CreatedAt     string           `json:"created_at"`
}

type huaweiAuthorizationEligibility struct {
	Operation
	RequestDigest string
	CreatedAt     string
	Upload        *huaweiArchivedUpload
}

func (s *Store) checkHuaweiAuthorizationRenewal(db rowQuery, targetID, expected string) (*huaweiAuthorizationEligibility, error) {
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
	var requestData, snapshotData, intent, scope, channel, preflight string
	var eligible huaweiAuthorizationEligibility
	err := db.QueryRow(`SELECT o.request,o.request_digest,o.effect,o.remote_ref,o.error,o.attempt,o.created_at,r.snapshot,r.digest,t.scope,t.channel,t.preflight
 FROM operations o JOIN targets t ON t.id=o.target_id JOIN releases r ON r.id=t.release_id
 WHERE o.target_id=? AND o.id=? AND o.kind='authorize'`, targetID, expected).Scan(&requestData, &eligible.RequestDigest, &eligible.Effect, &eligible.RemoteRef, &eligible.Error, &eligible.Attempt, &eligible.CreatedAt, &snapshotData, &intent, &scope, &channel, &preflight)
	if err != nil {
		return nil, ErrConflict
	}
	var snapshot manifest.Snapshot
	var baseline protocol.RemoteSnapshot
	if json.Unmarshal([]byte(requestData), &eligible.Request) != nil || protocol.Digest(eligible.Request) != eligible.RequestDigest || json.Unmarshal([]byte(snapshotData), &snapshot) != nil || snapshot.Verify(manifest.Production) != nil || json.Unmarshal([]byte(preflight), &baseline) != nil {
		return nil, ErrConflict
	}
	q, m := eligible.Request, snapshot.Manifest
	target, ok := m.Targets["huawei"]
	if !ok || channel != "huawei" || len(m.Targets) != 1 || len(m.Metadata) != 0 || m.ReleasePolicy.Mode != "after_approval" || q.Policy != m.ReleasePolicy || snapshot.Digest != intent || targetID != m.ReleaseID+"/huawei" || scope != Scope("huawei", target, m.PackageName) || q.Scope != scope || q.TargetID != targetID || q.IntentDigest != intent || q.OperationID != expected || q.OperationID != protocol.Digest([]string{intent, "huawei", "authorize"}) || q.Channel != "huawei" || q.Kind != "authorize" || q.PackageName != m.PackageName || q.VersionCode != m.VersionCode || q.VersionName != m.VersionName || q.ArtifactSHA256 != m.Artifacts[target.Artifact].SHA256 || q.PreviousRef != "" || len(q.Checkpoints) != 0 || baseline.Scope != scope || baseline.ActiveIntent != "" || baseline.Metadata == nil || !manifest.ValidHash(baseline.AdapterBinding) || q.AdapterBinding != baseline.AdapterBinding || q.SnapshotDigest != protocol.Digest(baseline.Metadata) || eligible.Effect != "confirmed_applied" || eligible.Attempt < 1 || eligible.Attempt >= 3 || q.AuthorizationGeneration != eligible.Attempt-1 || !q.ValidAuthorizationGeneration() || eligible.RemoteRef != "material-"+account.Key(q.MaterialOperationKey()) {
		return nil, ErrConflict
	}
	expectedMetadata := manifest.ApplyPatch(baseline.Metadata, m.Metadata)
	expectedMetadata["release_notes"] = snapshot.Notes
	if protocol.Digest(q.Metadata) != protocol.Digest(expectedMetadata) {
		return nil, ErrConflict
	}
	var count, history, matchingPolicy int
	if err = db.QueryRow("SELECT COUNT(*) FROM operations WHERE target_id=?", targetID).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 && count != 2 {
		return nil, ErrConflict
	}
	if err = db.QueryRow("SELECT COUNT(*),COALESCE(SUM(CASE WHEN policy=? THEN 1 ELSE 0 END),0) FROM authorization_attempts WHERE operation_id=?", huaweiAuthorizationPolicy, expected).Scan(&history, &matchingPolicy); err != nil {
		return nil, err
	}
	if history != eligible.Attempt-1 || matchingPolicy != history {
		return nil, ErrConflict
	}
	if count == 2 {
		var upload huaweiArchivedUpload
		var uploadData string
		err = db.QueryRow("SELECT id,target_id,kind,request,request_digest,effect,remote_ref,error,attempt,created_at FROM operations WHERE target_id=? AND kind='upload'", targetID).Scan(&upload.OperationID, &upload.TargetID, &upload.Kind, &uploadData, &upload.RequestDigest, &upload.Effect, &upload.RemoteRef, &upload.Error, &upload.Attempt, &upload.CreatedAt)
		if err != nil || json.Unmarshal([]byte(uploadData), &upload.Request) != nil || protocol.Digest(upload.Request) != upload.RequestDigest {
			return nil, ErrConflict
		}
		u := upload.Request
		if upload.Effect != "confirmed_not_applied" || upload.RemoteRef != "" || upload.Attempt < 1 || upload.OperationID != protocol.Digest([]string{intent, "huawei", "upload"}) || upload.TargetID != targetID || upload.Kind != "upload" || u.OperationID != upload.OperationID || u.TargetID != targetID || u.Scope != q.Scope || u.Channel != q.Channel || u.Kind != "upload" || u.IntentDigest != q.IntentDigest || u.PackageName != q.PackageName || u.VersionCode != q.VersionCode || u.VersionName != q.VersionName || u.ArtifactSHA256 != q.ArtifactSHA256 || u.Policy != q.Policy || u.AuthorizationGeneration != 0 || u.AdapterBinding != q.AdapterBinding || u.SnapshotDigest != q.SnapshotDigest || protocol.Digest(u.Metadata) != protocol.Digest(q.Metadata) || u.PreviousRef != eligible.RemoteRef || len(u.Checkpoints) != 1 || u.Checkpoints["authorize"] != eligible.RemoteRef {
			return nil, ErrConflict
		}
		eligible.Upload = &upload
	}
	// Preserve the same ownership and restored-state barriers as an ordinary
	// business write, including another release with an unfinished staging chain.
	if err = checkWritable(db, scope, targetID); err != nil {
		return nil, err
	}
	return &eligible, nil
}

// CheckHuaweiAuthorizationRenewal only checks durable eligibility. The caller
// must separately verify the sealed grant has expired and has never been used.
func (s *Store) CheckHuaweiAuthorizationRenewal(targetID, expected string) (*Operation, error) {
	eligible, err := s.checkHuaweiAuthorizationRenewal(s.DB, targetID, expected)
	if err != nil {
		return nil, err
	}
	return &eligible.Operation, nil
}

// BeginHuaweiAuthorizationRenewal archives known outcomes in one transaction
// before allocating another grant. It never removes an intent or unknown write.
func (s *Store) BeginHuaweiAuthorizationRenewal(targetID, expected string) (protocol.Request, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return protocol.Request{}, err
	}
	defer tx.Rollback()
	eligible, err := s.checkHuaweiAuthorizationRenewal(tx, targetID, expected)
	if err != nil {
		return protocol.Request{}, err
	}
	archive, err := json.Marshal(huaweiAuthorizationArchive{SchemaVersion: 1, OriginalError: eligible.Error, AuthorizeRequest: eligible.Request, KnownNotSentUpload: eligible.Upload})
	if err != nil {
		return protocol.Request{}, err
	}
	_, err = tx.Exec(`INSERT INTO authorization_attempts(operation_id,attempt,request_digest,effect,remote_ref,error,created_at,archived_at,policy)
 VALUES(?,?,?,?,?,?,?,?,?)`, expected, eligible.Attempt, eligible.RequestDigest, eligible.Effect, eligible.RemoteRef, string(archive), eligible.CreatedAt, now(), huaweiAuthorizationPolicy)
	if err != nil {
		return protocol.Request{}, err
	}
	if eligible.Upload != nil {
		result, err := tx.Exec("DELETE FROM operations WHERE id=? AND target_id=? AND kind='upload' AND effect='confirmed_not_applied' AND request_digest=?", eligible.Upload.OperationID, targetID, eligible.Upload.RequestDigest)
		if err != nil {
			return protocol.Request{}, err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return protocol.Request{}, ErrConflict
		}
	}
	q := eligible.Request
	q.AuthorizationGeneration++
	b, err := json.Marshal(q)
	if err != nil {
		return protocol.Request{}, err
	}
	result, err := tx.Exec("UPDATE operations SET request=?,request_digest=?,effect='intent',attempt=attempt+1,remote_ref='',error='',created_at=? WHERE id=? AND target_id=? AND effect='confirmed_applied' AND request_digest=?", string(b), protocol.Digest(q), now(), expected, targetID, eligible.RequestDigest)
	if err != nil {
		return protocol.Request{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return protocol.Request{}, ErrConflict
	}
	if _, err = tx.Exec("UPDATE targets SET phase='submitting' WHERE id=?", targetID); err != nil {
		return protocol.Request{}, err
	}
	if err = tx.Commit(); err != nil {
		return protocol.Request{}, err
	}
	return q, nil
}
