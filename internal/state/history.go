package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
)

type HistoryFilter struct {
	PackageName string
	Channel     string
	Limit       int
}

// ReleaseRecord is an internal, transactionally consistent view. Callers must
// project it into a public DTO rather than exporting targets or snapshots whole.
type ReleaseRecord struct {
	Snapshot         manifest.Snapshot  `json:"-"`
	CreatedAt        string             `json:"-"`
	Targets          []Target           `json:"-"`
	Operations       []HistoryOperation `json:"-"`
	RecoveryRequired bool               `json:"-"`
}

// HistoryOperation deliberately excludes requests, remote references and errors.
type HistoryOperation struct {
	Sequence   int64  `json:"sequence"`
	Channel    string `json:"channel"`
	Kind       string `json:"operation_kind"`
	Effect     string `json:"effect"`
	Attempt    int    `json:"attempt"`
	NextAction string `json:"next_action"`
	CreatedAt  string `json:"created_at"`
}

var historyPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// History filters which releases are listed; target counts always retain each
// release's complete scope. This API does not obtain credentials or query stores.
func (s *Store) History(ctx context.Context, filter HistoryFilter) ([]ReleaseRecord, error) {
	if filter.Limit < 1 || filter.Limit > 200 || filter.PackageName != "" && !historyPackage.MatchString(filter.PackageName) || filter.Channel != "" && !slices.Contains(manifest.Channels, filter.Channel) {
		return nil, fmt.Errorf("invalid history filter")
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM releases WHERE (?='' OR json_extract(snapshot,'$.manifest.package_name')=?) AND (?='' OR EXISTS(SELECT 1 FROM targets WHERE targets.release_id=releases.id AND channel=?)) ORDER BY created_at DESC,id DESC LIMIT ?`, filter.PackageName, filter.PackageName, filter.Channel, filter.Channel, filter.Limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return s.releaseRecords(ctx, tx, ids)
}

// ReleaseRecords reads an entire report scope from a single SQLite snapshot.
func (s *Store) ReleaseRecords(ctx context.Context, ids []string) ([]ReleaseRecord, error) {
	if len(ids) == 0 || len(ids) > 200 {
		return nil, fmt.Errorf("invalid history selection")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !manifest.ValidID(id) || seen[id] {
			return nil, fmt.Errorf("invalid history selection")
		}
		seen[id] = true
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return s.releaseRecords(ctx, tx, ids)
}

func (s *Store) releaseRecords(ctx context.Context, tx *sql.Tx, ids []string) ([]ReleaseRecord, error) {
	var recovery string
	if err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='recovery_required'").Scan(&recovery); err != nil || recovery != "0" && recovery != "1" {
		return nil, fmt.Errorf("invalid recovery state")
	}
	result := make([]ReleaseRecord, 0, len(ids))
	for _, id := range ids {
		var r ReleaseRecord
		var data, digest string
		if err := tx.QueryRowContext(ctx, "SELECT snapshot,digest,created_at FROM releases WHERE id=?", id).Scan(&data, &digest, &r.CreatedAt); err != nil {
			if err == sql.ErrNoRows {
				return nil, ErrReleaseNotFound
			}
			return nil, err
		}
		// Notes and the source manifest each allow 1 MiB UTF-8; JSON can
		// expand a byte to six bytes. Leave room for both plus the snapshot.
		if len(data) > 16<<20 || json.Unmarshal([]byte(data), &r.Snapshot) != nil || r.Snapshot.Verify(s.Mode) != nil || r.Snapshot.Manifest.ReleaseID != id || digest != r.Snapshot.Digest {
			return nil, fmt.Errorf("invalid saved release")
		}
		if _, err := time.Parse(time.RFC3339Nano, r.CreatedAt); err != nil {
			return nil, fmt.Errorf("invalid release creation time")
		}
		r.RecoveryRequired = recovery != "0"
		rows, err := tx.QueryContext(ctx, "SELECT "+targetColumns+" FROM targets WHERE release_id=? ORDER BY channel", id)
		if err != nil {
			return nil, err
		}
		r.Targets = []Target{}
		for rows.Next() {
			t, err := scanTarget(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			target, ok := r.Snapshot.Manifest.Targets[t.Channel]
			if !ok || t.ID != id+"/"+t.Channel || t.ReleaseID != id || t.Scope != Scope(t.Channel, target, r.Snapshot.Manifest.PackageName) {
				rows.Close()
				return nil, fmt.Errorf("invalid saved target")
			}
			r.Targets = append(r.Targets, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(r.Targets) != len(r.Snapshot.Manifest.Targets) {
			return nil, fmt.Errorf("incomplete saved release")
		}
		for i := range r.Targets {
			if err := attachObservationSources(tx, &r.Targets[i]); err != nil {
				return nil, err
			}
		}
		r.Operations, err = historyOperations(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, tx.Commit()
}

func historyOperations(ctx context.Context, tx *sql.Tx, id string) ([]HistoryOperation, error) {
	rows, err := tx.QueryContext(ctx, "SELECT o.sequence,t.channel,o.kind,o.effect,o.attempt,o.created_at,t.phase FROM operations o JOIN targets t ON t.id=o.target_id WHERE t.release_id=? ORDER BY o.sequence", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []HistoryOperation{}
	for rows.Next() {
		var o HistoryOperation
		var phase string
		if err := rows.Scan(&o.Sequence, &o.Channel, &o.Kind, &o.Effect, &o.Attempt, &o.CreatedAt, &phase); err != nil {
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
			o.NextAction = "resume"
			if phase == "submitted" {
				o.NextAction = "observe"
			}
		}
		result = append(result, o)
	}
	return result, rows.Err()
}
