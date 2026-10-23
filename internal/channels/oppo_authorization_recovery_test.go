package channels

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type responseFailVault struct{ credentials.Vault }

type fileCheckpointFailVault struct {
	credentials.Vault
	name string
}

func (v fileCheckpointFailVault) Put(ctx context.Context, ref credentials.Ref, value *credentials.Secret, replace bool) error {
	if ref.Name == v.name {
		return credentials.ErrUnavailable
	}
	return v.Vault.Put(ctx, ref, value, replace)
}

func TestOPPOSealedHTTPFileReferenceRecoversUploadWithoutRetransmission(t *testing.T) {
	v := productionFixture(t)
	v.fileURLScheme = "http"
	e, p := recoveryEngine(t, v)
	uploadID := protocol.Digest([]string{v.input.Snapshot.Digest, "oppo", "upload"})
	ref := "material-" + account.Key(uploadID)
	original := v.runtime.Manager.Vault
	v.runtime.Manager.Vault = fileCheckpointFailVault{original, materialName(ref)}
	id := v.input.Snapshot.Manifest.ReleaseID
	ctx := context.Background()
	result, err := e.Submit(ctx, id, nil)
	if err == nil || len(v.writes) != 2 || result.Operations[1].Effect != "unknown" {
		t.Fatal("missing checkpoint was accepted or continued submitting")
	}
	v.runtime.Manager.Vault = original
	p2, err := v.runtime.NewOPPOProduction(ctx, v.input, p.BaselineRef(), false)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p2
	result, err = e.Reconcile(ctx, id, nil)
	if err != nil || result.Operations[1].Effect != "confirmed_applied" || result.Operations[1].Attempt != 1 || len(v.writes) != 2 {
		t.Fatalf("sealed upload proof: %v", err)
	}
	d := p2.LastWriteDiagnostic()
	if d == nil || d.FileURLScheme != "http" || !d.FileChecks["md5_matches"] || !d.FileChecks["size_matches"] {
		t.Fatal("opaque reference not verified")
	}
	p3, err := v.runtime.NewOPPOProduction(ctx, v.input, p.BaselineRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p3
	result, err = e.Submit(ctx, id, nil)
	if err != nil || !result.AllAccepted || strings.Join(v.writes, ",") != "authorize,upload,submit" {
		t.Fatalf("recovered chain: %v writes=%v", err, v.writes)
	}
}

func TestOPPOInvalidSealedFileReceiptDoesNotSettleOrUploadAgain(t *testing.T) {
	for _, kind := range []string{"md5", "size", "extension", "id", "url"} {
		t.Run(kind, func(t *testing.T) {
			v := productionFixture(t)
			v.uploadCorruption = kind
			e, _ := recoveryEngine(t, v)
			id := v.input.Snapshot.Manifest.ReleaseID
			ctx := context.Background()
			if _, err := e.Submit(ctx, id, nil); err == nil {
				t.Fatal("bad receipt accepted")
			}
			r, err := e.Reconcile(ctx, id, nil)
			if err == nil || r.Operations[1].Effect != "unknown" || len(v.writes) != 2 {
				t.Fatal("invalid receipt settled or retransmitted upload")
			}
		})
	}
}

func (v responseFailVault) Put(ctx context.Context, ref credentials.Ref, value *credentials.Secret, replace bool) error {
	if strings.HasPrefix(ref.Name, "upload-") {
		return credentials.ErrUnavailable
	}
	return v.Vault.Put(ctx, ref, value, replace)
}

func TestOPPOResponseSealingFailureStopsBeforeAPKUpload(t *testing.T) {
	v := productionFixture(t)
	e, p := recoveryEngine(t, v)
	v.runtime.Manager.Vault = responseFailVault{v.runtime.Manager.Vault}
	result, err := e.Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil)
	if err == nil || len(v.writes) != 1 || result.Operations[0].Effect != "unknown" {
		t.Fatal("response persistence failure continued uploading")
	}
	d := p.LastWriteDiagnostic()
	if d == nil || d.HTTPStatus != 200 || d.Recording != "not_saved" {
		t.Fatal("failed sealing lost typed diagnostic")
	}
	if _, err = e.Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil); err == nil || len(v.writes) != 1 {
		t.Fatal("sealing failure automatically retried")
	}
}

func recoveryEngine(t *testing.T, v *oppoProductionFixture) (*engine.Engine, *OPPOProduction) {
	t.Helper()
	ctx := context.Background()
	p, err := v.runtime.NewOPPOProduction(ctx, v.input, "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Store: v.store, Inspector: artifact.SimulationInspector{}, Adapter: p}
	if _, err = e.Preflight(ctx, v.input.Snapshot.Manifest.ReleaseID, nil); err != nil {
		t.Fatal(err)
	}
	p, err = v.runtime.NewOPPOProduction(ctx, v.input, p.BaselineRef(), true)
	if err != nil {
		t.Fatal(err)
	}
	e.Adapter = p
	return e, p
}
func authorizationID(v *oppoProductionFixture) string {
	return protocol.Digest([]string{v.input.Snapshot.Digest, "oppo", "authorize"})
}

func TestOPPOObservedOfficialUploadHostAndStrictDestinationChecks(t *testing.T) {
	v := productionFixture(t)
	v.uploadHost = "api.open.oppomobile.com"
	e, _ := recoveryEngine(t, v)
	r, err := e.Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil)
	if err != nil || !r.AllAccepted || strings.Join(v.writes, ",") != "authorize,upload,submit" {
		t.Fatalf("official returned host: %v", err)
	}
	for _, u := range []string{"http://api.open.oppomobile.com/upload", "https://api.open.oppomobile.com.evil.invalid/upload", "https://evil.api.open.oppomobile.com/upload", "https://oppomobile.com/upload", "https://api.open.oppomobile.com:443/upload", "https://token@api.open.oppomobile.com/upload", "https://api.open.oppomobile.com/upload#fragment"} {
		if allowedUpload("oppo", u) {
			t.Fatal("unreviewed upload origin accepted")
		}
	}
}

func TestOPPOSealedResponseCanBeDiagnosedWithoutHTTPOrSecrets(t *testing.T) {
	v := productionFixture(t)
	v.uploadHost = "api.open.oppomobile.com"
	e, p := recoveryEngine(t, v)
	e.Hook = func(stage string, q protocol.Request) error {
		if stage == "after_execute" && q.Kind == "authorize" {
			return errors.New("synthetic receipt interruption")
		}
		return nil
	}
	e.Submit(context.Background(), v.input.Snapshot.Manifest.ReleaseID, nil)
	e.Hook = nil
	op, _ := v.store.Operation(v.input.Snapshot.Manifest.ReleaseID+"/oppo", "authorize")
	p2, err := v.runtime.NewOPPOProduction(context.Background(), v.input, p.BaselineRef(), false)
	if err != nil {
		t.Fatal(err)
	}
	before := len(v.writes)
	r, err := p2.Reconcile(context.Background(), op.Request)
	if err != nil || r.Result != "inconclusive" || len(v.writes) != before {
		t.Fatal("read-only diagnosis issued a business request")
	}
	d := p2.LastWriteDiagnostic()
	if d == nil || d.UploadHost != "api.open.oppomobile.com" || d.UploadScheme != "https" || !d.SignPresent || !d.UploadURLPresent || d.JSONTypes["data.sign"] != "string" {
		t.Fatal("sealed response structure unavailable")
	}
	b, _ := json.Marshal(d)
	if bytes.Contains(b, []byte("SYNTHETIC-ONETIME")) || bytes.Contains(b, []byte("/upload")) {
		t.Fatal("signed URL or sign leaked from diagnostic")
	}
}

func TestOPPOConfigurationReplacementPreservesUnknownAndSubmitsOnce(t *testing.T) {
	v := productionFixture(t)
	e, p := recoveryEngine(t, v)
	v.authorizeFailures = 1
	id := v.input.Snapshot.Manifest.ReleaseID
	ctx := context.Background()
	if _, err := e.Submit(ctx, id, nil); err == nil {
		t.Fatal("gateway failure accepted")
	}
	d := p.LastWriteDiagnostic()
	if d == nil || d.HTTPStatus != 504 || !d.Complete || d.ResponseRef == "" || d.Recording != "sealed_in_keychain" {
		t.Fatal("gateway receipt was not sealed before rejection")
	}
	if err := e.ReplaceUnusedOPPOAuthorization(ctx, id, authorizationID(v)); err != nil {
		t.Fatal(err)
	}
	result, err := e.Submit(ctx, id, nil)
	if err != nil || !result.AllAccepted || strings.Join(v.writes, ",") != "authorize,authorize,upload,submit" {
		t.Fatalf("replacement: %v writes=%v", err, v.writes)
	}
	if len(result.AuthorizationHistory) != 1 || result.AuthorizationHistory[0].Effect != "unknown" || result.AuthorizationHistory[0].Attempt != 1 || result.Operations[0].Attempt != 2 {
		t.Fatal("old unknown was falsified or lost")
	}
	if _, err = e.Submit(ctx, id, nil); err != nil || len(v.writes) != 4 {
		t.Fatal("accepted update repeated")
	}
	if err = e.ReplaceUnusedOPPOAuthorization(ctx, id, authorizationID(v)); err == nil || len(v.writes) != 4 {
		t.Fatal("completed chain renewed")
	}
	b, _ := json.Marshal(result)
	for _, secret := range []string{"SYNTHETIC-PRIVATE", "SYNTHETIC-ONETIME", "SYNTHETIC-UPLOADED", "https://"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Fatal("private response leaked")
		}
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err = v.store.Backup(backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err = state.Restore(restored, backup, manifest.Production); err != nil {
		t.Fatal(err)
	}
	s, err := state.Open(restored, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	history, err := s.OPPOAuthorizationHistory(id)
	if err != nil || len(history) != 1 || history[0].Effect != "unknown" {
		t.Fatal("backup lost authorization history")
	}
	quarantined, _ := s.RecoveryRequired()
	if !quarantined {
		t.Fatal("restored state escaped quarantine")
	}
}

func TestOPPOConfigurationReplacementIsBoundedAndDoesNotReleaseUnknownUpload(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"limit", "wrong-proof", "recovery", "cached-grant", "unknown-submit", "archive-failure"} {
		t.Run(kind, func(t *testing.T) {
			v := productionFixture(t)
			e, _ := recoveryEngine(t, v)
			id := v.input.Snapshot.Manifest.ReleaseID
			switch kind {
			case "cached-grant":
				e.Hook = func(stage string, q protocol.Request) error {
					if stage == "after_execute" && q.Kind == "authorize" {
						return errors.New("synthetic interrupted receipt")
					}
					return nil
				}
			case "unknown-submit":
				v.drop = true
			default:
				v.authorizeFailures = 5
			}
			if _, err := e.Submit(ctx, id, nil); err == nil {
				t.Fatal("failure fixture accepted")
			}
			e.Hook = nil
			proof := authorizationID(v)
			switch kind {
			case "wrong-proof":
				proof = strings.Repeat("0", 64)
			case "recovery":
				v.store.DB.Exec("UPDATE meta SET value='1' WHERE key='recovery_required'")
			case "archive-failure":
				v.store.DB.Exec("CREATE TRIGGER reject_archive BEFORE INSERT ON authorization_attempts BEGIN SELECT RAISE(ABORT,'synthetic storage failure'); END")
			}
			before := len(v.writes)
			if kind == "limit" {
				for range 2 {
					if err := e.ReplaceUnusedOPPOAuthorization(ctx, id, proof); err == nil {
						t.Fatal("failed replacement accepted")
					}
				}
				before = len(v.writes)
			}
			if err := e.ReplaceUnusedOPPOAuthorization(ctx, id, proof); err == nil || len(v.writes) != before {
				t.Fatal("ineligible replacement sent HTTP")
			}
			if _, err := e.Submit(ctx, id, nil); err == nil || len(v.writes) != before {
				t.Fatal("ordinary submit bypassed barrier")
			}
		})
	}
}

func TestOPPOAuthorizationHistoryMigratesVersionOneWithoutSettlingUnknown(t *testing.T) {
	v := productionFixture(t)
	e, _ := recoveryEngine(t, v)
	v.authorizeFailures = 1
	id := v.input.Snapshot.Manifest.ReleaseID
	e.Submit(context.Background(), id, nil)
	if _, err := v.store.DB.Exec("DROP TABLE authorization_attempts; PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	root := v.store.Root
	v.store.Close()
	s, err := state.Open(root, manifest.Production, true)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.OPPOAuthorizationHistory(id)
	if err != nil || len(history) != 0 {
		t.Fatal("legacy read requires migration")
	}
	s.Close()
	s, err = state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, err := s.Operation(id+"/oppo", "authorize")
	if err != nil || op.Effect != "unknown" || op.Attempt != 1 {
		t.Fatal("migration settled old unknown")
	}
	if _, err = s.CheckOPPOAuthorizationReplacement(id+"/oppo", authorizationID(v)); err != nil {
		t.Fatal(err)
	}
}

func TestOPPOReplacementKillWorker(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--oppo-replacement-worker" {
		return
	}
	v := productionFixtureRoot(t, os.Args[len(os.Args)-1])
	e, _ := recoveryEngine(t, v)
	v.authorizeFailures = 1
	id := v.input.Snapshot.Manifest.ReleaseID
	e.Submit(context.Background(), id, nil)
	e.Hook = func(stage string, _ protocol.Request) error {
		if stage == "after_intent" {
			os.Stdout.WriteString("REPLACEMENT_INTENT\n")
			select {}
		}
		return nil
	}
	e.ReplaceUnusedOPPOAuthorization(context.Background(), id, authorizationID(v))
	os.Exit(7)
}

func TestKilledOPPOReplacementRetainsOldUnknownAndNewIntent(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "state")
	tmp := filepath.Join(base, "tmp")
	os.Mkdir(tmp, 0700)
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestOPPOReplacementKillWorker$", "--", "--oppo-replacement-worker", root)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != "REPLACEMENT_INTENT" {
		t.Fatal("worker did not persist replacement intent")
	}
	cmd.Process.Kill()
	cmd.Wait()
	s, err := state.Open(root, manifest.Production, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	op, err := s.Operation("oppo-production-test/oppo", "authorize")
	if err != nil || op.Effect != "intent" || op.Attempt != 2 {
		t.Fatal("interrupted replacement lost its barrier")
	}
	history, err := s.OPPOAuthorizationHistory("oppo-production-test")
	if err != nil || len(history) != 1 || history[0].Effect != "unknown" {
		t.Fatal("kill lost original unknown")
	}
	if err = s.CheckWritable(op.Request.Scope, op.Request.TargetID); !errors.Is(err, state.ErrConflict) {
		t.Fatal("killed replacement became writable")
	}
}
