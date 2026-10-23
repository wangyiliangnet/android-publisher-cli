package state_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type huaweiRenewalFixture struct {
	store    *state.Store
	snapshot manifest.Snapshot
	baseline protocol.RemoteSnapshot
	request  protocol.Request
	ref      string
}

func newHuaweiRenewalFixture(t *testing.T, mode, channel string) *huaweiRenewalFixture {
	t.Helper()
	root := t.TempDir()
	f, err := artifact.CreateSimulation(filepath.Join(t.TempDir(), "synthetic.apk"), artifact.Info{PackageName: "com.example.huaweirenewal", VersionCode: 248, VersionName: "3.2.7", ABI: []string{"arm64-v8a"}})
	must(t, err)
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "huawei-renewal-fixture", Mode: mode, PackageName: "com.example.huaweirenewal", VersionCode: 248, VersionName: "3.2.7", Artifacts: map[string]manifest.Artifact{"apk": f}, NotesFile: "notes", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{channel: {AccountRef: "fixture", AppRef: "com.example.huaweirenewal", Artifact: "apk"}}}
	snapshot := manifest.Snapshot{Manifest: m, Notes: "Frozen synthetic renewal notes\n"}
	snapshot.Digest = snapshot.CalculateDigest()
	must(t, artifact.Freeze(context.Background(), root, m, artifact.SimulationInspector{}))
	s, err := state.Open(root, mode, false)
	must(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Save(snapshot)
	must(t, err)
	targets, err := s.Targets(m.ReleaseID)
	must(t, err)
	baseline := protocol.RemoteSnapshot{Scope: targets[0].Scope, AdapterBinding: protocol.Digest("synthetic-huawei-binding"), Metadata: map[string]string{"existing_baseline_ref": "opaque-synthetic-baseline"}}
	must(t, s.Preflight(targets[0], baseline))
	q := protocol.Request{OperationID: protocol.Digest([]string{snapshot.Digest, channel, "authorize"}), TargetID: targets[0].ID, Scope: targets[0].Scope, Channel: channel, IntentDigest: snapshot.Digest, Kind: "authorize", PackageName: m.PackageName, VersionCode: m.VersionCode, VersionName: m.VersionName, ArtifactSHA256: f.SHA256, SnapshotDigest: protocol.Digest(baseline.Metadata), AdapterBinding: baseline.AdapterBinding, Metadata: map[string]string{"existing_baseline_ref": "opaque-synthetic-baseline", "release_notes": snapshot.Notes}, Policy: m.ReleasePolicy}
	ref := "material-" + account.Key(q.MaterialOperationKey())
	must(t, s.Begin(q))
	must(t, s.Complete(q, "confirmed_applied", ref, "synthetic original authorization error field"))
	return &huaweiRenewalFixture{s, snapshot, baseline, q, ref}
}

func (v *huaweiRenewalFixture) upload(t *testing.T, effect string) protocol.Request {
	t.Helper()
	q := v.request
	q.OperationID = protocol.Digest([]string{q.IntentDigest, q.Channel, "upload"})
	q.Kind, q.PreviousRef, q.Checkpoints = "upload", v.ref, map[string]string{"authorize": v.ref}
	q.AuthorizationGeneration = 0
	must(t, v.store.Begin(q))
	if effect != "intent" {
		ref := ""
		if effect == "confirmed_applied" {
			ref = "synthetic-object-id"
		}
		must(t, v.store.Complete(q, effect, ref, "synthetic unsent upload"))
	}
	return q
}

func TestHuaweiAuthorizationRenewalArchivesKnownOutcomesBeforeNewIntent(t *testing.T) {
	for _, withUpload := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocation-only", true: "known-unsent-upload"}[withUpload], func(t *testing.T) {
			v := newHuaweiRenewalFixture(t, manifest.Production, "huawei")
			var upload protocol.Request
			var uploadCreated string
			if withUpload {
				upload = v.upload(t, "confirmed_not_applied")
				must(t, v.store.DB.QueryRow("SELECT created_at FROM operations WHERE id=?", upload.OperationID).Scan(&uploadCreated))
			}
			var originalCreated string
			must(t, v.store.DB.QueryRow("SELECT created_at FROM operations WHERE id=?", v.request.OperationID).Scan(&originalCreated))
			renewed, err := v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID)
			must(t, err)
			if renewed.OperationID != v.request.OperationID || renewed.AuthorizationGeneration != 1 || protocol.Digest(renewed) == protocol.Digest(v.request) {
				t.Fatal("renewal did not retain operation identity with a distinct request generation")
			}
			op, err := v.store.Operation(v.request.TargetID, "authorize")
			must(t, err)
			if op.Effect != "intent" || op.Attempt != 2 || op.RemoteRef != "" || op.Error != "" || protocol.Digest(op.Request) != protocol.Digest(renewed) {
				t.Fatal("next authorization intent was not durable")
			}
			if _, err = v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID); err == nil {
				t.Fatal("unresolved renewed authorization was reset")
			}
			storedUpload, err := v.store.Operation(v.request.TargetID, "upload")
			must(t, err)
			if storedUpload != nil {
				t.Fatal("known-unsent upload row remained after its archival")
			}
			var digest, effect, ref, message, created, archived, policy string
			must(t, v.store.DB.QueryRow("SELECT request_digest,effect,remote_ref,error,created_at,archived_at,policy FROM authorization_attempts WHERE operation_id=? AND attempt=1", v.request.OperationID).Scan(&digest, &effect, &ref, &message, &created, &archived, &policy))
			if digest != protocol.Digest(v.request) || effect != "confirmed_applied" || ref != v.ref || created != originalCreated || archived == "" || policy != "huawei-expired-unused-upload-grant-v1" {
				t.Fatal("original authorization evidence was not archived exactly")
			}
			var audit struct {
				SchemaVersion    int              `json:"schema_version"`
				OriginalError    string           `json:"original_error"`
				AuthorizeRequest protocol.Request `json:"authorize_request"`
			}
			// Decode the private archive by its explicit JSON names below; the
			// public status projection intentionally never exposes this payload.
			var payload map[string]json.RawMessage
			must(t, json.Unmarshal([]byte(message), &payload))
			must(t, json.Unmarshal([]byte(message), &audit))
			if audit.SchemaVersion != 1 || audit.OriginalError != "synthetic original authorization error field" || protocol.Digest(audit.AuthorizeRequest) != protocol.Digest(v.request) {
				t.Fatal("private archive lost the original request or error")
			}
			if withUpload {
				var row struct {
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
				must(t, json.Unmarshal(payload["known_not_sent_upload"], &row))
				if row.OperationID != upload.OperationID || row.TargetID != upload.TargetID || row.Kind != upload.Kind || protocol.Digest(row.Request) != protocol.Digest(upload) || row.RequestDigest != protocol.Digest(upload) || row.Effect != "confirmed_not_applied" || row.RemoteRef != "" || row.Error != "synthetic unsent upload" || row.Attempt != 1 || row.CreatedAt != uploadCreated {
					t.Fatal("known-unsent upload audit did not retain the complete original row")
				}
			} else if _, ok := payload["known_not_sent_upload"]; ok {
				t.Fatal("allocation-only archive invented an upload row")
			}
			must(t, v.store.Complete(renewed, "confirmed_applied", "material-"+account.Key(renewed.MaterialOperationKey()), ""))
			second, err := v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID)
			must(t, err)
			if second.AuthorizationGeneration != 2 {
				t.Fatal("second bounded renewal lost generation")
			}
			must(t, v.store.Complete(second, "confirmed_applied", "material-"+account.Key(second.MaterialOperationKey()), ""))
			if _, err = v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID); err == nil {
				t.Fatal("fourth authorization allocation was allowed")
			}
			history, err := v.store.OPPOAuthorizationHistory(v.snapshot.Manifest.ReleaseID)
			must(t, err)
			if len(history) != 2 || history[0].Policy != policy || history[1].Policy != policy {
				t.Fatal("bounded renewal history was not retained in the existing schema")
			}
		})
	}
}

func TestHuaweiAuthorizationRenewalNeverResetsUnknownOrAppliedUpload(t *testing.T) {
	for _, effect := range []string{"intent", "unknown", "confirmed_applied", "confirmed_rejected"} {
		t.Run(effect, func(t *testing.T) {
			v := newHuaweiRenewalFixture(t, manifest.Production, "huawei")
			upload := v.upload(t, effect)
			before, err := v.store.Operation(v.request.TargetID, "upload")
			must(t, err)
			if _, err = v.store.CheckHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID); err == nil {
				t.Fatal("used or unresolved upload was eligible")
			}
			if _, err = v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID); err == nil {
				t.Fatal("used or unresolved upload was reset")
			}
			after, err := v.store.Operation(v.request.TargetID, "upload")
			must(t, err)
			if after == nil || protocol.Digest(after.Request) != protocol.Digest(upload) || after.Effect != before.Effect || after.RemoteRef != before.RemoteRef || after.Attempt != before.Attempt {
				t.Fatal("denied renewal altered an upload outcome")
			}
			var count int
			must(t, v.store.DB.QueryRow("SELECT COUNT(*) FROM authorization_attempts").Scan(&count))
			if count != 0 {
				t.Fatal("denied renewal archived a new attempt")
			}
		})
	}
}

func TestHuaweiAuthorizationRenewalArchivalAndKnownUnsentUploadDeletionAreAtomic(t *testing.T) {
	v := newHuaweiRenewalFixture(t, manifest.Production, "huawei")
	v.upload(t, "confirmed_not_applied")
	original, err := v.store.Operation(v.request.TargetID, "authorize")
	must(t, err)
	unsent, err := v.store.Operation(v.request.TargetID, "upload")
	must(t, err)
	_, err = v.store.DB.Exec(`CREATE TRIGGER reject_synthetic_renewal BEFORE UPDATE OF request ON operations
 WHEN NEW.kind='authorize' AND NEW.request<>OLD.request
 BEGIN SELECT RAISE(FAIL,'synthetic request persistence failure'); END`)
	must(t, err)
	if _, err = v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, v.request.OperationID); err == nil {
		t.Fatal("injected request persistence failure was ignored")
	}
	after, err := v.store.Operation(v.request.TargetID, "authorize")
	must(t, err)
	afterUpload, err := v.store.Operation(v.request.TargetID, "upload")
	must(t, err)
	var history int
	must(t, v.store.DB.QueryRow("SELECT COUNT(*) FROM authorization_attempts").Scan(&history))
	if protocol.Digest(after) != protocol.Digest(original) || protocol.Digest(afterUpload) != protocol.Digest(unsent) || history != 0 {
		t.Fatal("failed renewal partially removed the old upload or archived an uncommitted attempt")
	}
}

func TestHuaweiAuthorizationRenewalStrictFrozenAndScopeGates(t *testing.T) {
	for _, variant := range []string{"simulation", "channel", "wrong-id", "restore", "authorization-unknown", "authorization-intent", "generation", "foreign-ref", "metadata", "snapshot", "upload-checkpoint", "other-step", "other-release-unknown", "other-release-staged"} {
		t.Run(variant, func(t *testing.T) {
			mode, channel := manifest.Production, "huawei"
			if variant == "simulation" {
				mode = manifest.Simulation
			}
			if variant == "channel" {
				channel = "honor"
			}
			v := newHuaweiRenewalFixture(t, mode, channel)
			expected := v.request.OperationID
			switch variant {
			case "wrong-id":
				expected = protocol.Digest("wrong authorization")
			case "restore":
				_, err := v.store.DB.Exec("UPDATE meta SET value='1' WHERE key='recovery_required'")
				must(t, err)
			case "authorization-unknown", "authorization-intent":
				effect := "unknown"
				if variant == "authorization-intent" {
					effect = "intent"
				}
				_, err := v.store.DB.Exec("UPDATE operations SET effect=?", effect)
				must(t, err)
			case "generation", "metadata", "snapshot":
				q := v.request
				switch variant {
				case "generation":
					q.AuthorizationGeneration = 1
				case "metadata":
					q.Metadata["unreviewed"] = "change"
				case "snapshot":
					q.SnapshotDigest = protocol.Digest("foreign baseline")
				}
				b, err := json.Marshal(q)
				must(t, err)
				_, err = v.store.DB.Exec("UPDATE operations SET request=?,request_digest=?", string(b), protocol.Digest(q))
				must(t, err)
			case "foreign-ref":
				_, err := v.store.DB.Exec("UPDATE operations SET remote_ref=?", "material-"+account.Key("foreign"))
				must(t, err)
			case "upload-checkpoint":
				u := v.upload(t, "confirmed_not_applied")
				u.Checkpoints["authorize"] = "material-" + account.Key("foreign")
				b, err := json.Marshal(u)
				must(t, err)
				_, err = v.store.DB.Exec("UPDATE operations SET request=?,request_digest=? WHERE kind='upload'", string(b), protocol.Digest(u))
				must(t, err)
			case "other-step":
				u := v.upload(t, "confirmed_not_applied")
				_, err := v.store.DB.Exec("UPDATE operations SET kind='bind' WHERE id=?", u.OperationID)
				must(t, err)
			case "other-release-unknown", "other-release-staged":
				snapshot := v.snapshot
				snapshot.Manifest.ReleaseID = "other-huawei-renewal-release"
				snapshot.Notes += "another intent"
				snapshot.Digest = snapshot.CalculateDigest()
				_, err := v.store.Save(snapshot)
				must(t, err)
				q := v.request
				q.TargetID = snapshot.Manifest.ReleaseID + "/huawei"
				q.IntentDigest = snapshot.Digest
				q.OperationID = protocol.Digest([]string{snapshot.Digest, "huawei", "authorize"})
				b, err := json.Marshal(q)
				must(t, err)
				effect := "unknown"
				if variant == "other-release-staged" {
					effect = "confirmed_applied"
				}
				_, err = v.store.DB.Exec("INSERT INTO operations(id,target_id,kind,request,request_digest,effect,created_at) VALUES(?,?,?,?,?,?,?)", q.OperationID, q.TargetID, q.Kind, string(b), protocol.Digest(q), effect, "synthetic-existing-conflict")
				must(t, err)
			}
			before, err := v.store.Operation(v.request.TargetID, "authorize")
			must(t, err)
			_, checkErr := v.store.CheckHuaweiAuthorizationRenewal(v.request.TargetID, expected)
			_, beginErr := v.store.BeginHuaweiAuthorizationRenewal(v.request.TargetID, expected)
			if checkErr == nil || beginErr == nil {
				t.Fatal("invalid frozen authorization was renewed")
			}
			if variant == "restore" && (!errors.Is(checkErr, state.ErrRecovery) || !errors.Is(beginErr, state.ErrRecovery)) {
				t.Fatal("restored-state isolation was not preserved")
			}
			after, err := v.store.Operation(v.request.TargetID, "authorize")
			must(t, err)
			if protocol.Digest(after) != protocol.Digest(before) {
				t.Fatal("denied renewal changed the original authorization")
			}
		})
	}
}
