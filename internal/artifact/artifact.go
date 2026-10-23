package artifact

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

type Info struct {
	PackageName  string   `json:"package_name"`
	VersionCode  int64    `json:"version_code"`
	VersionName  string   `json:"version_name"`
	SignerSHA256 string   `json:"signer_sha256"`
	ABI          []string `json:"abi"`
	Debuggable   bool     `json:"debuggable"`
}
type Inspector interface {
	Inspect(context.Context, string) (Info, error)
}

func Path(root, digest string) string { return filepath.Join(root, "artifacts", digest, "blob") }

func Freeze(ctx context.Context, root string, m manifest.Manifest, inspector Inspector) error {
	if err := privatefs.Dir(filepath.Join(root, "artifacts")); err != nil {
		return err
	}
	for _, a := range m.Artifacts {
		dir := filepath.Join(root, "artifacts", a.SHA256)
		if err := privatefs.Dir(dir); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".import-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		tmp.Close()
		os.Remove(name)
		if err := privatefs.Copy(a.Path, name); err != nil {
			return err
		}
		err = Check(ctx, name, m, a, inspector)
		if err == nil {
			err = os.Rename(name, Path(root, a.SHA256))
		}
		os.Remove(name)
		if err != nil {
			return err
		}
		if err := privatefs.SyncDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func Check(ctx context.Context, path string, m manifest.Manifest, a manifest.Artifact, inspector Inspector) error {
	digest, _, err := privatefs.Hash(path)
	if err != nil {
		return err
	}
	if digest != a.SHA256 {
		return fmt.Errorf("artifact digest mismatch")
	}
	info, err := inspector.Inspect(ctx, path)
	if err != nil {
		return err
	}
	slices.Sort(info.ABI)
	if info.PackageName != m.PackageName || info.VersionCode != m.VersionCode || info.VersionName != m.VersionName || info.SignerSHA256 != a.SignerSHA256 || !slices.Equal(info.ABI, a.ABI) || info.Debuggable {
		return fmt.Errorf("artifact package, version, certificate, ABI or debuggable policy mismatch")
	}
	after, _, err := privatefs.Hash(path)
	if err != nil {
		return err
	}
	if after != digest {
		return fmt.Errorf("artifact changed during verification")
	}
	return nil
}

func VerifyFrozen(ctx context.Context, root string, s manifest.Snapshot, inspector Inspector) error {
	if err := s.Verify(s.Manifest.Mode); err != nil {
		return err
	}
	for _, a := range s.Manifest.Artifacts {
		if err := Check(ctx, Path(root, a.SHA256), s.Manifest, a, inspector); err != nil {
			return err
		}
	}
	return nil
}

type AndroidTools struct {
	SDKRoot     string `json:"sdk_root"`
	JavaHome    string `json:"java_home"`
	APKSigner   string `json:"apksigner"`
	APKAnalyzer string `json:"apkanalyzer"`
	AAPT2       string `json:"aapt2"`
	AndroidJar  string `json:"android_jar"`
}

func Detect(sdk, javaHome string) AndroidTools {
	home, _ := os.UserHomeDir()
	if sdk == "" {
		sdk = os.Getenv("ANDROID_SDK_ROOT")
	}
	if sdk == "" {
		sdk = os.Getenv("ANDROID_HOME")
	}
	if sdk == "" {
		sdk = filepath.Join(home, "Library", "Android", "sdk")
	}
	if javaHome == "" {
		javaHome = os.Getenv("JAVA_HOME")
	}
	if javaHome == "" {
		if _, err := os.Stat("/Applications/Android Studio.app/Contents/jbr/Contents/Home/bin/java"); err == nil {
			javaHome = "/Applications/Android Studio.app/Contents/jbr/Contents/Home"
		}
	}
	t := AndroidTools{SDKRoot: sdk, JavaHome: javaHome}
	// Select the latest installed numeric package, not lexicographic "9 > 36".
	latest := func(pattern string) string {
		paths, _ := filepath.Glob(pattern)
		slices.SortFunc(paths, func(a, b string) int {
			av := regexp.MustCompile(`[0-9]+`).FindAllString(a, -1)
			bv := regexp.MustCompile(`[0-9]+`).FindAllString(b, -1)
			for i := 0; i < len(av) && i < len(bv); i++ {
				x, _ := strconv.Atoi(av[i])
				y, _ := strconv.Atoi(bv[i])
				if x != y {
					return x - y
				}
			}
			return strings.Compare(a, b)
		})
		if len(paths) == 0 {
			return ""
		}
		return paths[len(paths)-1]
	}
	t.APKSigner = latest(filepath.Join(sdk, "build-tools", "*", "apksigner"))
	t.AAPT2 = latest(filepath.Join(sdk, "build-tools", "*", "aapt2"))
	t.AndroidJar = latest(filepath.Join(sdk, "platforms", "android-*", "android.jar"))
	t.APKAnalyzer = filepath.Join(sdk, "cmdline-tools", "latest", "bin", "apkanalyzer")
	if _, err := os.Stat(t.APKAnalyzer); err != nil {
		t.APKAnalyzer = latest(filepath.Join(sdk, "cmdline-tools", "*", "bin", "apkanalyzer"))
	}
	return t
}

func (t AndroidTools) Command(ctx context.Context, path string, args ...string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("Android tool is unavailable; run doctor")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = os.Environ()
	if t.JavaHome != "" {
		cmd.Env = append(cmd.Env, "JAVA_HOME="+t.JavaHome)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed (check SDK/JDK or APK validity)", filepath.Base(path))
	}
	return out, nil
}

func (t AndroidTools) Inspect(ctx context.Context, path string) (Info, error) {
	var info Info
	out, err := t.Command(ctx, t.APKSigner, "verify", "--verbose", "--print-certs", path)
	if err != nil {
		return info, err
	}
	if info.SignerSHA256, err = parseSignerCertificate(out); err != nil {
		return info, err
	}
	query := func(verb string) (string, error) {
		b, e := t.Command(ctx, t.APKAnalyzer, "manifest", verb, path)
		return strings.TrimSpace(string(b)), e
	}
	if info.PackageName, err = query("application-id"); err != nil {
		return info, err
	}
	if info.VersionName, err = query("version-name"); err != nil {
		return info, err
	}
	code, err := query("version-code")
	if err != nil {
		return info, err
	}
	info.VersionCode, err = strconv.ParseInt(code, 10, 64)
	if err != nil {
		return info, fmt.Errorf("invalid APK version code")
	}
	debug, err := query("debuggable")
	if err != nil {
		return info, err
	}
	if debug != "true" && debug != "false" {
		return info, fmt.Errorf("unknown debuggable result")
	}
	info.Debuggable = debug == "true"
	z, err := zip.OpenReader(path)
	if err != nil {
		return info, err
	}
	defer z.Close()
	seen := map[string]bool{}
	for _, f := range z.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) == 3 && parts[0] == "lib" && strings.HasSuffix(parts[2], ".so") {
			seen[parts[1]] = true
		}
	}
	for abi := range seen {
		info.ABI = append(info.ABI, abi)
	}
	slices.Sort(info.ABI)
	return info, nil
}

// Called only after apksigner verify succeeds. Build Tools 37 labels certificates
// by signature scheme; older versions use Signer #1. Repeated scheme evidence
// may name the same certificate, but distinct certificates require another policy.
func parseSignerCertificate(out []byte) (string, error) {
	unsupported := fmt.Errorf("exactly one verified APK signer is supported; unrecognized output, rotation or multiple signers require a separate policy")
	counts := regexp.MustCompile(`(?m)^Number of signers: ([0-9]+)[ \t\r]*$`).FindAllSubmatch(out, -1)
	if len(counts) != 1 || string(counts[0][1]) != "1" {
		return "", unsupported
	}
	certificates := regexp.MustCompile(`^(?:Signer #1 |V[0-9]+(?:\.[0-9]+)? Signer: )certificate SHA-256 digest: ([a-fA-F0-9]{64})[ \t\r]*$`)
	var digest string
	for _, line := range strings.Split(string(out), "\n") {
		// Public-key digests and SourceStamp certificates are not APK signers.
		if !strings.Contains(line, "certificate SHA-256 digest:") || strings.HasPrefix(line, "Source Stamp ") {
			continue
		}
		match := certificates.FindStringSubmatch(line)
		if match == nil {
			return "", unsupported
		}
		next := strings.ToLower(match[1])
		if digest != "" && digest != next {
			return "", unsupported
		}
		digest = next
	}
	if digest == "" {
		return "", unsupported
	}
	return digest, nil
}

// Simulation artifacts are signed JSON, intentionally not APKs. Production never uses this inspector.
type SimulationInspector struct{}
type envelope struct {
	Info      Info   `json:"info"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

func CreateSimulation(path string, info Info) (manifest.Artifact, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return manifest.Artifact{}, err
	}
	payload, _ := json.Marshal(info)
	cert := sha256.Sum256(pub)
	env := envelope{info, base64.StdEncoding.EncodeToString(pub), base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))}
	data, _ := json.Marshal(env)
	if err := privatefs.Write(path, data); err != nil {
		return manifest.Artifact{}, err
	}
	h := sha256.Sum256(data)
	return manifest.Artifact{Path: path, SHA256: hex.EncodeToString(h[:]), SignerSHA256: hex.EncodeToString(cert[:]), ABI: info.ABI}, nil
}
func (SimulationInspector) Inspect(_ context.Context, path string) (Info, error) {
	data, err := privatefs.Read(path, 1<<20)
	if err != nil {
		return Info{}, err
	}
	if len(data) > 1<<20 {
		return Info{}, fmt.Errorf("simulation artifact too large")
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Info{}, fmt.Errorf("invalid simulation artifact")
	}
	pub, e1 := base64.StdEncoding.DecodeString(env.PublicKey)
	sig, e2 := base64.StdEncoding.DecodeString(env.Signature)
	payload, _ := json.Marshal(env.Info)
	if e1 != nil || e2 != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, payload, sig) {
		return Info{}, fmt.Errorf("simulation signature verification failed")
	}
	h := sha256.Sum256(pub)
	env.Info.SignerSHA256 = hex.EncodeToString(h[:])
	return env.Info, nil
}
