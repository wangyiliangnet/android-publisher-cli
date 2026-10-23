package channels_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
	"github.com/wangyiliangnet/android-publisher-cli/internal/transport"
	"go.yaml.in/yaml/v3"
)

const fixtureAppID = "9007199254740993"
const fixtureObjectID = "9007199254740995"
const fixtureReleaseID = "synthetic-release-exact-1"
const fixtureBearer = "synthetic-fixture-only-bearer"
const fixturePackage = "com.example.protocolfixture"

func checkFixture(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// fixtureMaterials contains only generated test grants in t.TempDir. Runtime
// never uses this plaintext store; production material remains in Keychain.
type fixtureMaterials struct{ root string }

type fixtureExecutor struct {
	wire      *fixtureWire
	materials fixtureMaterials
}

func (f fixtureExecutor) WithClient(ctx context.Context, target channels.Target, use func(*channels.Client, channels.Materials) error) error {
	if target.Account.Channel != "honor" {
		return errors.New("target escaped synthetic Honor account")
	}
	token, err := credentials.NewSecret([]byte(fixtureBearer))
	if err != nil {
		return err
	}
	defer token.Destroy()
	return use(&channels.Client{Channel: "honor", Wire: f.wire, Auth: channels.Auth{Token: token}}, f.materials)
}

func (m fixtureMaterials) path(key string) string { return filepath.Join(m.root, account.Key(key)) }
func (m fixtureMaterials) Get(_ context.Context, key string) (*credentials.Secret, error) {
	b, err := privatefs.Read(m.path(key), 16384)
	if os.IsNotExist(err) {
		return nil, credentials.ErrMissing
	}
	if err != nil {
		return nil, err
	}
	defer clear(b)
	return credentials.NewSecret(b)
}
func (m fixtureMaterials) Put(_ context.Context, key string, value *credentials.Secret) error {
	if err := privatefs.Dir(m.root); err != nil {
		return err
	}
	b := value.Bytes()
	defer clear(b)
	f, err := os.OpenFile(m.path(key), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return credentials.ErrExists
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(b)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr, privatefs.SyncDir(m.root))
}

// fixtureWire only maps the one statically declared Honor origin to our TLS
// listener. It cannot dial vendor hosts, follow redirects or retry any request.
type fixtureWire struct {
	endpoint *url.URL
	client   *http.Client
	store    *state.Store
	release  string
}

func newFixtureWire(endpoint string, certificate []byte, store *state.Store, release string) (*fixtureWire, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" {
		return nil, errors.New("invalid synthetic endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("synthetic endpoint is not loopback")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certificate) {
		return nil, errors.New("invalid synthetic certificate")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true, ForceAttemptHTTP2: false, Proxy: nil}
	return &fixtureWire{u, &http.Client{Transport: tr, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, store, release}, nil
}
func (f *fixtureWire) Send(ctx context.Context, q transport.Request) ([]byte, error) {
	if q.Body != nil {
		defer q.Body.Close()
	}
	u, err := url.Parse(q.URL)
	if err != nil || u.Scheme != "https" || u.Host != "appmarket-openapi-drcn.cloud.honor.com" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("request escaped synthetic protocol fixture")
	}
	kind := map[string]string{"get-file-upload-url": "authorize", "file-upload": "upload", "update-file-info": "bind", "update-language-info": "metadata", "submit-audit": "submit"}[filepath.Base(u.Path)]
	if kind != "" {
		op, err := f.store.Operation(f.release+"/honor", kind)
		if err != nil || op == nil || op.Effect != "intent" {
			return nil, errors.New("HTTP request preceded durable operation intent")
		}
		if kind != "authorize" && (q.Class != transport.Write || q.OperationID != op.Request.OperationID) {
			return nil, errors.New("business request lacks exact operation identity")
		}
	}
	u.Scheme = f.endpoint.Scheme
	u.Host = f.endpoint.Host
	r, err := http.NewRequestWithContext(ctx, q.Method, u.String(), q.Body)
	if err != nil {
		return nil, err
	}
	r.Header = q.Headers.Clone()
	r.ContentLength = q.ContentLength
	r.GetBody = nil
	response, err := f.client.Do(r)
	if err != nil {
		return nil, &channels.Error{Code: "synthetic_connection_lost", Effect: "unknown"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &channels.Error{Code: "synthetic_http_failure", Effect: "unknown"}
	}
	return io.ReadAll(io.LimitReader(response.Body, 1<<20))
}

type honorFixture struct {
	mu              sync.Mutex
	counts, applied map[string]int
	metadata        map[string]string
	fileSHA         string
	fileBytes       []byte
	dropSubmit      bool
	server          *httptest.Server
}

func initialMetadata() map[string]string {
	return map[string]string{"live_version_code": "1", "lang_id": "zh-CN", "lang_app_name": "合成应用", "lang_intro": "existing introduction", "lang_brief": "existing brief", "lang_notes": "old notes"}
}
func newHonorFixture(t *testing.T, drop bool) *honorFixture {
	t.Helper()
	f := &honorFixture{counts: map[string]int{}, applied: map[string]int{}, metadata: initialMetadata(), dropSubmit: drop}
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}
func (f *honorFixture) snapshot() (map[string]int, map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, a := map[string]int{}, map[string]int{}
	for k, v := range f.counts {
		c[k] = v
	}
	for k, v := range f.applied {
		a[k] = v
	}
	return c, a
}
func (f *honorFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := filepath.Base(r.URL.Path)
	f.counts[name]++ // Count every request, including rejected duplicates.
	write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	reject := func() { write(map[string]any{"code": 400, "msg": "synthetic protocol rejection"}) }
	if r.Header.Get("Authorization") != "Bearer "+fixtureBearer || !strings.HasPrefix(r.URL.Path, "/openapi/v1/publish/") {
		reject()
		return
	}
	if name == "get-app-id" {
		if r.Method != "GET" || r.URL.Query().Get("pkgName") != fixturePackage {
			reject()
			return
		}
		write(map[string]any{"code": 0, "data": []any{map[string]any{"packageName": fixturePackage, "appId": json.Number(fixtureAppID)}}})
		return
	}
	if r.URL.Query().Get("appId") != fixtureAppID && name != "get-audit-result" {
		reject()
		return
	}
	if name == "get-app-detail" {
		if r.Method != "GET" {
			reject()
			return
		}
		m := f.metadata
		write(map[string]any{"code": 0, "data": map[string]any{"basicInfo": map[string]any{"appId": json.Number(fixtureAppID), "packageName": fixturePackage}, "releaseInfo": map[string]any{"versionCode": 1}, "languageInfo": []any{map[string]any{"languageId": m["lang_id"], "appName": m["lang_app_name"], "intro": m["lang_intro"], "briefIntro": m["lang_brief"], "newFeature": m["lang_notes"]}}}})
		return
	}
	if r.Method != "POST" {
		reject()
		return
	}
	if name == "file-upload" {
		if f.applied[name] > 0 || r.URL.Query().Get("objectId") != fixtureObjectID || r.ContentLength <= 0 || r.ParseMultipartForm(1<<20) != nil {
			reject()
			return
		}
		defer r.MultipartForm.RemoveAll()
		file, _, err := r.FormFile("file")
		if err != nil {
			reject()
			return
		}
		b, err := io.ReadAll(file)
		file.Close()
		if err != nil || !reflect.DeepEqual(b, f.fileBytes) {
			reject()
			return
		}
		f.applied[name]++
		write(map[string]any{"code": 0})
		return
	}
	var body json.RawMessage
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || json.Unmarshal(b, &body) != nil {
		reject()
		return
	}
	switch name {
	case "get-file-upload-url":
		var rows []struct {
			FileName   string `json:"fileName"`
			FileType   int    `json:"fileType"`
			FileSize   int64  `json:"fileSize"`
			FileSHA256 string `json:"fileSha256"`
		}
		if json.Unmarshal(body, &rows) != nil || len(rows) != 1 || rows[0].FileType != 100 || rows[0].FileSHA256 != f.fileSHA || rows[0].FileSize != int64(len(f.fileBytes)) || f.applied[name] > 0 {
			reject()
			return
		}
		f.applied[name]++
		write(map[string]any{"code": 0, "data": []any{map[string]any{"fileName": rows[0].FileName, "objectId": json.Number(fixtureObjectID), "expireTime": time.Now().Add(time.Hour).Unix(), "uploadUrl": "https://appmarket-openapi-drcn.cloud.honor.com/openapi/v1/publish/file-upload?appId=" + fixtureAppID + "&objectId=" + fixtureObjectID}}})
	case "update-file-info":
		var got struct {
			Files []struct {
				ObjectID json.Number `json:"objectId"`
			} `json:"bindingFileList"`
		}
		if json.Unmarshal(body, &got) != nil || len(got.Files) != 1 || got.Files[0].ObjectID.String() != fixtureObjectID || f.applied["file-upload"] != 1 || f.applied[name] > 0 {
			reject()
			return
		}
		f.applied[name]++
		write(map[string]any{"code": 0})
	case "update-language-info":
		var got struct {
			SetAll int                 `json:"setAll"`
			Rows   []map[string]string `json:"languageInfoList"`
		}
		if json.Unmarshal(body, &got) != nil || got.SetAll != 0 || len(got.Rows) != 1 || f.applied["update-file-info"] != 1 || f.applied[name] > 0 {
			reject()
			return
		}
		row := got.Rows[0]
		m := f.metadata
		if row["languageId"] != m["lang_id"] || row["appName"] != m["lang_app_name"] || row["intro"] != m["lang_intro"] || row["briefIntro"] != m["lang_brief"] || row["newFeature"] == "" {
			reject()
			return
		}
		m["lang_notes"] = row["newFeature"]
		f.applied[name]++
		write(map[string]any{"code": 0})
	case "submit-audit":
		var got map[string]json.RawMessage
		var releaseType int
		if json.Unmarshal(body, &got) != nil || json.Unmarshal(got["releaseType"], &releaseType) != nil || releaseType != 1 || len(got) != 1 || f.applied["update-language-info"] != 1 || f.applied[name] > 0 {
			reject()
			return
		}
		f.applied[name]++
		if f.dropSubmit {
			f.dropSubmit = false
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				connection.Close()
			}
			return
		}
		write(map[string]any{"code": 0, "data": fixtureReleaseID})
	case "get-audit-result":
		var got struct {
			Apps []struct {
				AppID     json.Number `json:"appId"`
				ReleaseID string      `json:"releaseId"`
			} `json:"appId"`
		}
		if json.Unmarshal(body, &got) != nil || len(got.Apps) != 1 || got.Apps[0].AppID.String() != fixtureAppID || got.Apps[0].ReleaseID != fixtureReleaseID {
			reject()
			return
		}
		write(map[string]any{"code": 0, "data": []any{map[string]any{"appId": json.Number(fixtureAppID), "releaseId": fixtureReleaseID, "auditResult": 1}}})
	default:
		reject()
	}
}

type recoveryFixture struct {
	engine                                 *engine.Engine
	remote                                 *honorFixture
	base, root, path, release, certificate string
}

func newRecoveryFixture(t *testing.T, drop bool) *recoveryFixture {
	t.Helper()
	base := t.TempDir()
	checkFixture(t, privatefs.Dir(filepath.Join(base, "fixture")))
	file, err := artifact.CreateSimulation(filepath.Join(base, "fixture", "honor.sim"), artifact.Info{PackageName: fixturePackage, VersionCode: 2, VersionName: "2.0", ABI: []string{"arm64-v8a"}})
	checkFixture(t, err)
	checkFixture(t, privatefs.Write(filepath.Join(base, "fixture", "notes.txt"), []byte("合成发布说明 & +\nsecond line")))
	m := manifest.Manifest{SchemaVersion: 1, ReleaseID: "honor-wire-release", Mode: manifest.Simulation, PackageName: fixturePackage, VersionCode: 2, VersionName: "2.0", Artifacts: map[string]manifest.Artifact{"honor": file}, NotesFile: "notes.txt", ReleasePolicy: manifest.Policy{Mode: "after_approval"}, Targets: map[string]manifest.Target{"honor": {AccountRef: "synthetic-account", AppRef: fixtureAppID, Artifact: "honor"}}}
	b, err := yaml.Marshal(m)
	checkFixture(t, err)
	path := filepath.Join(base, "fixture", "release.yaml")
	checkFixture(t, privatefs.Write(path, b))
	s, err := state.Open(filepath.Join(base, "client"), manifest.Simulation, false)
	checkFixture(t, err)
	e := &engine.Engine{Store: s, Inspector: artifact.SimulationInspector{}}
	plan, err := e.Plan(context.Background(), path)
	checkFixture(t, err)
	remote := newHonorFixture(t, drop)
	remote.fileBytes, err = os.ReadFile(file.Path)
	checkFixture(t, err)
	sum := sha256.Sum256(remote.fileBytes)
	remote.fileSHA = hex.EncodeToString(sum[:])
	certificate := filepath.Join(base, "fixture-cert.pem")
	checkFixture(t, privatefs.Write(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: remote.server.Certificate().Raw})))
	v := &recoveryFixture{e, remote, base, s.Root, path, plan.ReleaseID, certificate}
	v.installAdapter(t)
	t.Cleanup(func() { _ = v.engine.Store.Close() })
	_, err = e.Preflight(context.Background(), v.release, nil)
	checkFixture(t, err)
	return v
}
func frozenHonorAdapter(s *state.Store, release, endpoint, certificate, materialRoot string) (*channels.Adapter, error) {
	snapshot, err := s.Load(release)
	if err != nil {
		return nil, err
	}
	rows, err := s.Targets(release)
	if err != nil {
		return nil, err
	}
	t := snapshot.Manifest.Targets["honor"]
	a := snapshot.Manifest.Artifacts[t.Artifact]
	file, err := channels.NewNamedFile(artifact.Path(s.Root, a.SHA256), "release.apk")
	if err != nil {
		return nil, err
	}
	cert, err := os.ReadFile(certificate)
	if err != nil {
		return nil, err
	}
	wire, err := newFixtureWire(endpoint, cert, s, release)
	if err != nil {
		return nil, err
	}
	runtime := fixtureExecutor{wire: wire, materials: fixtureMaterials{root: materialRoot}}
	return channels.NewAdapter([]channels.Target{{Scope: rows[0].Scope, IntentDigest: snapshot.Digest, Account: account.Scope{Channel: "honor", Account: t.AccountRef}, AppID: fixtureAppID, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, VersionName: snapshot.Manifest.VersionName, APK: file, ABI: a.ABI, Notes: snapshot.Notes, Policy: snapshot.Manifest.ReleasePolicy, Metadata: initialMetadata(), Patch: snapshot.Manifest.Metadata, LanguageID: "zh-CN"}}, runtime)
}
func (v *recoveryFixture) installAdapter(t *testing.T) {
	t.Helper()
	a, err := frozenHonorAdapter(v.engine.Store, v.release, v.remote.server.URL, v.certificate, filepath.Join(v.base, "synthetic-materials"))
	checkFixture(t, err)
	v.engine.Adapter = a
}
func (v *recoveryFixture) reopen(t *testing.T) {
	t.Helper()
	checkFixture(t, v.engine.Store.Close())
	s, err := state.Open(v.root, manifest.Simulation, false)
	checkFixture(t, err)
	v.engine.Store = s
	v.installAdapter(t)
}
