package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	_ "modernc.org/sqlite"
)

var ErrBusy = errors.New("publisher is busy; retry after the active command finishes")
var ErrRecovery = errors.New("restored state requires account/application reconciliation; business writes are isolated")
var ErrConflict = errors.New("an unresolved operation already owns this account/application")
var ErrReleaseNotFound = errors.New("release was not found in local state")

type Store struct {
	DB        *sql.DB
	Root      string
	Mode      string
	locks     []*os.File
	commandMu sync.Mutex
}

// BeginCommand complements the OS writer lock: sharing one Store must not let a
// second executor reconcile an intent whose first executor is still running.
func (s *Store) BeginCommand() (func(), error) {
	if !s.commandMu.TryLock() {
		return nil, ErrBusy
	}
	return s.commandMu.Unlock, nil
}

const schema = `
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE releases (id TEXT PRIMARY KEY, digest TEXT NOT NULL UNIQUE, snapshot TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE TABLE targets (id TEXT PRIMARY KEY, release_id TEXT NOT NULL REFERENCES releases(id), channel TEXT NOT NULL, scope TEXT NOT NULL, phase TEXT NOT NULL DEFAULT 'planned', review TEXT NOT NULL DEFAULT 'unknown', availability TEXT NOT NULL DEFAULT 'unknown', remote_ref TEXT NOT NULL DEFAULT '', preflight TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL DEFAULT 0, projection_generation INTEGER NOT NULL DEFAULT 0, last_success_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', query_failures INTEGER NOT NULL DEFAULT 0, next_check_at TEXT NOT NULL DEFAULT '', disputed INTEGER NOT NULL DEFAULT 0, UNIQUE(release_id,channel));
CREATE TABLE operations (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, target_id TEXT NOT NULL REFERENCES targets(id), kind TEXT NOT NULL, request TEXT NOT NULL, request_digest TEXT NOT NULL, effect TEXT NOT NULL CHECK(effect IN ('intent','unknown','confirmed_applied','confirmed_rejected','confirmed_not_applied')), remote_ref TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', attempt INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL, UNIQUE(target_id,kind));
CREATE TABLE observations (id INTEGER PRIMARY KEY, target_id TEXT NOT NULL REFERENCES targets(id), generation INTEGER NOT NULL, document TEXT NOT NULL, created_at TEXT NOT NULL);
CREATE INDEX scope_operations ON targets(scope);
PRAGMA user_version=1;`

func lock(root, name string, kind int) (*os.File, error) {
	f, err := privatefs.Open(filepath.Join(root, name), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), kind|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return f, nil
}

func Open(root, mode string, readOnly bool) (s *Store, err error) {
	if mode != manifest.Production && mode != manifest.Simulation {
		return nil, fmt.Errorf("invalid store mode")
	}
	if _, e := os.Stat(filepath.Join(root, "fake.sqlite")); e == nil {
		return nil, fmt.Errorf("publisher state must be separate from the fake store")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = privatefs.Dir(root); err != nil {
		return nil, err
	}
	s = &Store{Root: root, Mode: mode}
	opened := s
	defer func() {
		if err != nil {
			opened.Close()
		}
	}()
	f, err := lock(root, ".lifecycle.lock", syscall.LOCK_SH)
	if err != nil {
		return nil, err
	}
	s.locks = append(s.locks, f)
	if !readOnly {
		f, err = lock(root, ".writer.lock", syscall.LOCK_EX)
		if err != nil {
			return nil, err
		}
		s.locks = append(s.locks, f)
	}
	path := filepath.Join(root, "state.sqlite")
	flags := os.O_RDWR | os.O_CREATE
	if readOnly {
		flags = os.O_RDONLY
	}
	f, err = privatefs.Open(path, flags)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	f.Close()
	if err != nil {
		return nil, err
	}
	if err = restrictSidecars(path); err != nil {
		return nil, err
	}
	dsn := path
	if readOnly {
		dsn = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: "mode=ro"}).String()
	}
	s.DB, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.DB.SetMaxOpenConns(1)
	if _, err = s.DB.Exec("PRAGMA busy_timeout=1000; PRAGMA foreign_keys=ON;"); err != nil {
		return nil, err
	}
	var version int
	if err = s.DB.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version > 3 || version < 0 || (readOnly && version == 0) {
		return nil, fmt.Errorf("unsupported database schema %d", version)
	}
	if !readOnly {
		if _, err = s.DB.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); err != nil {
			return nil, err
		}
		if version == 0 {
			var tx *sql.Tx
			tx, err = s.DB.Begin()
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			if _, err = tx.Exec(schema); err == nil {
				_, err = tx.Exec("INSERT INTO meta(key,value) VALUES ('mode',?),('recovery_required','0')", mode)
			}
			if err == nil {
				err = tx.Commit()
			}
			if err != nil {
				return nil, err
			}
		}
		if version < 2 {
			tx, e := s.DB.Begin()
			if e != nil {
				return nil, e
			}
			defer tx.Rollback()
			if _, e = tx.Exec(oppoAuthorizationHistorySchema); e == nil {
				e = tx.Commit()
			}
			if e != nil {
				return nil, e
			}
		}
	}
	var storedMode string
	if err = s.DB.QueryRow("SELECT value FROM meta WHERE key='mode'").Scan(&storedMode); err != nil {
		return nil, err
	}
	if storedMode != mode {
		return nil, fmt.Errorf("production and simulation state cannot be mixed")
	}
	if err = restrictSidecars(path); err != nil {
		return nil, err
	}
	return s, nil
}
func restrictSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		f, err := privatefs.Open(path+suffix, os.O_RDONLY)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = f.Chmod(0600)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Close() error {
	var err error
	if s == nil {
		return nil
	}
	if s.DB != nil {
		err = s.DB.Close()
	}
	for i := len(s.locks) - 1; i >= 0; i-- {
		syscall.Flock(int(s.locks[i].Fd()), syscall.LOCK_UN)
		s.locks[i].Close()
	}
	s.locks = nil
	return err
}
func Scope(channel string, t manifest.Target, packageName string) string {
	return protocol.Digest([]string{channel, t.AccountRef, t.AppRef, packageName})
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) Save(snapshot manifest.Snapshot) (string, error) {
	if err := snapshot.Verify(s.Mode); err != nil {
		return "", err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var digest, id string
	err = tx.QueryRow("SELECT id,digest FROM releases WHERE id=?", snapshot.Manifest.ReleaseID).Scan(&id, &digest)
	if err == nil {
		if digest != snapshot.Digest {
			return "", fmt.Errorf("release_id already has a different immutable intent")
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	err = tx.QueryRow("SELECT id FROM releases WHERE digest=?", snapshot.Digest).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	id = snapshot.Manifest.ReleaseID
	if _, err = tx.Exec("INSERT INTO releases VALUES (?,?,?,?)", id, snapshot.Digest, string(data), now()); err != nil {
		return "", err
	}
	for channel, t := range snapshot.Manifest.Targets {
		if _, err = tx.Exec("INSERT INTO targets(id,release_id,channel,scope) VALUES (?,?,?,?)", id+"/"+channel, id, channel, Scope(channel, t, snapshot.Manifest.PackageName)); err != nil {
			return "", err
		}
	}
	return id, tx.Commit()
}
func (s *Store) Load(id string) (manifest.Snapshot, error) {
	var data string
	var snapshot manifest.Snapshot
	if err := s.DB.QueryRow("SELECT snapshot FROM releases WHERE id=?", id).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return snapshot, ErrReleaseNotFound
		}
		return snapshot, err
	}
	if err := json.Unmarshal([]byte(data), &snapshot); err != nil {
		return snapshot, fmt.Errorf("invalid saved snapshot")
	}
	return snapshot, snapshot.Verify(s.Mode)
}

type Target struct {
	ID                      string                   `json:"target_id"`
	ReleaseID               string                   `json:"release_id"`
	Channel                 string                   `json:"channel"`
	Scope                   string                   `json:"scope"`
	Phase                   string                   `json:"execution_phase"`
	Review                  string                   `json:"review_state"`
	Availability            string                   `json:"availability_state"`
	RemoteRef               string                   `json:"remote_ref"`
	Preflight               string                   `json:"-"`
	Generation              int64                    `json:"generation"`
	ProjectionGeneration    int64                    `json:"projection_generation"`
	LastSuccessAt           string                   `json:"last_success_at"`
	LastError               string                   `json:"last_error"`
	QueryFailures           int                      `json:"query_failures"`
	NextCheckAt             string                   `json:"next_check_at"`
	Disputed                bool                     `json:"evidence_disputed"`
	ReviewSource            string                   `json:"review_source,omitempty"`
	ReviewObservedAt        string                   `json:"review_observed_at,omitempty"`
	AvailabilitySource      string                   `json:"availability_source,omitempty"`
	AvailabilityObservedAt  string                   `json:"availability_observed_at,omitempty"`
	Context                 *protocol.ChannelContext `json:"channel_context,omitempty"`
	ContextObservedAt       string                   `json:"channel_context_observed_at,omitempty"`
	CachedContext           *protocol.ChannelContext `json:"cached_channel_context,omitempty"`
	CachedContextObservedAt string                   `json:"cached_channel_context_observed_at,omitempty"`
}

const targetColumns = "id,release_id,channel,scope,phase,review,availability,remote_ref,preflight,generation,projection_generation,last_success_at,last_error,query_failures,next_check_at,disputed"

func scanTarget(scanner interface{ Scan(...any) error }) (Target, error) {
	var t Target
	err := scanner.Scan(&t.ID, &t.ReleaseID, &t.Channel, &t.Scope, &t.Phase, &t.Review, &t.Availability, &t.RemoteRef, &t.Preflight, &t.Generation, &t.ProjectionGeneration, &t.LastSuccessAt, &t.LastError, &t.QueryFailures, &t.NextCheckAt, &t.Disputed)
	return t, err
}
func (s *Store) Targets(id string) ([]Target, error) {
	rows, err := s.DB.Query("SELECT "+targetColumns+" FROM targets WHERE release_id=? ORDER BY channel", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Target{}
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return nil, rowErr
	}
	for i := range result {
		if err := s.attachObservationSources(&result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *Store) Target(id string) (Target, error) {
	t, err := scanTarget(s.DB.QueryRow("SELECT "+targetColumns+" FROM targets WHERE id=?", id))
	if err == nil {
		err = s.attachObservationSources(&t)
	}
	return t, err
}

func (s *Store) attachObservationSources(t *Target) error {
	return attachObservationSources(s.DB, t)
}

func attachObservationSources(q interface{ QueryRow(string, ...any) *sql.Row }, t *Target) error {
	for _, dimension := range []struct {
		key, value string
		source, at *string
	}{
		{"review_state", t.Review, &t.ReviewSource, &t.ReviewObservedAt},
		{"availability_state", t.Availability, &t.AvailabilitySource, &t.AvailabilityObservedAt},
	} {
		if dimension.value == "unknown" {
			continue
		}
		err := q.QueryRow("SELECT json_extract(document,'$.observation.source'),json_extract(document,'$.observation.collected_at') FROM observations WHERE target_id=? AND generation<=? AND json_extract(document,'$.valid')=1 AND json_extract(document,?)=? ORDER BY generation DESC,id DESC LIMIT 1", t.ID, t.ProjectionGeneration, "$.observation."+dimension.key, dimension.value).Scan(dimension.source, dimension.at)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	// Context describes the channel's current, unassociated facts. Only an
	// accepted observation may publish it; failed reads and stale generations
	// keep the prior facts with their original timestamp.
	var document string
	var contextGeneration int64
	err := q.QueryRow("SELECT json_extract(document,'$.observation.channel_context'),json_extract(document,'$.observation.collected_at'),generation FROM observations WHERE target_id=? AND generation<=? AND json_extract(document,'$.valid')=1 AND json_extract(document,'$.projected')=1 AND json_type(document,'$.observation.channel_context')='object' ORDER BY generation DESC,id DESC LIMIT 1", t.ID, t.ProjectionGeneration).Scan(&document, &t.ContextObservedAt, &contextGeneration)
	if err == nil {
		var c protocol.ChannelContext
		if json.Unmarshal([]byte(document), &c) != nil || c.Validate() != nil {
			return fmt.Errorf("invalid saved channel context")
		}
		t.Context = &c
		if c.QueryStatus() != "observed" {
			var cached string
			err := q.QueryRow("SELECT json_extract(document,'$.observation.channel_context'),json_extract(document,'$.observation.collected_at') FROM observations WHERE target_id=? AND generation<? AND json_extract(document,'$.valid')=1 AND json_extract(document,'$.projected')=1 AND (json_extract(document,'$.observation.channel_context.api_query_health')='healthy' OR json_extract(document,'$.observation.channel_context.store_query_health')='healthy') ORDER BY generation DESC,id DESC LIMIT 1", t.ID, contextGeneration).Scan(&cached, &t.CachedContextObservedAt)
			if err == nil {
				var previous protocol.ChannelContext
				if json.Unmarshal([]byte(cached), &previous) != nil || previous.Validate() != nil {
					return fmt.Errorf("invalid saved cached channel context")
				}
				t.CachedContext = &previous
			} else if err != sql.ErrNoRows {
				return err
			}
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	return nil
}
func (s *Store) CheckWritable(scope, targetID string) error {
	return checkWritable(s.DB, scope, targetID)
}
func checkWritable(q interface{ QueryRow(string, ...any) *sql.Row }, scope, targetID string) error {
	var recovery string
	if err := q.QueryRow("SELECT value FROM meta WHERE key='recovery_required'").Scan(&recovery); err != nil {
		return err
	}
	if recovery != "0" {
		return ErrRecovery
	}
	var floorData string
	if err := q.QueryRow("SELECT value FROM meta WHERE key='recovery_version_floors'").Scan(&floorData); err == nil {
		var floors map[string]int64
		if json.Unmarshal([]byte(floorData), &floors) != nil {
			return ErrRecovery
		}
		if floor, ok := floors[scope]; ok {
			var snapshotData string
			if err := q.QueryRow("SELECT r.snapshot FROM releases r JOIN targets t ON t.release_id=r.id WHERE t.id=? AND t.scope=?", targetID, scope).Scan(&snapshotData); err != nil {
				return err
			}
			var snapshot manifest.Snapshot
			if json.Unmarshal([]byte(snapshotData), &snapshot) != nil || snapshot.Manifest.VersionCode <= floor {
				return fmt.Errorf("%w: version is at or below the externally reviewed recovery floor", ErrRecovery)
			}
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	var count int
	err := q.QueryRow(`SELECT COUNT(*) FROM operations o JOIN targets t ON t.id=o.target_id WHERE t.scope=? AND (o.effect IN ('intent','unknown') OR (t.id<>? AND o.effect='confirmed_applied' AND o.kind NOT IN ('submit','push') AND NOT EXISTS(SELECT 1 FROM operations final WHERE final.target_id=t.id AND final.effect='confirmed_applied' AND final.kind IN ('submit','push'))))`, scope, targetID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrConflict
	}
	return nil
}
func (s *Store) Preflight(t Target, snapshot protocol.RemoteSnapshot) error {
	if snapshot.Scope != t.Scope || snapshot.Metadata == nil {
		return fmt.Errorf("invalid remote snapshot")
	}
	data, _ := json.Marshal(snapshot)
	_, err := s.DB.Exec("UPDATE targets SET preflight=?,phase=CASE WHEN phase='planned' THEN 'ready' ELSE phase END WHERE id=? AND NOT EXISTS(SELECT 1 FROM operations WHERE target_id=targets.id)", string(data), t.ID)
	return err
}

type Operation struct {
	Request   protocol.Request `json:"request"`
	Effect    string           `json:"effect"`
	RemoteRef string           `json:"remote_ref"`
	Error     string           `json:"error"`
	Attempt   int              `json:"attempt"`
}

type OperationStatus struct {
	Sequence    int64  `json:"sequence"`
	OperationID string `json:"operation_id"`
	Channel     string `json:"channel"`
	Kind        string `json:"operation_kind"`
	Phase       string `json:"phase"`
	Effect      string `json:"effect"`
	RemoteRef   string `json:"remote_ref"`
	Attempt     int    `json:"attempt"`
	NextAction  string `json:"next_action"`
}

func (s *Store) OperationStatuses(releaseID string) ([]OperationStatus, error) {
	rows, err := s.DB.Query("SELECT o.sequence,o.id,t.channel,o.kind,t.phase,o.effect,o.remote_ref,o.attempt FROM operations o JOIN targets t ON t.id=o.target_id WHERE t.release_id=? ORDER BY o.sequence", releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OperationStatus{}
	for rows.Next() {
		var o OperationStatus
		if err := rows.Scan(&o.Sequence, &o.OperationID, &o.Channel, &o.Kind, &o.Phase, &o.Effect, &o.RemoteRef, &o.Attempt); err != nil {
			return nil, err
		}
		switch o.Effect {
		case "intent", "unknown":
			o.NextAction = "reconcile"
		case "confirmed_rejected":
			o.NextAction = "fix_config"
		case "confirmed_not_applied":
			o.NextAction = "resume"
		default:
			if o.Phase == "submitted" {
				o.NextAction = "observe"
			} else {
				o.NextAction = "resume"
			}
		}
		result = append(result, o)
	}
	return result, rows.Err()
}

func (s *Store) Operation(targetID, kind string) (*Operation, error) {
	var op Operation
	var data, digest string
	err := s.DB.QueryRow("SELECT request,request_digest,effect,remote_ref,error,attempt FROM operations WHERE target_id=? AND kind=?", targetID, kind).Scan(&data, &digest, &op.Effect, &op.RemoteRef, &op.Error, &op.Attempt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(data), &op.Request); err != nil || protocol.Digest(op.Request) != digest {
		return nil, fmt.Errorf("corrupt operation request")
	}
	return &op, nil
}
func (s *Store) Begin(q protocol.Request) error {
	data, _ := json.Marshal(q)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkWritable(tx, q.Scope, q.TargetID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO operations(id,target_id,kind,request,request_digest,effect,created_at) VALUES(?,?,?,?,?,'intent',?) ON CONFLICT(target_id,kind) DO UPDATE SET effect='intent',attempt=attempt+1,error='' WHERE effect='confirmed_not_applied' AND request_digest=excluded.request_digest`, q.OperationID, q.TargetID, q.Kind, string(data), protocol.Digest(q), now())
	if err != nil {
		return err
	}
	var effect, digest string
	if err = tx.QueryRow("SELECT effect,request_digest FROM operations WHERE id=?", q.OperationID).Scan(&effect, &digest); err != nil {
		return err
	}
	if effect != "intent" || digest != protocol.Digest(q) {
		return fmt.Errorf("operation is not retryable")
	}
	_, err = tx.Exec("UPDATE targets SET phase='submitting' WHERE id=?", q.TargetID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Complete(q protocol.Request, effect, ref, message string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentEffect, currentRef string
	if err = tx.QueryRow("SELECT effect,remote_ref FROM operations WHERE id=? AND request_digest=?", q.OperationID, protocol.Digest(q)).Scan(&currentEffect, &currentRef); err != nil {
		return err
	}
	if currentEffect != "intent" && currentEffect != "unknown" {
		if currentEffect == effect && currentRef == ref {
			return nil
		}
		return fmt.Errorf("receipt contradicts an already resolved operation")
	}
	result, err := tx.Exec("UPDATE operations SET effect=?,remote_ref=?,error=? WHERE id=? AND request_digest=? AND effect IN ('intent','unknown')", effect, ref, message, q.OperationID, protocol.Digest(q))
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("operation receipt has no matching intent")
	}
	phase := "submission_unknown"
	if effect == "confirmed_applied" {
		switch q.Kind {
		case "authorize", "upload":
			phase = "staged"
		case "bind", "metadata":
			phase = "bound"
		case "submit", "push":
			phase = "submitted"
		default:
			return fmt.Errorf("unknown operation kind")
		}
	} else if effect == "confirmed_rejected" {
		phase = "blocked"
	} else if effect == "confirmed_not_applied" {
		phase = "ready"
		var checkpoint string
		if e := tx.QueryRow("SELECT kind FROM operations WHERE target_id=? AND effect='confirmed_applied' ORDER BY sequence DESC LIMIT 1", q.TargetID).Scan(&checkpoint); e == nil {
			switch checkpoint {
			case "authorize", "upload":
				phase = "staged"
			case "bind", "metadata":
				phase = "bound"
			case "submit", "push":
				phase = "submitted"
			}
		} else if e != sql.ErrNoRows {
			return e
		}
	}
	_, err = tx.Exec("UPDATE targets SET phase=?,remote_ref=CASE WHEN ?='confirmed_applied' THEN ? ELSE remote_ref END WHERE id=?", phase, effect, ref, q.TargetID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) BeginObservation(targetID string) (int64, error) {
	var generation int64
	err := s.DB.QueryRow("UPDATE targets SET generation=generation+1 WHERE id=? RETURNING generation", targetID).Scan(&generation)
	return generation, err
}
func (s *Store) Observe(t Target, generation int64, version int64, o protocol.Observation, queryErr error) error {
	if contextErr := o.Context.Validate(); contextErr != nil {
		// An invalid context may contain vendor links or credentials. Do not
		// retain it even in the failed-observation journal.
		o.Context = nil
		if queryErr == nil {
			queryErr = contextErr
		}
	}
	if queryErr == nil {
		queryErr = o.Validate(t.Scope, version, t.RemoteRef)
	}
	if evidenceErr := s.validateObservationEvidence(o); evidenceErr != nil {
		// Do not persist paths supplied by an invalid worker, or let such paths
		// contaminate future backup inventories. Keep the last good projection.
		o.Evidence = nil
		if queryErr == nil {
			queryErr = evidenceErr
		}
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentGeneration, projectionGeneration int64
	var last, review, availability, preflight string
	if err = tx.QueryRow("SELECT generation,projection_generation,last_success_at,review,availability,preflight FROM targets WHERE id=?", t.ID).Scan(&currentGeneration, &projectionGeneration, &last, &review, &availability, &preflight); err != nil {
		return err
	}
	if queryErr == nil {
		var frozen protocol.RemoteSnapshot
		if preflight != "" && json.Unmarshal([]byte(preflight), &frozen) != nil {
			queryErr = fmt.Errorf("invalid frozen observation scope")
		} else if frozen.AdapterBinding != o.AdapterBinding {
			queryErr = fmt.Errorf("observation adapter configuration mismatch")
		}
	}
	// A source with no evidence for one dimension cannot erase an already
	// established state for the same immutable target. Keep the original
	// observation in history; only merge the materialized projection below.
	projectedReview, projectedAvailability := o.Review, o.Availability
	if projectedReview == "unknown" {
		projectedReview = review
	}
	if projectedAvailability == "unknown" {
		projectedAvailability = availability
	}
	if queryErr == nil && projectedAvailability == "live" && projectedReview != "approved" {
		queryErr = fmt.Errorf("conflicting review and availability evidence")
	}
	if generation <= 0 || generation > currentGeneration {
		return fmt.Errorf("observation has no issued generation")
	}
	var lastProjection string
	projectionErr := tx.QueryRow("SELECT json_extract(document,'$.observation.collected_at') FROM observations WHERE target_id=? AND generation=? AND json_extract(document,'$.projected')=1 ORDER BY id DESC LIMIT 1", t.ID, projectionGeneration).Scan(&lastProjection)
	if projectionErr != nil && projectionErr != sql.ErrNoRows {
		return projectionErr
	}
	if lastProjection != "" {
		last = lastProjection
	}
	conflict := false
	if queryErr == nil && generation == currentGeneration && last != "" {
		if previous, e := time.Parse(time.RFC3339Nano, last); e == nil {
			if o.CollectedAt.Before(previous) {
				queryErr = fmt.Errorf("historical observation cannot replace newer evidence")
			}
			if o.CollectedAt.Equal(previous) && (o.Review != review || o.Availability != availability) {
				queryErr = fmt.Errorf("conflicting evidence at the same observation time")
				conflict = true
			}
		}
	}
	data, _ := json.Marshal(struct {
		Observation protocol.Observation `json:"observation"`
		Valid       bool                 `json:"valid"`
		Projected   bool                 `json:"projected"`
	}{o, queryErr == nil, queryErr == nil && generation == currentGeneration && projectionGeneration < generation})
	if _, err = tx.Exec("INSERT INTO observations(target_id,generation,document,created_at) VALUES(?,?,?,?)", t.ID, generation, string(data), now()); err != nil {
		return err
	}
	next := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	if queryErr != nil {
		_, err = tx.Exec("UPDATE targets SET last_error='query failed or evidence insufficient',query_failures=query_failures+1,next_check_at=?,disputed=CASE WHEN ? THEN 1 ELSE disputed END WHERE id=? AND generation=?", next, conflict, t.ID, generation)
	} else if o.Context != nil && o.Context.QueryStatus() != "observed" {
		_, err = tx.Exec("UPDATE targets SET review=?,availability=?,projection_generation=?,last_error='one or more channel sources failed',query_failures=query_failures+1,next_check_at=? WHERE id=? AND generation=? AND projection_generation<?", projectedReview, projectedAvailability, generation, next, t.ID, generation, generation)
	} else {
		_, err = tx.Exec("UPDATE targets SET review=?,availability=?,projection_generation=?,last_success_at=?,last_error='',query_failures=0,next_check_at=?,disputed=CASE WHEN ? THEN disputed ELSE 0 END WHERE id=? AND generation=? AND projection_generation<?", projectedReview, projectedAvailability, generation, o.CollectedAt.UTC().Format(time.RFC3339Nano), next, o.Context != nil, t.ID, generation, generation)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) RecoveryRequired() (bool, error) {
	var v string
	err := s.DB.QueryRow("SELECT value FROM meta WHERE key='recovery_required'").Scan(&v)
	return v != "0", err
}

// Ping verifies the selected driver and durable configuration without touching a store account.
func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }
