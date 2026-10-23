package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
	"go.yaml.in/yaml/v3"
)

type prepareChannel struct {
	Channel        string `json:"channel"`
	Account        string `json:"account"`
	APK            string `json:"apk"`
	CredentialName string `json:"credential_name"`
	ClientID       string `json:"client_id,omitempty"`
	AppID          string `json:"app_id,omitempty"`
	PublicKeyName  string `json:"public_key_name,omitempty"`
	LanguageID     string `json:"language_id,omitempty"`
	IconPath       string `json:"icon_path,omitempty"`
	PrivacyURL     string `json:"privacy_url,omitempty"`
}
type prepareInput struct {
	SchemaVersion int              `json:"schema_version"`
	BatchID       string           `json:"batch_id"`
	PackageName   string           `json:"package_name"`
	VersionCode   int64            `json:"version_code"`
	VersionName   string           `json:"version_name"`
	SignerSHA256  string           `json:"signer_sha256"`
	NotesFile     string           `json:"notes_file"`
	SourceCommit  *string          `json:"source_commit,omitempty"`
	Channels      []prepareChannel `json:"channels"`
}

func prepareTemplate() prepareInput {
	input := prepareInput{SchemaVersion: 1, BatchID: "example-1", PackageName: "com.example.app", VersionCode: 1, VersionName: "1.0.0", SignerSHA256: "REPLACE_WITH_APPROVED_APK_CERTIFICATE_SHA256", NotesFile: "/absolute/path/release-notes.txt"}
	names := map[string]string{"oppo": "client-secret", "vivo": "access-secret", "xiaomi": "api-secret", "huawei": "service-account", "honor": "client-secret", "tencent": "api-secret"}
	for _, ch := range manifest.Channels {
		c := prepareChannel{Channel: ch, Account: ch + "-main", APK: "/absolute/path/" + ch + ".apk", CredentialName: names[ch]}
		if ch == "oppo" {
			c.Account = "REPLACE_WITH_OPPO_CLIENT_ID"
		}
		if ch != "huawei" {
			c.ClientID = "REPLACE_WITH_PUBLIC_ACCOUNT_ID"
		}
		if ch == "xiaomi" {
			c.PublicKeyName = "api-public-key"
			c.IconPath = "/absolute/path/icon.png"
			c.PrivacyURL = "https://example.com/privacy"
		}
		if ch == "tencent" {
			c.AppID = "REPLACE_WITH_APPLICATION_APPID"
		}
		input.Channels = append(input.Channels, c)
	}
	return input
}
func runPrepare(ctx context.Context, mode, root string, args []string, inspector artifact.Inspector, out, stderr io.Writer) int {
	f := flags(args[0], stderr)
	file := f.String("file", "", "explicit release input JSON")
	project := f.String("project", "", "saved reusable project ID")
	batch := f.String("batch", "", "new immutable batch ID")
	notes := f.String("notes", "", "absolute path to release notes")
	var apks projectAPKFlags
	f.Var(&apks, "apk", "explicit channel=absolute-APK-path; repeat for every saved channel")
	if err := parse(f, args[1:]); err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	seen := map[string]bool{}
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	bad := func(err error) int { return failure(out, mode, "fix_config", 2, err) }
	if mode == manifest.Production && filepath.Clean(root) != defaultRoot(mode) {
		return bad(fmt.Errorf("production preparation requires the canonical data directory"))
	}
	if seen["project"] || seen["batch"] || seen["notes"] || seen["apk"] {
		if args[0] != "prepare" || seen["file"] || *project == "" || *batch == "" || *notes == "" || len(apks) == 0 {
			return bad(fmt.Errorf("prepare requires either --file or --project with --batch, --notes and explicit --apk entries"))
		}
		b, err := prepareProject(ctx, mode, root, *project, *batch, *notes, apks, inspector)
		if err != nil {
			return bad(err)
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "effect": "offline", "project_id": *project, "batch_id": b.ID, "total_targets": len(b.Entries), "entries": b.Entries, "next_action": "review_and_preflight_each_channel"})
	}
	if *file == "" {
		return bad(fmt.Errorf("--file or a saved --project is required"))
	}
	if args[0] == "init" {
		data, _ := json.MarshalIndent(prepareTemplate(), "", "  ")
		fd, err := privatefs.Open(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return bad(fmt.Errorf("template destination must be a new regular file"))
		}
		_, err = fd.Write(data)
		if err == nil {
			err = fd.Sync()
		}
		closeErr := fd.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return bad(fmt.Errorf("could not save release template"))
		}
		return encodeCode(out, map[string]any{"schema_version": 1, "effect": "offline", "template": *file})
	}
	b, err := prepareRelease(ctx, mode, root, *file, inspector)
	if err != nil {
		return bad(err)
	}
	return encodeCode(out, map[string]any{"schema_version": 1, "effect": "offline", "batch_id": b.ID, "total_targets": len(b.Entries), "entries": b.Entries, "next_action": "review_and_preflight_each_channel"})
}
func prepareRelease(ctx context.Context, mode, root, file string, inspector artifact.Inspector) (batchFile, error) {
	var input prepareInput
	var result batchFile
	data, err := privatefs.Read(file, 65536)
	if err != nil || strictjson.Decode(data, &input) != nil {
		return result, fmt.Errorf("invalid release input JSON; secrets must remain in Keychain")
	}
	return prepareReleaseInput(ctx, mode, root, input, inspector)
}
func prepareReleaseInput(ctx context.Context, mode, root string, input prepareInput, inspector artifact.Inspector) (batchFile, error) {
	var result batchFile
	if input.SchemaVersion != 1 || !manifest.ValidID(input.BatchID) || !manifest.ValidHash(input.SignerSHA256) || len(input.Channels) == 0 || len(input.Channels) > 6 {
		return result, fmt.Errorf("invalid release identity, signer or channels")
	}
	if !filepath.IsAbs(input.NotesFile) {
		return result, fmt.Errorf("notes_file must be an absolute path")
	}
	notes, err := privatefs.Read(input.NotesFile, 1<<20)
	if err != nil {
		return result, fmt.Errorf("release notes are unavailable")
	}
	// Validate every explicit input before creating any frozen release. No vendor
	// client, credential resolver or network transport is constructed on this path.
	manifests := []manifest.Manifest{}
	configs := []channels.PreparationConfig{}
	seen := map[string]bool{}
	for _, c := range input.Channels {
		if !slices.Contains(manifest.Channels, c.Channel) || seen[c.Channel] || !account.ValidName(c.Account) || !credentials.ValidCredentialName(c.CredentialName) || !filepath.IsAbs(c.APK) {
			return result, fmt.Errorf("invalid explicit channel, account, credential reference or APK path")
		}
		seen[c.Channel] = true
		info, err := inspector.Inspect(ctx, c.APK)
		if err != nil {
			return result, fmt.Errorf("%s: APK signature or metadata validation failed", c.Channel)
		}
		digest, _, err := privatefs.Hash(c.APK)
		if err != nil {
			return result, fmt.Errorf("%s: APK cannot be read", c.Channel)
		}
		m := manifest.Manifest{SchemaVersion: 1, ReleaseID: input.BatchID + "-" + c.Channel, Mode: mode, PackageName: input.PackageName, VersionCode: input.VersionCode, VersionName: input.VersionName, SourceCommit: input.SourceCommit, Artifacts: map[string]manifest.Artifact{c.Channel: {Path: c.APK, SHA256: digest, SignerSHA256: input.SignerSHA256, ABI: info.ABI}}, NotesFile: input.NotesFile, ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{c.Channel: {AccountRef: c.Account, AppRef: input.PackageName, Artifact: c.Channel}}}
		if err = m.Validate(mode); err != nil {
			return result, err
		}
		if err = artifact.Check(ctx, c.APK, m, m.Artifacts[c.Channel], inspector); err != nil {
			return result, fmt.Errorf("%s: APK differs from approved package, version or signer", c.Channel)
		}
		snapshot := manifest.Snapshot{Manifest: m, Notes: string(notes)}
		snapshot.Digest = snapshot.CalculateDigest()
		if err = snapshot.Verify(mode); err != nil {
			return result, err
		}
		manifests = append(manifests, m)
		configs = append(configs, channels.PreparationConfig{AccountQuery: channels.AccountQuery{Channel: c.Channel, Account: c.Account, ClientID: c.ClientID, SecretName: c.CredentialName, PublicKeyName: c.PublicKeyName, PackageName: input.PackageName, AppID: c.AppID, LanguageID: c.LanguageID}, IconPath: c.IconPath, PrivacyURL: c.PrivacyURL})
	}
	s, err := state.Open(root, mode, false)
	if err != nil {
		return result, err
	}
	result = batchFile{SchemaVersion: 1, ID: input.BatchID}
	for i, m := range manifests {
		// Preserve the original specification and notes in private storage. Retry of
		// the same input reuses the immutable intent; changed semantics must use a new ID.
		dir := filepath.Join(root, "inputs", m.ReleaseID)
		if err = privatefs.Dir(dir); err != nil {
			s.Close()
			return result, err
		}
		m.NotesFile = filepath.Join(dir, "release-notes.txt")
		if err = writeSame(m.NotesFile, notes); err != nil {
			s.Close()
			return result, err
		}
		yamlBytes, _ := yaml.Marshal(m)
		manifestPath := filepath.Join(dir, "manifest.yaml")
		if err = writeSame(manifestPath, yamlBytes); err != nil {
			s.Close()
			return result, err
		}
		summary, err := (&engine.Engine{Store: s, Inspector: inspector}).Plan(ctx, manifestPath)
		if err != nil {
			s.Close()
			return result, err
		}
		entry := batchEntry{ReleaseID: summary.ReleaseID, IntentDigest: summary.IntentDigest, Channel: configs[i].Channel}
		if configs[i].Channel == "oppo" {
			entry.CredentialName = configs[i].SecretName
		}
		if configs[i].Channel != "oppo" {
			entry.AccountConfig = filepath.Join(dir, "account-config.json")
			configBytes, _ := json.MarshalIndent(configs[i], "", "  ")
			if err = writeSame(entry.AccountConfig, configBytes); err != nil {
				s.Close()
				return result, err
			}
		}
		result.Entries = append(result.Entries, entry)
	}
	s.Close()
	specPath := filepath.Join(root, "inputs", input.BatchID+"-batch.json")
	specBytes, _ := json.MarshalIndent(result, "", "  ")
	if err = writeSame(specPath, specBytes); err != nil {
		return result, err
	}
	return createBatch(root, mode, specPath)
}
func writeSame(path string, data []byte) error {
	if old, err := privatefs.Read(path, 1<<20); err == nil {
		if string(old) != string(data) {
			return fmt.Errorf("existing frozen input differs; use a new batch ID")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".publisher-input-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Publish a complete file without replacing an existing immutable input.
	if err = os.Link(f.Name(), path); err != nil {
		if old, readErr := privatefs.Read(path, 1<<20); readErr == nil && string(old) == string(data) {
			return nil
		}
		return err
	}
	return privatefs.SyncDir(filepath.Dir(path))
}
