package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

var errSetupCanceled = errors.New("配置已取消，未保存项目配置")

type setupPrompt struct {
	ctx context.Context
	in  *bufio.Scanner
	out io.Writer
}

func newSetupPrompt(ctx context.Context, in io.Reader, out io.Writer) setupPrompt {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1024), 8192)
	return setupPrompt{ctx: ctx, in: scanner, out: out}
}

func (p *setupPrompt) ask(label, fallback string) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	if fallback != "" {
		fmt.Fprintf(p.out, "%s [%s]：", label, fallback)
	} else {
		fmt.Fprintf(p.out, "%s：", label)
	}
	type readResult struct {
		ok   bool
		line string
		err  error
	}
	read := make(chan readResult, 1)
	go func() {
		ok := p.in.Scan()
		read <- readResult{ok: ok, line: p.in.Text(), err: p.in.Err()}
	}()
	var answer readResult
	select {
	case <-p.ctx.Done():
		return "", p.ctx.Err()
	case answer = <-read:
	}
	if !answer.ok {
		if answer.err != nil {
			return "", fmt.Errorf("输入无法读取或单行过长，未保存配置")
		}
		return "", errSetupCanceled
	}
	line := answer.line
	if !projectText(line) {
		return "", fmt.Errorf("输入包含控制字符，未保存配置")
	}
	line = strings.TrimSpace(line)
	if line == "取消" || strings.EqualFold(line, "cancel") || line == ":q" {
		return "", errSetupCanceled
	}
	if line == "" {
		return fallback, nil
	}
	return line, nil
}

func (p *setupPrompt) confirm(label string) error {
	answer, err := p.ask(label+"（输入 yes 或 确认；其他输入取消）", "")
	if err != nil {
		return err
	}
	if answer != "确认" && !strings.EqualFold(answer, "yes") {
		return errSetupCanceled
	}
	return nil
}

func runSetup(ctx context.Context, mode, root string, args []string, inspector artifact.Inspector, in io.Reader, out, stderr io.Writer) int {
	f := flags("setup", stderr)
	id := f.String("project", "", "reusable project ID")
	apk := f.String("apk", "", "explicit trusted, signed APK absolute path; prompted when omitted")
	replace := f.Bool("replace", false, "explicitly replace an existing project after final confirmation")
	if err := parse(f, args); err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	if mode != manifest.Production || filepath.Clean(root) != defaultRoot(mode) {
		return failure(out, mode, "fix_config", 2, fmt.Errorf("setup 仅支持正式工具的本机标准数据目录；不会访问渠道或钥匙串"))
	}
	p, err := setupProject(ctx, root, *id, *apk, *replace, inspector, in, stderr)
	if errors.Is(err, errSetupCanceled) || errors.Is(err, context.Canceled) {
		return encodeCodeWithStatus(out, map[string]any{"schema_version": 1, "effect": "offline", "canceled": true, "saved": false, "next_action": "rerun_setup"}, 130)
	}
	if err != nil {
		return failure(out, mode, "fix_config", 2, err)
	}
	return encodeCode(out, map[string]any{"schema_version": 1, "effect": "offline", "project_id": p.ID, "package_name": p.PackageName, "signer_sha256": p.SignerSHA256, "channels": p.Channels, "saved": true, "next_action": "import_credentials_then_prepare"})
}

func encodeCodeWithStatus(out io.Writer, v any, status int) int {
	if encode(out, v) != nil {
		return 5
	}
	return status
}

func setupProject(ctx context.Context, root, id, apk string, replace bool, inspector artifact.Inspector, in io.Reader, stderr io.Writer) (projectProfile, error) {
	var result projectProfile
	path, err := projectPath(root, id)
	if err != nil {
		return result, err
	}
	var previous []byte
	if _, err = os.Lstat(path); err == nil {
		if !replace {
			return result, fmt.Errorf("项目已存在；只有 --replace 和最终确认才能替换")
		}
		_, previous, err = loadProject(root, id)
		if err != nil {
			return result, err
		}
	} else if !os.IsNotExist(err) {
		return result, fmt.Errorf("无法读取已有项目，未保存配置")
	} else if replace {
		return result, fmt.Errorf("项目不存在，请移除 --replace 后创建")
	}
	prompt := newSetupPrompt(ctx, in, stderr)
	fmt.Fprintln(stderr, "配置向导仅在本机保存公开标识和钥匙串引用，不上传或提审。任意步骤输入 cancel 或按 Ctrl-D 可取消。")
	fmt.Fprintln(stderr, "请勿输入密钥、token 或私钥。凭据请另用 release credentials import 的隐藏输入或文件导入。")
	if apk == "" {
		apk, err = prompt.ask("请明确选择一份你信任的已签名 APK（绝对路径，不要拖入引号）", "")
		if err != nil {
			return result, err
		}
	}
	if !projectAbsolutePath(apk) || inspector == nil {
		return result, fmt.Errorf("必须明确选择 APK 的绝对路径，不能搜索或猜测文件")
	}
	before, _, err := privatefs.Hash(apk)
	if err != nil {
		return result, fmt.Errorf("所选 APK 不是可读取的普通文件")
	}
	info, err := inspector.Inspect(ctx, apk)
	if err != nil || !channels.ValidInspectionPackage(info.PackageName) || !manifest.ValidHash(info.SignerSHA256) || info.Debuggable || info.VersionCode <= 0 || info.VersionName == "" || !projectText(info.VersionName) {
		return result, fmt.Errorf("APK 验签或元数据检查失败，或所选 APK 是可调试包；未保存配置")
	}
	after, _, err := privatefs.Hash(apk)
	if err != nil || before != after {
		return result, fmt.Errorf("APK 在检查期间变化，请重新选择")
	}
	fmt.Fprintf(stderr, "已验签包名：%s\n样例版本：%s (%d)\n签名 SHA-256：%s\n", info.PackageName, info.VersionName, info.VersionCode, info.SignerSHA256)
	fmt.Fprintln(stderr, "确认后，该项目的后续 APK 必须匹配此包名和签名。这不代表已核实线上历史签名。")
	if err = prompt.confirm("确认信任这个包名和签名"); err != nil {
		return result, err
	}
	result = projectProfile{SchemaVersion: 1, ID: id, PackageName: info.PackageName, SignerSHA256: info.SignerSHA256}
	selected, err := prompt.ask("选择渠道，逗号分隔（oppo,vivo,xiaomi,huawei,honor,tencent）", "oppo,vivo,xiaomi,huawei,honor,tencent")
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, ch := range strings.Split(selected, ",") {
		ch = strings.TrimSpace(ch)
		if !slices.Contains(manifest.Channels, ch) || seen[ch] {
			return result, fmt.Errorf("渠道未知或重复，请使用列出的渠道标识")
		}
		seen[ch] = true
		c := projectChannel{Channel: ch}
		fmt.Fprintf(stderr, "\n%s 渠道（只填写公开信息和钥匙串条目名称）\n", ch)
		defaultAccount := ch + "-main"
		accountLabel := "本机账号别名"
		if ch == "oppo" {
			defaultAccount, accountLabel = "", "OPPO 公开 client_id（同时作为本机账号标识）"
		}
		if c.Account, err = prompt.ask(accountLabel, defaultAccount); err != nil {
			return result, err
		}
		name := map[string]string{"oppo": "client-secret", "vivo": "access-secret", "xiaomi": "api-secret", "huawei": "service-account", "honor": "client-secret", "tencent": "api-secret"}[ch]
		if c.CredentialName, err = prompt.ask("密钥所在的钥匙串条目名称（不是密钥内容）", name); err != nil {
			return result, err
		}
		switch ch {
		case "vivo", "honor", "xiaomi", "tencent":
			label := map[string]string{"vivo": "公开 access_key", "honor": "公开 client_id", "xiaomi": "小米发布 API userName", "tencent": "应用宝账号 uid（不是 AppID）"}[ch]
			if c.ClientID, err = prompt.ask(label, ""); err != nil {
				return result, err
			}
		}
		if ch == "tencent" {
			if c.AppID, err = prompt.ask("该应用的 AppID", ""); err != nil {
				return result, err
			}
		}
		if ch == "huawei" || ch == "honor" {
			if c.LanguageID, err = prompt.ask("中文语言标识（可留空，由只读预检匹配）", ""); err != nil {
				return result, err
			}
		}
		if ch == "xiaomi" {
			if c.PublicKeyName, err = prompt.ask("公钥证书的钥匙串条目名称（不是证书内容）", "api-public-key"); err != nil {
				return result, err
			}
			if c.IconPath, err = prompt.ask("小米应用图标的绝对文件路径", ""); err != nil {
				return result, err
			}
			if !projectAbsolutePath(c.IconPath) {
				return result, fmt.Errorf("图标必须是绝对路径")
			}
			if _, _, err = privatefs.Hash(c.IconPath); err != nil {
				return result, fmt.Errorf("小米图标不是可读取的普通文件")
			}
			if c.PrivacyURL, err = prompt.ask("公开 HTTPS 隐私政策网址（不含账号、查询参数或片段）", ""); err != nil {
				return result, err
			}
		}
		result.Channels = append(result.Channels, c)
	}
	if err = result.validate(); err != nil {
		return result, err
	}
	fmt.Fprintf(stderr, "\n即将%s项目 %s，包名 %s，渠道 %s。\n", map[bool]string{true: "替换", false: "保存"}[replace], id, result.PackageName, selected)
	for _, c := range result.Channels {
		fmt.Fprintf(stderr, "  %s：账号 %s；钥匙串 %s", c.Channel, c.Account, c.CredentialName)
		if c.ClientID != "" {
			fmt.Fprintf(stderr, "；公开账号标识 %s", c.ClientID)
		}
		if c.AppID != "" {
			fmt.Fprintf(stderr, "；AppID %s", c.AppID)
		}
		if c.LanguageID != "" {
			fmt.Fprintf(stderr, "；语言 %s", c.LanguageID)
		}
		fmt.Fprintln(stderr)
		if c.Channel == "xiaomi" {
			fmt.Fprintf(stderr, "    公钥引用 %s；图标 %s；隐私网址 %s\n", c.PublicKeyName, c.IconPath, c.PrivacyURL)
		}
	}
	fmt.Fprintln(stderr, "将保存上述配置，不保存样例 APK、版本或更新说明，不读取钥匙串，也不验证渠道权限。")
	if err = prompt.confirm("确认保存项目配置"); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = saveProject(root, result, previous); err != nil {
		return result, err
	}
	fmt.Fprintln(stderr, "项目已保存。请另行导入各渠道凭据；每次发布仍须明确提供更新说明和各渠道 APK。")
	return result, nil
}
