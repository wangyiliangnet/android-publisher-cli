package simulation

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"go.yaml.in/yaml/v3"
)

func Init(dir string) (string, error) {
	var absErr error
	dir, absErr = filepath.Abs(dir)
	if absErr != nil {
		return "", absErr
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return "", fmt.Errorf("simulation fixture directory must be empty")
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := privatefs.Dir(dir); err != nil {
		return "", err
	}
	a, err := artifact.CreateSimulation(filepath.Join(dir, "artifact.sim"), artifact.Info{PackageName: "com.example.publisher.simulation", VersionCode: 1, VersionName: "1.0", ABI: []string{"arm64-v8a"}})
	if err != nil {
		return "", err
	}
	a.Path = "artifact.sim"
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "simulation-1", Mode: manifest.Simulation, PackageName: "com.example.publisher.simulation", VersionCode: 1, VersionName: "1.0", Artifacts: map[string]manifest.Artifact{"fixture": a}, NotesFile: "notes.txt", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{}, Metadata: map[string]manifest.Patch{"title": {Action: "set", Value: "模拟更新 & +"}}}
	for _, channel := range manifest.Channels {
		m.Targets[channel] = manifest.Target{AccountRef: "simulation-account", AppRef: "simulation-app", Artifact: "fixture"}
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	if err = privatefs.Write(filepath.Join(dir, "notes.txt"), []byte("模拟更新：中文、空格 & +\n保留换行。\n")); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "manifest.yaml")
	return path, privatefs.Write(path, data)
}
