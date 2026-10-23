package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"regexp"
)

type RecoveryApplication struct {
	Identity        Identity `json:"identity"`
	LatestVersion   int64    `json:"latest_remote_version_code"`
	HistoryComplete bool     `json:"complete_history_checked"`
	NoPendingWrites bool     `json:"no_pending_remote_writes"`
	Evidence        string   `json:"redacted_history_evidence"`
}
type RecoveryReview struct {
	SchemaVersion            int                   `json:"schema_version"`
	RestoreSource            string                `json:"restore_source_sha256"`
	RestoreTime              string                `json:"restore_time"`
	LedgerDigest             string                `json:"ledger_digest"`
	ReviewedAt               string                `json:"reviewed_at"`
	ReviewedBy               string                `json:"reviewed_by"`
	Reason                   string                `json:"reason"`
	CompleteAccountInventory bool                  `json:"complete_account_inventory_checked"`
	Applications             []RecoveryApplication `json:"applications"`
}

func (s *Store) recoveryLedger() (string, error) {
	// Query individual columns into a deterministic journal, excluding status
	// polling. A new business checkpoint invalidates an earlier review approval.
	var parts []string
	for _, query := range []string{"SELECT id||':'||digest FROM releases ORDER BY id", "SELECT id||':'||request_digest||':'||effect||':'||attempt||':'||remote_ref FROM operations ORDER BY sequence"} {
		rows, err := s.DB.Query(query)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				rows.Close()
				return "", err
			}
			parts = append(parts, line)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return protocol.Digest(parts), nil
}
func (s *Store) RecoveryTemplate() (RecoveryReview, error) {
	required, err := s.RecoveryRequired()
	if err != nil || !required {
		return RecoveryReview{}, fmt.Errorf("state is not in recovery isolation")
	}
	review := RecoveryReview{SchemaVersion: 1, Applications: []RecoveryApplication{}}
	var inventoryJSON string
	for key, dst := range map[string]*string{"restore_source": &review.RestoreSource, "restore_time": &review.RestoreTime, "restore_inventory": &inventoryJSON} {
		if err = s.DB.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(dst); err != nil {
			return review, fmt.Errorf("restore audit metadata is incomplete")
		}
	}
	var identities []Identity
	if json.Unmarshal([]byte(inventoryJSON), &identities) != nil {
		return review, fmt.Errorf("restore inventory is invalid")
	}
	_, current, err := inventory(s.DB)
	if err != nil {
		return review, err
	}
	identities = append(identities, current...)
	seen := map[string]bool{}
	for _, i := range identities {
		if !seen[i.Scope] {
			review.Applications = append(review.Applications, RecoveryApplication{Identity: i})
			seen[i.Scope] = true
		}
	}
	slices.SortFunc(review.Applications, func(a, b RecoveryApplication) int { return strings.Compare(a.Identity.Scope, b.Identity.Scope) })
	review.LedgerDigest, err = s.recoveryLedger()
	return review, err
}
func (s *Store) ValidateRecoveryReview(r RecoveryReview) (string, error) {
	expected, err := s.RecoveryTemplate()
	if err != nil {
		return "", err
	}
	if r.SchemaVersion != 1 || r.RestoreSource != expected.RestoreSource || r.RestoreTime != expected.RestoreTime || r.LedgerDigest != expected.LedgerDigest || !r.CompleteAccountInventory || len(r.Applications) == 0 || len(expected.Applications) == 0 {
		return "", fmt.Errorf("recovery review is incomplete or differs from the current ledger")
	}
	at, err := time.Parse(time.RFC3339Nano, r.ReviewedAt)
	restored, parseErr := time.Parse(time.RFC3339Nano, r.RestoreTime)
	if err != nil || parseErr != nil || at.Before(restored) || at.After(time.Now().Add(time.Minute)) || time.Since(at) > 30*time.Minute || strings.TrimSpace(r.ReviewedBy) == "" || len(r.ReviewedBy) > 80 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 2000 {
		return "", fmt.Errorf("recovery review needs a recent collector, reason and time")
	}
	known := map[string]bool{}
	for _, app := range r.Applications {
		i := app.Identity
		if !slices.Contains(manifest.Channels, i.Channel) || !account.ValidName(i.AccountRef) || !manifest.ValidID(i.AppRef) || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`).MatchString(i.PackageName) || i.Scope != Scope(i.Channel, manifest.Target{AccountRef: i.AccountRef, AppRef: i.AppRef}, i.PackageName) || known[i.Scope] || app.LatestVersion < 0 || !app.HistoryComplete || !app.NoPendingWrites || strings.TrimSpace(app.Evidence) == "" || len(app.Evidence) > 16000 {
			return "", fmt.Errorf("each application requires full history evidence and no unresolved remote writes")
		}
		known[i.Scope] = true
	}
	for _, app := range expected.Applications {
		if !known[app.Identity.Scope] {
			return "", fmt.Errorf("recovery review omitted a known application")
		}
	}
	var unresolved int
	err = s.DB.QueryRow(`SELECT COUNT(*) FROM operations o WHERE o.effect IN ('intent','unknown') OR (o.effect='confirmed_applied' AND o.kind NOT IN ('submit','push') AND NOT EXISTS (SELECT 1 FROM operations final WHERE final.target_id=o.target_id AND final.effect='confirmed_applied' AND final.kind IN ('submit','push')))`).Scan(&unresolved)
	if err != nil {
		return "", err
	}
	if unresolved > 0 {
		return "", fmt.Errorf("reconcile all unresolved and partial local writes before closing recovery")
	}
	// Every known accepted version must be included in the externally checked
	// high-water mark; a human assertion cannot contradict retained receipts.
	rows, err := s.DB.Query("SELECT t.scope,r.snapshot FROM targets t JOIN releases r ON r.id=t.release_id WHERE t.phase='submitted'")
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var scope, data string
		if err = rows.Scan(&scope, &data); err != nil {
			break
		}
		var snapshot manifest.Snapshot
		if json.Unmarshal([]byte(data), &snapshot) != nil {
			err = fmt.Errorf("corrupt accepted release")
			break
		}
		for _, app := range r.Applications {
			if app.Identity.Scope == scope && app.LatestVersion < snapshot.Manifest.VersionCode {
				err = fmt.Errorf("remote history contradicts a retained acceptance receipt")
				break
			}
		}
		if err != nil {
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	if rowErr != nil {
		return "", rowErr
	}
	return protocol.Digest(r), nil
}
func (s *Store) ApplyRecoveryReview(r RecoveryReview, approved string) error {
	return s.applyRecoveryReview(r, approved, nil)
}

// hook is an in-process transaction fault seam; production callers always use nil.
func (s *Store) applyRecoveryReview(r RecoveryReview, approved string, hook func() error) error {
	release, err := s.BeginCommand()
	if err != nil {
		return err
	}
	defer release()
	digest, err := s.ValidateRecoveryReview(r)
	if err != nil {
		return err
	}
	if approved != digest {
		return fmt.Errorf("exact reviewed recovery digest is required")
	}
	floors := map[string]int64{}
	var previous string
	if readErr := s.DB.QueryRow("SELECT value FROM meta WHERE key='recovery_version_floors'").Scan(&previous); readErr == nil {
		if json.Unmarshal([]byte(previous), &floors) != nil || floors == nil {
			return fmt.Errorf("invalid earlier recovery floors")
		}
	} else if readErr != sql.ErrNoRows {
		return readErr
	}
	for _, app := range r.Applications {
		if app.LatestVersion > floors[app.Identity.Scope] {
			floors[app.Identity.Scope] = app.LatestVersion
		}
	}
	data, _ := json.Marshal(r)
	floorBytes, _ := json.Marshal(floors)
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES (?,?)", "recovery_review_"+digest, string(data)); err != nil {
		return err
	}
	if hook != nil {
		if err := hook(); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES ('recovery_version_floors',?)", string(floorBytes)); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE meta SET value='0' WHERE key='recovery_required'"); err != nil {
		return err
	}
	// Older binaries must reject the database instead of ignoring version floors.
	if _, err = tx.Exec("PRAGMA user_version=3"); err != nil {
		return err
	}
	return tx.Commit()
}
