package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/strictjson"
)

// A project has no release files or version, and contains references rather than
// credential values. Strict decoding rejects accidental secret/APK fields.
type projectProfile struct {
	SchemaVersion int              `json:"schema_version"`
	ID            string           `json:"project_id"`
	PackageName   string           `json:"package_name"`
	SignerSHA256  string           `json:"signer_sha256"`
	Channels      []projectChannel `json:"channels"`
}

type projectChannel struct {
	Channel        string `json:"channel"`
	Account        string `json:"account"`
	CredentialName string `json:"credential_name"`
	ClientID       string `json:"client_id,omitempty"`
	AppID          string `json:"app_id,omitempty"`
	PublicKeyName  string `json:"public_key_name,omitempty"`
	LanguageID     string `json:"language_id,omitempty"`
	IconPath       string `json:"icon_path,omitempty"`
	PrivacyURL     string `json:"privacy_url,omitempty"`
}

var projectPublicID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._+\-]{0,255}$`)
var projectLanguageID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_\-]{0,31}$`)

func projectText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029'
	})
}
func projectAbsolutePath(s string) bool {
	return s != "" && len(s) <= 4096 && projectText(s) && filepath.IsAbs(s) && filepath.Clean(s) == s
}
func projectPrivacyURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && len(s) <= 2048 && projectText(s) && !strings.ContainsAny(s, " \\") && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.RawQuery == ""
}
func (p projectProfile) validate() error {
	if p.SchemaVersion != 1 || !manifest.ValidID(p.ID) || !channels.ValidInspectionPackage(p.PackageName) || !manifest.ValidID(p.PackageName) || !manifest.ValidHash(p.SignerSHA256) || len(p.Channels) == 0 || len(p.Channels) > len(manifest.Channels) {
		return fmt.Errorf("项目标识、包名、签名或渠道配置无效")
	}
	seen := map[string]bool{}
	for _, c := range p.Channels {
		if !slices.Contains(manifest.Channels, c.Channel) || seen[c.Channel] || !account.ValidName(c.Account) || !credentials.ValidCredentialName(c.CredentialName) {
			return fmt.Errorf("渠道重复，或账号标识和钥匙串引用无效")
		}
		seen[c.Channel] = true
		if c.ClientID != "" && !projectPublicID.MatchString(c.ClientID) || c.AppID != "" && !account.ValidName(c.AppID) || c.LanguageID != "" && !projectLanguageID.MatchString(c.LanguageID) {
			return fmt.Errorf("公开账号标识、应用 ID 或语言标识无效")
		}
		switch c.Channel {
		case "oppo", "huawei":
			if c.ClientID != "" {
				return fmt.Errorf("OPPO 使用账号字段作为 client_id；华为从钥匙串 JSON 读取身份")
			}
		default:
			if c.ClientID == "" {
				return fmt.Errorf("缺少渠道的公开账号标识")
			}
		}
		if c.Channel == "tencent" && c.AppID == "" {
			return fmt.Errorf("应用宝必须指定该应用的 AppID，不能使用账号 uid 代替")
		}
		if c.LanguageID != "" && c.Channel != "huawei" && c.Channel != "honor" {
			return fmt.Errorf("语言标识仅适用于华为和荣耀")
		}
		if c.Channel == "xiaomi" {
			if !credentials.ValidCredentialName(c.PublicKeyName) || !projectAbsolutePath(c.IconPath) || !projectPrivacyURL(c.PrivacyURL) || c.CredentialName == c.PublicKeyName {
				return fmt.Errorf("小米需要独立的公钥证书引用、绝对图标路径和公开 HTTPS 隐私网址")
			}
		} else if c.PublicKeyName != "" || c.IconPath != "" || c.PrivacyURL != "" {
			return fmt.Errorf("公钥证书、图标和隐私网址字段仅适用于小米")
		}
	}
	return nil
}

func projectPath(root, id string) (string, error) {
	if !manifest.ValidID(id) {
		return "", fmt.Errorf("项目 ID 只能使用字母、数字、点、短横线和下划线，最长 128 字符")
	}
	return filepath.Join(root, "projects", id+".json"), nil
}

func loadProject(root, id string) (projectProfile, []byte, error) {
	var p projectProfile
	path, err := projectPath(root, id)
	if err != nil {
		return p, nil, err
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return p, nil, fmt.Errorf("项目目录不存在或不是本机普通目录；请先运行 setup")
	}
	data, err := privatefs.Read(path, 65536)
	if err != nil {
		return p, nil, fmt.Errorf("无法读取项目配置；请先运行 setup --project ID")
	}
	if strictjson.Decode(data, &p) != nil || p.ID != id {
		return p, nil, fmt.Errorf("项目配置格式无效；禁止包含凭据值或单次发布文件")
	}
	if err := p.validate(); err != nil {
		return p, nil, err
	}
	return p, data, nil
}

// expected is the exact previous profile the user agreed to replace. The lock
// and comparison prevent a long-running wizard from overwriting another setup.
func saveProject(root string, p projectProfile, expected []byte) error {
	if err := p.validate(); err != nil {
		return err
	}
	path, _ := projectPath(root, p.ID)
	if err := privatefs.Dir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("无法创建私有项目目录")
	}
	lock, err := privatefs.Open(filepath.Join(filepath.Dir(path), ".setup.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return fmt.Errorf("无法锁定项目配置")
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("另一条配置向导正在保存，请稍后重试")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := privatefs.Read(path, 65536)
	if expected == nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("项目已存在，未覆盖；修改需使用 --replace 并重新确认")
		}
	} else if err != nil || string(current) != string(expected) {
		return fmt.Errorf("项目配置已在向导期间变化，未覆盖；请重新运行向导")
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if expected != nil {
		return privatefs.Write(path, data)
	}
	// Publish the complete file only if absent, even if a non-CLI writer ignores
	// our advisory lock. The private temp file is fsynced before publication.
	f, err := os.CreateTemp(filepath.Dir(path), ".project-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Link(f.Name(), path); err != nil {
		return fmt.Errorf("项目已存在或无法保存，未覆盖现有配置")
	}
	return privatefs.SyncDir(filepath.Dir(path))
}

type projectAPKFlags []string

func (v *projectAPKFlags) String() string { return strings.Join(*v, ",") }
func (v *projectAPKFlags) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func prepareProject(ctx context.Context, mode, root, projectID, batchID, notes string, apks []string, inspector artifact.Inspector) (batchFile, error) {
	var empty batchFile
	p, _, err := loadProject(root, projectID)
	if err != nil {
		return empty, err
	}
	if !manifest.ValidID(batchID) || !projectAbsolutePath(notes) {
		return empty, fmt.Errorf("批次 ID 无效，或更新说明不是明确的绝对文件路径")
	}
	paths := map[string]string{}
	for _, entry := range apks {
		ch, path, ok := strings.Cut(entry, "=")
		if !ok || !slices.Contains(manifest.Channels, ch) || paths[ch] != "" || !projectAbsolutePath(path) {
			return empty, fmt.Errorf("每个 --apk 必须是唯一的 channel=绝对路径，不支持自动搜索或猜测 APK")
		}
		paths[ch] = path
	}
	if len(paths) != len(p.Channels) {
		return empty, fmt.Errorf("请为项目的每个渠道明确指定一次 --apk，不能遗漏或增加渠道")
	}
	input := prepareInput{SchemaVersion: 1, BatchID: batchID, PackageName: p.PackageName, SignerSHA256: p.SignerSHA256, NotesFile: notes}
	for _, c := range p.Channels {
		path := paths[c.Channel]
		if path == "" {
			return empty, fmt.Errorf("缺少已配置渠道的明确 APK 路径")
		}
		info, err := inspector.Inspect(ctx, path)
		if err != nil || info.PackageName != p.PackageName || info.SignerSHA256 != p.SignerSHA256 || info.Debuggable || info.VersionCode <= 0 || info.VersionName == "" || !projectText(info.VersionName) {
			return empty, fmt.Errorf("%s：APK 验签失败，或包名和签名与已批准项目不符", c.Channel)
		}
		if input.VersionCode == 0 {
			input.VersionCode, input.VersionName = info.VersionCode, info.VersionName
		} else if input.VersionCode != info.VersionCode || input.VersionName != info.VersionName {
			return empty, fmt.Errorf("各渠道 APK 的版本号和版本名称必须一致")
		}
		input.Channels = append(input.Channels, prepareChannel{Channel: c.Channel, Account: c.Account, APK: path, CredentialName: c.CredentialName, ClientID: c.ClientID, AppID: c.AppID, PublicKeyName: c.PublicKeyName, LanguageID: c.LanguageID, IconPath: c.IconPath, PrivacyURL: c.PrivacyURL})
	}
	return prepareReleaseInput(ctx, mode, root, input, inspector)
}
