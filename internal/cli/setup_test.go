package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
)

type projectTestInspector struct {
	infos map[string]artifact.Info
	err   error
}

func (i projectTestInspector) Inspect(_ context.Context, path string) (artifact.Info, error) {
	if i.err != nil {
		return artifact.Info{}, i.err
	}
	if info, ok := i.infos[path]; ok {
		return info, nil
	}
	return artifact.Info{}, errors.New("fixture is absent")
}

func setupFixture(t *testing.T) (string, string, projectTestInspector) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runtime")
	apk := filepath.Join(t.TempDir(), "explicit.apk")
	if err := os.WriteFile(apk, []byte("synthetic APK placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	info := artifact.Info{PackageName: "com.example.colleague", VersionCode: 42, VersionName: "4.2", SignerSHA256: strings.Repeat("b", 64), ABI: []string{"arm64-v8a"}}
	return root, apk, projectTestInspector{infos: map[string]artifact.Info{apk: info}}
}

func setupOPPOAnswers() string { return "yes\noppo\nfixture-account\n\nyes\n" }

func TestSetupExplicitSignerAndPrivateReusableProfile(t *testing.T) {
	root, apk, inspector := setupFixture(t)
	var stderr bytes.Buffer
	p, err := setupProject(context.Background(), root, "team-app", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), &stderr)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	loaded, data, err := loadProject(root, p.ID)
	if err != nil || loaded.PackageName != p.PackageName || loaded.SignerSHA256 != p.SignerSHA256 || len(loaded.Channels) != 1 || loaded.Channels[0].CredentialName != "client-secret" {
		t.Fatal(loaded, err)
	}
	if !strings.Contains(stderr.String(), p.SignerSHA256) || !strings.Contains(stderr.String(), "不是密钥内容") {
		t.Fatal("trust confirmation or secret guidance missing")
	}
	for _, forbidden := range []string{apk, "version_code", "version_name", "notes_file", `"apk"`, `"secret"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("release material persisted in reusable profile: %s", forbidden)
		}
	}
	path, _ := projectPath(root, p.ID)
	info, _ := os.Stat(path)
	dir, _ := os.Stat(filepath.Dir(path))
	if info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
		t.Fatal("profile must remain private", info.Mode(), dir.Mode())
	}
	if _, err := os.Stat(filepath.Join(root, "state.sqlite")); !os.IsNotExist(err) {
		t.Fatal("setup touched publishing database")
	}
}

func TestSetupCancelEOFFailureAndReplacementPreserveProfile(t *testing.T) {
	root, apk, inspector := setupFixture(t)
	for name, input := range map[string]string{
		"before signer": "", "reject signer": "no\n", "during config": "yes\noppo\ncancel\n", "final EOF": "yes\noppo\nfixture-account\n\n", "reject final": "yes\noppo\nfixture-account\n\nno\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := setupProject(context.Background(), root, "cancelled", apk, false, inspector, strings.NewReader(input), io.Discard)
			if !errors.Is(err, errSetupCanceled) {
				t.Fatal("cancellation not explicit", err)
			}
			path, _ := projectPath(root, "cancelled")
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("cancelled setup saved a profile")
			}
		})
	}
	if _, err := setupProject(context.Background(), root, "team", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), io.Discard); err != nil {
		t.Fatal(err)
	}
	_, original, _ := loadProject(root, "team")
	if _, err := setupProject(context.Background(), root, "team", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), io.Discard); err == nil {
		t.Fatal("replacement without --replace accepted")
	}
	_, err := setupProject(context.Background(), root, "team", apk, true, inspector, strings.NewReader("yes\noppo\nnew-account\n\nno\n"), io.Discard)
	if !errors.Is(err, errSetupCanceled) {
		t.Fatal(err)
	}
	_, current, _ := loadProject(root, "team")
	if string(current) != string(original) {
		t.Fatal("cancelled replacement changed profile")
	}
	if _, err := setupProject(context.Background(), root, "team", apk, true, inspector, strings.NewReader("yes\noppo\nnew-account\n\nyes\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
	p, _, _ := loadProject(root, "team")
	if p.Channels[0].Account != "new-account" {
		t.Fatal("confirmed replacement was not saved")
	}
	if err := saveProject(root, p, original); err == nil {
		t.Fatal("stale replacement overwrote another setup")
	}
}

func TestSetupCanonicalCLIOutputAndCancellation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, apk, inspector := setupFixture(t)
	root := defaultRoot(manifest.Production)
	var out, stderr bytes.Buffer
	code := runSetup(context.Background(), manifest.Production, root, []string{"--project", "team", "--apk", apk}, inspector, strings.NewReader(setupOPPOAnswers()), &out, &stderr)
	var result map[string]any
	if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || result["saved"] != true || strings.Count(out.String(), "\n") != 1 || !strings.Contains(stderr.String(), "配置向导") {
		t.Fatal(code, out.String(), stderr.String())
	}
	out.Reset()
	code = runSetup(context.Background(), manifest.Production, root, []string{"--project", "cancel", "--apk", apk}, inspector, strings.NewReader(""), &out, io.Discard)
	if code != 130 || json.Unmarshal(out.Bytes(), &result) != nil || result["canceled"] != true || result["saved"] != false {
		t.Fatal(code, out.String())
	}
}

func TestSetupContextCancelWhileWaitingDoesNotSave(t *testing.T) {
	root, apk, inspector := setupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, err := setupProject(ctx, root, "cancel", apk, false, inspector, reader, io.Discard)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ctrl-C context did not stop input wait")
	}
}

func TestSetupFailsClosedBeforeSavingUntrustedAPKAndUnsafeInput(t *testing.T) {
	root, apk, inspector := setupFixture(t)
	for name, input := range map[string]string{
		"duplicate": "yes\noppo,oppo\nfixture\n\nfixture\n\nyes\n", "unknown": "yes\nother\n", "unsafe account": "yes\noppo\n../account\n\nyes\n", "reserved credential": "yes\noppo\nfixture\ntoken-other\nyes\n", "control": "yes\noppo\nfixture\nbad\x00name\nyes\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := setupProject(context.Background(), root, "unsafe", apk, false, inspector, strings.NewReader(input), io.Discard); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	if _, err := setupProject(context.Background(), root, "../escape", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), io.Discard); err == nil {
		t.Fatal("unsafe project path accepted")
	}
	inspector.err = errors.New("signature not verified")
	if _, err := setupProject(context.Background(), root, "untrusted", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), io.Discard); err == nil {
		t.Fatal("failed signature accepted")
	}
	inspector.err = nil
	info := inspector.infos[apk]
	info.Debuggable = true
	inspector.infos[apk] = info
	if _, err := setupProject(context.Background(), root, "debug", apk, false, inspector, strings.NewReader(setupOPPOAnswers()), io.Discard); err == nil {
		t.Fatal("debug APK accepted")
	}
}

func TestSetupAllChannelReferencesWithoutSecrets(t *testing.T) {
	root, apk, inspector := setupFixture(t)
	icon := filepath.Join(t.TempDir(), "icon.png")
	os.WriteFile(icon, []byte("synthetic icon"), 0600)
	answers := strings.Join([]string{"yes", "", "oppo-public", "", "", "", "vivo-public", "", "", "team@example.invalid", "", icon, "https://example.invalid/privacy", "", "", "zh-CN", "", "", "honor-public", "zh-CN", "", "", "100000", "100001", "yes", ""}, "\n")
	p, err := setupProject(context.Background(), root, "six", apk, false, inspector, strings.NewReader(answers), io.Discard)
	if err != nil || len(p.Channels) != 6 {
		t.Fatal(p, err)
	}
	if p.Channels[2].PublicKeyName != "api-public-key" || p.Channels[5].AppID != "100001" || p.Channels[3].LanguageID != "zh-CN" {
		t.Fatal(p)
	}
}
