package channels_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

var honorProtocolSteps = []string{"authorize", "upload", "bind", "metadata", "submit"}
var honorProtocolRoutes = []string{"get-file-upload-url", "file-upload", "update-file-info", "update-language-info", "submit-audit"}

func assertProtocolCalls(t *testing.T, f *honorFixture, want []int) {
	t.Helper()
	calls, applied := f.snapshot()
	for i, route := range honorProtocolRoutes {
		if calls[route] != want[i] || applied[route] != want[i] {
			t.Fatalf("%s: requests=%d applied=%d want=%d", route, calls[route], applied[route], want[i])
		}
	}
}
func assertJournal(t *testing.T, s *state.Store, release string, confirmed int, lastEffect string) {
	t.Helper()
	operations, err := s.OperationStatuses(release)
	checkFixture(t, err)
	want := confirmed
	if lastEffect != "" {
		want++
	}
	if len(operations) != want {
		t.Fatalf("journal contains %d operations, want %d", len(operations), want)
	}
	for i, op := range operations {
		effect := "confirmed_applied"
		if i == confirmed {
			effect = lastEffect
		}
		if op.Kind != honorProtocolSteps[i] || op.Effect != effect || op.Attempt != 1 {
			t.Fatalf("unexpected operation checkpoint %d: %+v", i, op)
		}
		if op.Effect == "confirmed_applied" && op.RemoteRef == "" {
			t.Fatal("confirmed checkpoint lacks reference")
		}
		stored, err := s.Operation(release+"/honor", op.Kind)
		checkFixture(t, err)
		if len(stored.Request.Checkpoints) != i {
			t.Fatalf("%s lost confirmed predecessor checkpoints", op.Kind)
		}
		for _, previous := range operations[:i] {
			if stored.Request.Checkpoints[previous.Kind] != previous.RemoteRef {
				t.Fatalf("%s does not retain %s receipt", op.Kind, previous.Kind)
			}
		}
		var journal string
		checkFixture(t, s.DB.QueryRow("SELECT request || remote_ref || error FROM operations WHERE id=?", op.OperationID).Scan(&journal))
		if strings.Contains(journal, fixtureBearer) || strings.Contains(journal, "https://appmarket-openapi-drcn.cloud.honor.com") {
			t.Fatal("journal contains secret token or upload URL")
		}
	}
}

func TestHonorProtocolJournalAndNewAdapterResumeAfterBind(t *testing.T) {
	v := newRecoveryFixture(t, false)
	stop := errors.New("synthetic stop after confirmed binding")
	v.engine.Hook = func(point string, q protocol.Request) error {
		if point == "after_receipt" && q.Kind == "bind" {
			return stop
		}
		return nil
	}
	if _, err := v.engine.Submit(context.Background(), v.release, nil); err == nil {
		t.Fatal("fault did not stop execution")
	}
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 0, 0})
	assertJournal(t, v.engine.Store, v.release, 3, "")
	v.reopen(t)
	v.engine.Hook = nil
	result, err := v.engine.Submit(context.Background(), v.release, nil)
	checkFixture(t, err)
	if !result.AllAccepted || result.AllApproved || result.AllLive || result.Accepted != 1 {
		t.Fatalf("acceptance conflated with review or availability: %+v", result)
	}
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 1, 1})
	assertJournal(t, v.engine.Store, v.release, 5, "")
	if _, err = v.engine.Submit(context.Background(), v.release, nil); err != nil {
		t.Fatal(err)
	}
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 1, 1})
	// Approval is exactly associated with the persisted release ID; an audit
	// result still supplies no evidence that the target version is available.
	result, err = v.engine.Refresh(context.Background(), v.release, nil)
	checkFixture(t, err)
	if !result.AllApproved || result.AllLive {
		t.Fatal("review evidence did not stay separate from live evidence")
	}
}

func TestHonorLostSubmitResponseBlocksRetryAndOtherRelease(t *testing.T) {
	v := newRecoveryFixture(t, true)
	if _, err := v.engine.Submit(context.Background(), v.release, nil); err == nil {
		t.Fatal("lost response was accepted")
	}
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 1, 1})
	assertJournal(t, v.engine.Store, v.release, 4, "unknown")
	v.reopen(t)
	before, _ := v.remote.snapshot()
	if _, err := v.engine.Reconcile(context.Background(), v.release, nil); err == nil {
		t.Fatal("missing operation evidence was treated as proof")
	}
	if _, err := v.engine.Submit(context.Background(), v.release, nil); err == nil {
		t.Fatal("unknown submit was retried")
	}
	after, _ := v.remote.snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("reconciliation or resume sent a new request: before=%v after=%v", before, after)
	}
	assertJournal(t, v.engine.Store, v.release, 4, "unknown")
	old, err := v.engine.Store.Target(v.release + "/honor")
	checkFixture(t, err)
	snapshot, err := v.engine.Store.Load(v.release)
	checkFixture(t, err)
	snapshot.Manifest.ReleaseID = "honor-next-intent"
	snapshot.Notes += " changed revision"
	snapshot.Digest = snapshot.CalculateDigest()
	newID, err := v.engine.Store.Save(snapshot)
	checkFixture(t, err)
	if err = v.engine.Store.CheckWritable(old.Scope, newID+"/honor"); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("unresolved write lost application ownership: %v", err)
	}
	if _, err = v.engine.Preflight(context.Background(), newID, nil); err == nil {
		t.Fatal("new release bypassed scope barrier")
	}
	if _, err = v.engine.Submit(context.Background(), newID, nil); err == nil {
		t.Fatal("new release submitted without a preflight")
	}
	assertProtocolCalls(t, v.remote, []int{1, 1, 1, 1, 1})
}

// This worker executes a real channel Adapter and SQLite engine. Its only
// network route is the parent-owned TLS fixture; no Keychain or vendor host is
// reachable through fixtureWire. A SIGKILL leaves journal and materials on disk.
func TestHonorProtocolRecoveryWorker(t *testing.T) {
	if os.Getenv("PUBLISHER_CHANNEL_RECOVERY_WORKER") != "1" {
		return
	}
	s, err := state.Open(os.Getenv("PUBLISHER_CHANNEL_RECOVERY_ROOT"), manifest.Simulation, false)
	checkFixture(t, err)
	defer s.Close()
	release := os.Getenv("PUBLISHER_CHANNEL_RECOVERY_RELEASE")
	a, err := frozenHonorAdapter(s, release, os.Getenv("PUBLISHER_CHANNEL_RECOVERY_ENDPOINT"), os.Getenv("PUBLISHER_CHANNEL_RECOVERY_CERT"), os.Getenv("PUBLISHER_CHANNEL_RECOVERY_MATERIALS"))
	checkFixture(t, err)
	e := &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}, Adapter: a, Hook: func(point string, q protocol.Request) error {
		if point == os.Getenv("PUBLISHER_CHANNEL_RECOVERY_POINT") && q.Kind == os.Getenv("PUBLISHER_CHANNEL_RECOVERY_KIND") {
			if err := privatefs.Write(os.Getenv("PUBLISHER_CHANNEL_RECOVERY_READY"), []byte("ready")); err != nil {
				return err
			}
			select {}
		}
		return nil
	}}
	_, err = e.Submit(context.Background(), release, nil)
	checkFixture(t, err)
}

func runHonorWorker(t *testing.T, v *recoveryFixture, point, kind string) *exec.Cmd {
	t.Helper()
	ready := filepath.Join(v.base, "worker-ready")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHonorProtocolRecoveryWorker$", "-test.timeout=18s")
	cmd.Env = append(os.Environ(), "PUBLISHER_CHANNEL_RECOVERY_WORKER=1", "PUBLISHER_CHANNEL_RECOVERY_ROOT="+v.root, "PUBLISHER_CHANNEL_RECOVERY_RELEASE="+v.release, "PUBLISHER_CHANNEL_RECOVERY_ENDPOINT="+v.remote.server.URL, "PUBLISHER_CHANNEL_RECOVERY_CERT="+v.certificate, "PUBLISHER_CHANNEL_RECOVERY_MATERIALS="+filepath.Join(v.base, "synthetic-materials"), "PUBLISHER_CHANNEL_RECOVERY_POINT="+point, "PUBLISHER_CHANNEL_RECOVERY_KIND="+kind, "PUBLISHER_CHANNEL_RECOVERY_READY="+ready)
	log, err := os.CreateTemp(t.TempDir(), "protocol-worker-")
	checkFixture(t, err)
	cmd.Stdout = log
	cmd.Stderr = log
	checkFixture(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = log.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	b, _ := os.ReadFile(log.Name())
	t.Fatalf("worker did not reach checkpoint: %s", b)
	return nil
}

func TestHonorActualProcessKillRestartsProtocolWithoutRepeatingWrites(t *testing.T) {
	for _, scenario := range []struct {
		point, kind   string
		confirmed     int
		before, after []int
		resume        bool
	}{
		{"after_receipt", "bind", 3, []int{1, 1, 1, 0, 0}, []int{1, 1, 1, 1, 1}, true},
		{"after_execute", "submit", 4, []int{1, 1, 1, 1, 1}, []int{1, 1, 1, 1, 1}, false},
	} {
		t.Run(scenario.point+"_"+scenario.kind, func(t *testing.T) {
			v := newRecoveryFixture(t, false)
			checkFixture(t, v.engine.Store.Close())
			worker := runHonorWorker(t, v, scenario.point, scenario.kind)
			if _, err := state.Open(v.root, manifest.Simulation, false); !errors.Is(err, state.ErrBusy) {
				t.Fatalf("process lock was not held: %v", err)
			}
			reader, err := state.Open(v.root, manifest.Simulation, true)
			checkFixture(t, err)
			lastEffect := ""
			if !scenario.resume {
				lastEffect = "intent"
			}
			assertJournal(t, reader, v.release, scenario.confirmed, lastEffect)
			checkFixture(t, reader.Close())
			assertProtocolCalls(t, v.remote, scenario.before)
			checkFixture(t, worker.Process.Kill())
			if err = worker.Wait(); err == nil {
				t.Fatal("worker exited normally instead of being killed")
			}
			s, err := state.Open(v.root, manifest.Simulation, false)
			checkFixture(t, err)
			v.engine.Store = s
			v.installAdapter(t)
			result, err := v.engine.Submit(context.Background(), v.release, nil)
			if scenario.resume {
				checkFixture(t, err)
				if !result.AllAccepted {
					t.Fatal("confirmed binding could not resume")
				}
				assertJournal(t, s, v.release, 5, "")
			} else if err == nil {
				t.Fatal("effect without durable receipt was blindly retried")
			}
			assertProtocolCalls(t, v.remote, scenario.after)
		})
	}
}
