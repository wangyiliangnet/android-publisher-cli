package channels_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
)

func TestJournalReconcileUnknownAuthorize(t *testing.T) {
	for _, kind := range []string{"authorize", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			v := newRecoveryFixture(t, false)
			v.engine.Hook = func(point string, q protocol.Request) error {
				if point == "after_intent" && q.Kind == kind {
					return errors.New("synthetic stop")
				}
				return nil
			}
			_, _ = v.engine.Submit(context.Background(), v.release, nil)
			op, err := v.engine.Store.Operation(v.release+"/honor", kind)
			checkFixture(t, err)
			if op == nil || op.Effect != "intent" {
				t.Fatalf("missing initial intent: %+v", op)
			}
			_, err = v.engine.Reconcile(context.Background(), v.release, nil)
			if err == nil {
				t.Fatal("Reconcile returned success without consulting unresolved authorize")
			}
		})
	}
}

type notSentAdapter struct{ protocol.Adapter }

func (a notSentAdapter) BuildPlan(q protocol.Request) ([]string, error) {
	return a.Adapter.(protocol.Planner).BuildPlan(q)
}
func (a notSentAdapter) ExecuteOne(context.Context, protocol.Request) (protocol.Receipt, error) {
	return protocol.Receipt{}, &transport.Error{Code: "synthetic_before_send", Effect: "not_sent", Attempts: 0}
}
func TestJournalTransportNotSentPersistsKnownNonApplication(t *testing.T) {
	v := newRecoveryFixture(t, false)
	original := v.engine.Adapter
	v.engine.Adapter = notSentAdapter{v.engine.Adapter}
	_, _ = v.engine.Submit(context.Background(), v.release, nil)
	op, err := v.engine.Store.Operation(v.release+"/honor", "authorize")
	checkFixture(t, err)
	if op == nil || op.Effect != "confirmed_not_applied" {
		t.Fatalf("explicit pre-send failure becomes %#v", op)
	}
	assertProtocolCalls(t, v.remote, []int{0, 0, 0, 0, 0})
	v.engine.Adapter = original
	_, err = v.engine.Submit(context.Background(), v.release, nil)
	checkFixture(t, err)
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 1, 1})
}

func TestJournalReadonlyPreflightCannotBreakConfirmedMetadataResume(t *testing.T) {
	v := newRecoveryFixture(t, false)
	v.engine.Hook = func(point string, q protocol.Request) error {
		if point == "after_receipt" && q.Kind == "metadata" {
			return errors.New("synthetic stop")
		}
		return nil
	}
	_, _ = v.engine.Submit(context.Background(), v.release, nil)
	assertJournal(t, v.engine.Store, v.release, 4, "")
	v.engine.Hook = nil
	_, err := v.engine.Preflight(context.Background(), v.release, nil)
	checkFixture(t, err)
	result, err := v.engine.Submit(context.Background(), v.release, nil)
	if err != nil {
		t.Fatalf("read-only preflight destroyed own metadata resume: %v %+v", err, result.Errors)
	}
}

type reboundObservationAdapter struct{ protocol.Adapter }

func (reboundObservationAdapter) AdapterBinding(string) (string, error) {
	return protocol.Digest("different account or app configuration"), nil
}
func TestJournalRefreshRejectsDifferentAccountBindingBeforeHTTP(t *testing.T) {
	v := newRecoveryFixture(t, false)
	_, err := v.engine.Submit(context.Background(), v.release, nil)
	checkFixture(t, err)
	before, _ := v.remote.snapshot()
	v.engine.Adapter = reboundObservationAdapter{v.engine.Adapter}
	result, err := v.engine.Refresh(context.Background(), v.release, nil)
	if err == nil || result.AllApproved || result.AllLive {
		t.Fatal("rebound account observation accepted")
	}
	after, _ := v.remote.snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refresh queried a changed account")
	}
	target, err := v.engine.Store.Target(v.release + "/honor")
	checkFixture(t, err)
	generation, err := v.engine.Store.BeginObservation(target.ID)
	checkFixture(t, err)
	o, err := v.engine.Adapter.Observe(context.Background(), target.Scope, 2, target.RemoteRef)
	checkFixture(t, err)
	o.AdapterBinding = protocol.Digest("forged scope")
	o.Review = "approved"
	o.Availability = "live"
	checkFixture(t, v.engine.Store.Observe(target, generation, 2, o, nil))
	target, err = v.engine.Store.Target(target.ID)
	checkFixture(t, err)
	if target.Review == "approved" || target.Availability == "live" {
		t.Fatal("direct state observation lost frozen account binding")
	}
}
