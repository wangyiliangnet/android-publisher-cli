package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
)

func TestStrictParsingAndDigest(t *testing.T) {
	path, err := simulation.Init(filepath.Join(t.TempDir(), "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, notes, err := manifest.Load(path, manifest.Simulation)
	if err != nil {
		t.Fatal(err)
	}
	original := manifest.Snapshot{Manifest: m, Notes: notes}
	original.Digest = original.CalculateDigest()
	for name, changed := range map[string]string{
		"fractional_schema":  strings.Replace(string(data), "schema_version: 1", "schema_version: 1.9", 1),
		"fractional_version": strings.Replace(string(data), "version_code: 1", "version_code: 1.9", 1),
		"duplicate":          string(data) + "\nrelease_id: other\n",
		"unknown":            string(data) + "\napi_secret: ignored\n",
		"numeric_identity":   strings.ReplaceAll(string(data), "simulation-app", "9007199254740993"),
		"alias":              strings.ReplaceAll(string(data), "simulation-app", "*missing"),
		"extra_document":     string(data) + "\n---\nrelease_id: other\n",
		"wrong_mode":         strings.ReplaceAll(string(data), "mode: simulation", "mode: production"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := manifest.Parse([]byte(changed), manifest.Simulation); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
	m.ReleaseID = "another-label"
	duplicate := manifest.Snapshot{Manifest: m, Notes: notes}
	if duplicate.CalculateDigest() != original.Digest {
		t.Fatal("release label must not evade semantic deduplication")
	}
	duplicate.Notes += " changed"
	if duplicate.CalculateDigest() == original.Digest {
		t.Fatal("notes omitted from intent")
	}
	if err := original.Verify(manifest.Production); err == nil {
		t.Fatal("simulation accepted as production")
	}
	copyData := strings.ReplaceAll(string(data), "simulation-app", `"9007199254740993"`)
	quoted, err := manifest.Parse([]byte(copyData), manifest.Simulation)
	if err != nil {
		t.Fatal(err)
	}
	if quoted.Targets["xiaomi"].AppRef != "9007199254740993" {
		t.Fatal("large ID lost precision")
	}
}

func TestPatchPreserveSetDelete(t *testing.T) {
	original := map[string]string{"title": "old", "privacy": "keep", "icon": "old-icon"}
	got := manifest.ApplyPatch(original, map[string]manifest.Patch{"title": {Action: "set", Value: ""}, "icon": {Action: "delete"}})
	if got["title"] != "" || got["privacy"] != "keep" {
		t.Fatal(got)
	}
	if _, ok := got["icon"]; ok {
		t.Fatal("delete ignored")
	}
	if original["title"] != "old" {
		t.Fatal("patch modified source snapshot")
	}
}
