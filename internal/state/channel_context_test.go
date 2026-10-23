package state_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

func TestChannelContextNeverPromotesTargetAndPreservesIndependentHealth(t *testing.T) {
	base := t.TempDir()
	path, err := simulation.Init(filepath.Join(base, "fixture"))
	must(t, err)
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	must(t, err)
	defer s.Close()
	e := engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	r, err := e.Plan(context.Background(), path)
	must(t, err)
	_, err = s.DB.Exec("UPDATE targets SET phase='submitted',remote_ref='remote' WHERE channel='tencent'")
	must(t, err)
	target, err := s.Target(r.ReleaseID + "/tencent")
	must(t, err)
	at := time.Now().UTC()
	o := protocol.Observation{Scope: target.Scope, VersionCode: 1, RemoteRef: "remote", Review: "unknown", Availability: "unknown", Source: "channel_context", CollectedAt: at,
		Context: &protocol.ChannelContext{Association: "unconfirmed", AuditStatus: "approved", AuditCode: "3", APIVersionCode: 1, StoreVersionName: "1.0", APIQueryHealth: "healthy", StoreQueryHealth: "healthy"}}
	observe := func(o protocol.Observation, queryErr error) {
		t.Helper()
		g, err := s.BeginObservation(target.ID)
		must(t, err)
		must(t, s.Observe(target, g, 1, o, queryErr))
	}
	observe(o, nil)
	got, err := s.Target(target.ID)
	must(t, err)
	if got.Context == nil || got.Context.AuditStatus != "approved" || got.Review != "unknown" || got.Availability != "unknown" || got.LastError != "" {
		t.Fatal("unassociated channel facts changed target states", got)
	}
	lastSuccess := got.LastSuccessAt
	o.CollectedAt = at.Add(time.Second)
	o.Context = &protocol.ChannelContext{Association: "unconfirmed", StoreVersionName: "1.1", APIQueryHealth: "failed", StoreQueryHealth: "healthy"}
	observe(o, nil)
	got, err = s.Target(target.ID)
	must(t, err)
	if got.Context == nil || got.Context.StoreVersionName != "1.1" || got.ContextObservedAt == "" || got.LastError == "" || got.LastSuccessAt != lastSuccess || got.QueryFailures != 1 {
		t.Fatal("partial result lost useful facts or hid failure", got)
	}
	o.CollectedAt = at.Add(500 * time.Millisecond)
	o.Context.StoreVersionName = "old"
	observe(o, nil)
	got, err = s.Target(target.ID)
	must(t, err)
	if got.Context.StoreVersionName != "1.1" {
		t.Fatal("older context replaced latest facts")
	}
	o.CollectedAt = at.Add(2 * time.Second)
	o.Context.StoreVersionName = "unverified"
	observe(o, errors.New("network error"))
	got, err = s.Target(target.ID)
	must(t, err)
	if got.Context.StoreVersionName != "1.1" {
		t.Fatal("failed observation replaced latest facts")
	}
	// A late result from a superseded generation cannot appear later when a
	// current observation happens to omit context.
	stale, err := s.BeginObservation(target.ID)
	must(t, err)
	current, err := s.BeginObservation(target.ID)
	must(t, err)
	o.CollectedAt = at.Add(3 * time.Second)
	o.Context.StoreVersionName = "late"
	must(t, s.Observe(target, stale, 1, o, nil))
	o.Context = nil
	must(t, s.Observe(target, current, 1, o, nil))
	got, err = s.Target(target.ID)
	must(t, err)
	if got.Context.StoreVersionName != "1.1" {
		t.Fatal("superseded generation contaminated context", got)
	}
	// Do not journal malformed context (which could contain sensitive links).
	o.CollectedAt = at.Add(4 * time.Second)
	o.Context = &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "healthy", StoreQueryHealth: "healthy", SourceURL: "https://sj.qq.com/appdetail/example.app?token=SECRET_SENTINEL"}
	observe(o, nil)
	var leaked int
	must(t, s.DB.QueryRow("SELECT count(*) FROM observations WHERE document LIKE ?", "%SECRET_SENTINEL%").Scan(&leaked))
	if leaked != 0 {
		t.Fatal("invalid context leaked into journal")
	}
	// A later unassociated query cannot resolve a dispute about exact target
	// evidence, regardless of whether its sources succeed or fail.
	o.Context = nil
	o.Source = "simulation"
	o.CollectedAt = at.Add(5 * time.Second)
	o.Review, o.Availability = "approved", "live"
	observe(o, nil)
	o.Review, o.Availability = "in_review", "not_observed_live"
	observe(o, nil)
	got, err = s.Target(target.ID)
	must(t, err)
	if !got.Disputed {
		t.Fatal("conflict fixture did not dispute target")
	}
	for i, health := range []string{"failed", "partial", "healthy"} {
		o.Review, o.Availability = "unknown", "unknown"
		o.Source = "tencent_automatic_context"
		o.CollectedAt = at.Add(time.Duration(6+i) * time.Second)
		o.Context = &protocol.ChannelContext{Association: "unconfirmed", APIQueryHealth: "failed", StoreQueryHealth: "failed"}
		if health != "failed" {
			o.Context.StoreQueryHealth = "healthy"
			o.Context.StoreVersionName = "1.2"
		}
		if health == "healthy" {
			o.Context.APIQueryHealth = "healthy"
			o.Context.AuditStatus = "approved"
			o.Context.AuditCode = "3"
		}
		observe(o, nil)
		got, err = s.Target(target.ID)
		must(t, err)
		if !got.Disputed {
			t.Fatal("channel context cleared target dispute", health)
		}
		if health == "failed" && (got.CachedContext == nil || got.CachedContext.StoreVersionName != "1.1" || got.CachedContextObservedAt == got.ContextObservedAt) {
			t.Fatal("failed query lost original cached facts/provenance", got)
		}
	}
	o.CollectedAt = at.Add(9 * time.Second)
	o.Review, o.Availability = "approved", "live"
	observe(o, nil)
	got, err = s.Target(target.ID)
	must(t, err)
	if !got.Disputed {
		t.Fatal("context illegally asserted target states")
	}
	status, err := e.Status(r.ReleaseID)
	must(t, err)
	if status.Approved != 0 || status.Live != 0 || strings.Contains(got.ReviewSource, "context") {
		t.Fatal("channel context changed verified aggregates", status)
	}
}
