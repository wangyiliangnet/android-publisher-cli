package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

type projectPrepareInspector struct {
	base      artifact.Info
	overrides map[string]artifact.Info
}

func (i projectPrepareInspector) Inspect(_ context.Context, path string) (artifact.Info, error) {
	if info, ok := i.overrides[path]; ok {
		return info, nil
	}
	return i.base, nil
}

func reusableProjectFixture(t *testing.T) (string, projectProfile, string, []string, projectPrepareInspector) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runtime")
	p := projectProfile{SchemaVersion: 1, ID: "colleague", PackageName: "com.example.colleague", SignerSHA256: strings.Repeat("c", 64), Channels: []projectChannel{{Channel: "oppo", Account: "fixture", CredentialName: "client-secret"}, {Channel: "honor", Account: "honor-main", CredentialName: "client-secret", ClientID: "synthetic-public-id", LanguageID: "zh-CN"}}}
	if err := saveProject(root, p, nil); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(notes, []byte("本次更新保持说明原文\n- 同事流程测试\n"), 0600)
	apks := []string{}
	for _, c := range p.Channels {
		path := filepath.Join(t.TempDir(), c.Channel+".apk")
		os.WriteFile(path, []byte("synthetic "+c.Channel), 0600)
		apks = append(apks, c.Channel+"="+path)
	}
	inspector := projectPrepareInspector{base: artifact.Info{PackageName: p.PackageName, VersionCode: 248, VersionName: "3.2.7", SignerSHA256: p.SignerSHA256, ABI: []string{"arm64-v8a"}}}
	return root, p, notes, apks, inspector
}

func TestPrepareProjectExplicitFilesAndImmutableRetry(t *testing.T) {
	root, p, notes, apks, inspector := reusableProjectFixture(t)
	b, err := prepareProject(context.Background(), manifest.Production, root, p.ID, "release-248", notes, apks, inspector)
	if err != nil || len(b.Entries) != 2 {
		t.Fatal(b, err)
	}
	if _, err := prepareProject(context.Background(), manifest.Production, root, p.ID, "release-248", notes, apks, inspector); err != nil {
		t.Fatal("identical preparation could not resume", err)
	}
	s, err := state.Open(root, manifest.Production, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range b.Entries {
		snapshot, err := s.Load(entry.ReleaseID)
		if err != nil || snapshot.Manifest.PackageName != p.PackageName || snapshot.Manifest.VersionCode != 248 || snapshot.Manifest.VersionName != "3.2.7" || snapshot.Manifest.ReleasePolicy.Mode != "after_approval" || snapshot.Notes != "本次更新保持说明原文\n- 同事流程测试\n" {
			t.Fatal(snapshot, err)
		}
		if err := artifact.VerifyFrozen(context.Background(), root, snapshot, inspector); err != nil {
			t.Fatal("frozen artifact failed", err)
		}
	}
	var operations int
	if err := s.DB.QueryRow("SELECT count(*) FROM operations").Scan(&operations); err != nil || operations != 0 {
		t.Fatal("offline project preparation created operations", operations, err)
	}
	s.Close()
	os.WriteFile(notes, []byte("changed notes"), 0600)
	if _, err := prepareProject(context.Background(), manifest.Production, root, p.ID, "release-248", notes, apks, inspector); err == nil {
		t.Fatal("changed notes overwrote frozen batch")
	}
	_, stored, err := loadProject(root, p.ID)
	if err != nil || strings.Contains(string(stored), "3.2.7") || strings.Contains(string(stored), notes) {
		t.Fatal("preparation modified reusable project", err)
	}
}

func TestPrepareProjectRejectsMissingDuplicateExtraPathsAndVersionsBeforeFreeze(t *testing.T) {
	root, p, notes, apks, inspector := reusableProjectFixture(t)
	for name, entries := range map[string][]string{
		"missing": apks[:1], "extra": append(append([]string{}, apks...), "vivo=/tmp/explicit.apk"), "duplicate": {apks[0], apks[0]}, "relative": {"oppo=relative.apk", apks[1]}, "newline": {apks[0] + "\n", apks[1]}, "unknown": {"other=/tmp/a.apk", apks[1]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareProject(context.Background(), manifest.Production, root, p.ID, "bad-"+name, notes, entries, inspector); err == nil {
				t.Fatal("ambiguous inputs accepted")
			}
		})
	}
	_, second, _ := strings.Cut(apks[1], "=")
	for name, mutate := range map[string]func(*artifact.Info){
		"version code": func(i *artifact.Info) { i.VersionCode++ }, "version name": func(i *artifact.Info) { i.VersionName = "3.2.8" }, "package": func(i *artifact.Info) { i.PackageName = "com.other.app" }, "signer": func(i *artifact.Info) { i.SignerSHA256 = strings.Repeat("a", 64) }, "debug": func(i *artifact.Info) { i.Debuggable = true },
	} {
		t.Run(name, func(t *testing.T) {
			changed := inspector.base
			mutate(&changed)
			inspector.overrides = map[string]artifact.Info{second: changed}
			if _, err := prepareProject(context.Background(), manifest.Production, root, p.ID, "must-not-freeze", notes, apks, inspector); err == nil {
				t.Fatal("unapproved APK accepted")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "state.sqlite")); !os.IsNotExist(err) {
		t.Fatal("invalid inputs created publishing state")
	}
}

func TestProjectValidationAndStrictPrivateProfile(t *testing.T) {
	root, p, _, _, _ := reusableProjectFixture(t)
	path, _ := projectPath(root, p.ID)
	valid, _ := json.Marshal(p)
	for name, data := range map[string][]byte{
		"secret":         []byte(strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"secret":"never-a-real-secret"`, 1)),
		"apk":            []byte(strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"apk":"/tmp/old.apk"`, 1)),
		"duplicate":      []byte(strings.Replace(string(valid), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1)),
		"alias":          []byte(strings.Replace(string(valid), `"schema_version"`, `"Schema_Version"`, 1)),
		"wrong identity": []byte(strings.Replace(string(valid), `"project_id":"colleague"`, `"project_id":"different"`, 1)),
	} {
		t.Run(name, func(t *testing.T) {
			os.WriteFile(path, data, 0600)
			if _, _, err := loadProject(root, p.ID); err == nil {
				t.Fatal("malformed/private data profile accepted")
			}
		})
	}
	os.WriteFile(path, valid, 0600)
	os.Remove(path)
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, valid, 0600)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadProject(root, p.ID); err == nil {
		t.Fatal("profile symlink accepted")
	}
	other := t.TempDir()
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(other, "projects")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadProject(other, p.ID); err == nil {
		t.Fatal("project directory symlink accepted")
	}
}

func TestProjectPrivacyAndPublicIdentityValidation(t *testing.T) {
	_, p, _, _, _ := reusableProjectFixture(t)
	base := projectChannel{Channel: "xiaomi", Account: "xiaomi-main", CredentialName: "api-secret", ClientID: "team@example.invalid", PublicKeyName: "api-public-key", IconPath: "/tmp/icon.png", PrivacyURL: "https://example.invalid/privacy"}
	p.Channels = []projectChannel{base}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"http://example.invalid/privacy", "https://user:pass@example.invalid/privacy", "https://example.invalid/\nprivacy", "https:///missing-host", "https://example.invalid/privacy#fragment", "https://example.invalid/privacy?token=synthetic", "https://example.invalid/has space"} {
		p.Channels[0].PrivacyURL = value
		if err := p.validate(); err == nil {
			t.Fatal("invalid privacy URL accepted", value)
		}
	}
	p.Channels[0] = base
	p.Channels[0].ClientID = "public\nidentity"
	if err := p.validate(); err == nil {
		t.Fatal("newline public identity accepted")
	}
	p.Channels[0] = base
	p.Channels = append(p.Channels, base)
	if err := p.validate(); err == nil {
		t.Fatal("duplicate channel accepted")
	}
}

func TestPrepareCLIRejectsMixedOrIncompleteProjectFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"prepare", "--file", "input.json", "--project", "team", "--batch", "release", "--notes", "/tmp/notes", "--apk", "oppo=/tmp/explicit.apk"},
		{"prepare", "--file", "input.json", "--project", ""},
		{"prepare", "--project", "team"},
		{"init", "--file", "new.json", "--project", "team"},
		{"prepare", "--project", "../team", "--batch", "release", "--notes", "/tmp/notes", "--apk", "oppo=/tmp/explicit.apk"},
	} {
		var out bytes.Buffer
		if code := runPrepare(context.Background(), manifest.Production, defaultRoot(manifest.Production), args, projectPrepareInspector{}, &out, &bytes.Buffer{}); code != 2 || !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatal(args, code, out.String())
		}
	}
}
