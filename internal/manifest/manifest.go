package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const Production = "production"
const Simulation = "simulation"

var Channels = []string{"oppo", "vivo", "xiaomi", "huawei", "honor", "tencent"}
var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

type Artifact struct {
	Path         string   `yaml:"path" json:"-"`
	SHA256       string   `yaml:"sha256" json:"sha256"`
	SignerSHA256 string   `yaml:"signer_sha256" json:"signer_sha256"`
	ABI          []string `yaml:"abi" json:"abi"`
}
type Target struct {
	AccountRef string `yaml:"account_ref" json:"account_ref"`
	AppRef     string `yaml:"app_ref" json:"app_ref"`
	Artifact   string `yaml:"artifact" json:"artifact"`
}
type Policy struct {
	Mode string `yaml:"mode" json:"mode"`
	At   string `yaml:"at,omitempty" json:"at,omitempty"`
}
type Patch struct {
	Action string `yaml:"action" json:"action"`
	Value  string `yaml:"value,omitempty" json:"value,omitempty"`
}
type Manifest struct {
	SchemaVersion int                 `yaml:"schema_version" json:"schema_version"`
	ReleaseID     string              `yaml:"release_id" json:"release_id"`
	Mode          string              `yaml:"mode" json:"mode"`
	SourceCommit  *string             `yaml:"source_commit,omitempty" json:"source_commit,omitempty"`
	PackageName   string              `yaml:"package_name" json:"package_name"`
	VersionCode   int64               `yaml:"version_code" json:"version_code"`
	VersionName   string              `yaml:"version_name" json:"version_name"`
	Artifacts     map[string]Artifact `yaml:"artifacts" json:"artifacts"`
	NotesFile     string              `yaml:"notes_file" json:"-"`
	ReleasePolicy Policy              `yaml:"release_policy" json:"release_policy"`
	Targets       map[string]Target   `yaml:"targets" json:"targets"`
	Metadata      map[string]Patch    `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}
type Snapshot struct {
	Manifest Manifest `json:"manifest"`
	Notes    string   `json:"notes"`
	Digest   string   `json:"intent_digest"`
}

func ValidID(s string) bool   { return safeID.MatchString(s) }
func ValidHash(s string) bool { return hashPattern.MatchString(s) }

// validateNode also rejects YAML's implicit scalar-to-string conversions for IDs.
func validateNode(n *yaml.Node, t reflect.Type) error {
	if n.Kind == yaml.AliasNode || n.Tag == "!!merge" {
		return fmt.Errorf("YAML aliases and merges are unsupported")
	}
	for t.Kind() == reflect.Pointer {
		if n.Tag == "!!null" {
			return nil
		}
		t = t.Elem()
	}
	if t.Kind() == reflect.String && (n.Kind != yaml.ScalarNode || n.Tag != "!!str") {
		return fmt.Errorf("string fields must be YAML strings (quote numeric IDs)")
	}
	if (t.Kind() == reflect.Int || t.Kind() == reflect.Int64) && (n.Kind != yaml.ScalarNode || n.Tag != "!!int") {
		return fmt.Errorf("integer fields require YAML integers; fractional values are not accepted")
	}
	if n.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Tag != "!!str" || seen[key.Value] {
				return fmt.Errorf("duplicate or non-string YAML key")
			}
			seen[key.Value] = true
			var child reflect.Type
			if t.Kind() == reflect.Map {
				child = t.Elem()
			} else if t.Kind() == reflect.Struct {
				for j := 0; j < t.NumField(); j++ {
					field := t.Field(j)
					if strings.Split(field.Tag.Get("yaml"), ",")[0] == key.Value {
						child = field.Type
						break
					}
				}
			}
			if child == nil {
				return fmt.Errorf("unknown field %q", key.Value)
			}
			if err := validateNode(value, child); err != nil {
				return fmt.Errorf("%s: %w", key.Value, err)
			}
		}
	} else if n.Kind == yaml.SequenceNode {
		if t.Kind() != reflect.Slice {
			return fmt.Errorf("unexpected sequence")
		}
		for _, child := range n.Content {
			if err := validateNode(child, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func Parse(data []byte, mode string) (Manifest, error) {
	var m Manifest
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return m, fmt.Errorf("manifest must be UTF-8 and at most 1 MiB")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return m, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(node.Content) != 1 {
		return m, fmt.Errorf("manifest is empty")
	}
	if err := validateNode(node.Content[0], reflect.TypeFor[Manifest]()); err != nil {
		return m, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("exactly one YAML document is required")
	}
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&m); err != nil {
		return m, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := m.Validate(mode); err != nil {
		return m, err
	}
	return m, nil
}

func (m *Manifest) Validate(mode string) error {
	if mode != Production && mode != Simulation {
		return fmt.Errorf("unsupported execution mode")
	}
	if m.SchemaVersion != 1 || m.Mode != mode || !ValidID(m.ReleaseID) {
		return fmt.Errorf("schema_version, mode or release_id is invalid")
	}
	if !packagePattern.MatchString(m.PackageName) || m.VersionCode <= 0 || strings.TrimSpace(m.VersionName) == "" {
		return fmt.Errorf("package and version are required")
	}
	if m.SourceCommit != nil {
		match, _ := regexp.MatchString(`^[a-f0-9]{40}$`, *m.SourceCommit)
		if !match {
			return fmt.Errorf("source_commit must be a full commit or null")
		}
	}
	if len(m.Targets) == 0 || len(m.Artifacts) == 0 {
		return fmt.Errorf("targets and artifacts must not be empty")
	}
	used := map[string]bool{}
	for channel, target := range m.Targets {
		if !slices.Contains(Channels, channel) || !ValidID(target.AccountRef) || !ValidID(target.AppRef) {
			return fmt.Errorf("invalid channel or stable account/app identity")
		}
		if _, ok := m.Artifacts[target.Artifact]; !ok {
			return fmt.Errorf("target %s references an absent artifact", channel)
		}
		used[target.Artifact] = true
	}
	for name, a := range m.Artifacts {
		if !ValidID(name) || !ValidHash(a.SHA256) || !ValidHash(a.SignerSHA256) || !used[name] {
			return fmt.Errorf("artifact %s has invalid digest, certificate or usage", name)
		}
		slices.Sort(a.ABI)
		a.ABI = slices.Compact(a.ABI)
		for _, abi := range a.ABI {
			if !slices.Contains([]string{"arm64-v8a", "armeabi-v7a", "x86", "x86_64"}, abi) {
				return fmt.Errorf("unsupported ABI")
			}
		}
		m.Artifacts[name] = a
	}
	if m.ReleasePolicy.Mode != "after_approval" && m.ReleasePolicy.Mode != "scheduled" {
		return fmt.Errorf("unsupported release policy")
	}
	if m.ReleasePolicy.Mode == "scheduled" {
		at, err := time.Parse(time.RFC3339, m.ReleasePolicy.At)
		if err != nil {
			return fmt.Errorf("scheduled policy requires an RFC3339 time")
		}
		m.ReleasePolicy.At = at.UTC().Format(time.RFC3339)
	} else if m.ReleasePolicy.At != "" {
		return fmt.Errorf("after_approval cannot carry a schedule")
	}
	for field, p := range m.Metadata {
		if !ValidID(field) || (p.Action != "set" && p.Action != "delete") || (p.Action == "delete" && p.Value != "") {
			return fmt.Errorf("metadata requires explicit set/delete patches")
		}
	}
	return nil
}

func (s Snapshot) CalculateDigest() string {
	m := s.Manifest
	m.ReleaseID = "" // The same semantic intent cannot acquire a second submission by changing its label.
	encoded, _ := json.Marshal(struct {
		Manifest Manifest `json:"manifest"`
		Notes    string   `json:"notes"`
	}{m, s.Notes})
	h := sha256.Sum256(encoded)
	return hex.EncodeToString(h[:])
}
func (s Snapshot) Verify(mode string) error {
	m := s.Manifest
	if err := m.Validate(mode); err != nil {
		return err
	}
	if s.Digest != s.CalculateDigest() || strings.TrimSpace(s.Notes) == "" {
		return fmt.Errorf("frozen intent is corrupt")
	}
	return nil
}
func Load(path, mode string) (Manifest, string, error) {
	data, err := privatefs.Read(path, 1<<20)
	if err != nil {
		return Manifest{}, "", err
	}
	m, err := Parse(data, mode)
	if err != nil {
		return m, "", err
	}
	base := filepath.Dir(path)
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	for key, a := range m.Artifacts {
		if a.Path == "" {
			return m, "", fmt.Errorf("artifact path is required")
		}
		a.Path = resolve(a.Path)
		m.Artifacts[key] = a
	}
	if m.NotesFile == "" {
		return m, "", fmt.Errorf("notes_file is required")
	}
	notes, err := privatefs.Read(resolve(m.NotesFile), 1<<20)
	if err != nil {
		return m, "", err
	}
	if len(notes) > 1<<20 || !utf8.Valid(notes) || strings.TrimSpace(string(notes)) == "" {
		return m, "", fmt.Errorf("notes must be nonempty UTF-8 and at most 1 MiB")
	}
	return m, string(notes), nil
}

func ApplyPatch(existing map[string]string, patch map[string]Patch) map[string]string {
	result := map[string]string{}
	for k, v := range existing {
		result[k] = v
	}
	for k, p := range patch {
		if p.Action == "delete" {
			delete(result, k)
		} else {
			result[k] = p.Value
		}
	}
	return result
}
