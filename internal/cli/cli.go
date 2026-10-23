package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/artifact"
	"github.com/wangyiliangnet/android-publisher-cli/internal/channels"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/simulation"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

var Version = "0.1.0-dev"
var Commit = "unknown"
var BuildDate = "unknown"

func encode(out io.Writer, v any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(v)
}
func failure(out io.Writer, mode, action string, code int, err error) int {
	return failureEffect(out, mode, action, code, err, "not_sent")
}
func failureEffect(out io.Writer, mode, action string, code int, err error, effect string) int {
	if errors.Is(err, context.Canceled) {
		code = 130
	}
	if errors.Is(err, state.ErrBusy) || errors.Is(err, state.ErrRecovery) {
		code = 3
	}
	if encode(out, map[string]any{"schema_version": 1, "mode": mode, "error": err.Error(), "effect": effect, "next_action": action}) != nil {
		return 5
	}
	return code
}
func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}
func parse(f *flag.FlagSet, args []string) error {
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	return nil
}
func defaultRoot(mode string) string {
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, "Library", "Application Support", "AndroidPublisherCLI")
	if mode == manifest.Simulation {
		root = filepath.Join(root, "Simulation")
	}
	return root
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	mode := manifest.Production
	if len(args) > 0 && args[0] == "simulate" {
		mode = manifest.Simulation
		args = args[1:]
	}
	global := flags("release", stderr)
	showVersion := global.Bool("version", false, "print version")
	root := global.String("data-dir", defaultRoot(mode), "private publisher data directory")
	sdk := global.String("sdk-root", "", "Android SDK root")
	javaHome := global.String("java-home", "", "JDK home")
	if err := global.Parse(args); err != nil {
		if err == flag.ErrHelp {
			help(stdout)
			return 0
		}
		return failure(stdout, mode, "fix_config", 2, err)
	}
	args = global.Args()
	if *showVersion {
		if len(args) != 0 {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("--version takes no command"))
		}
		fmt.Fprintln(stdout, Version)
		return 0
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		help(stdout)
		return 0
	}
	command, rest := args[0], args[1:]
	if command == "protocols" {
		if len(rest) != 0 {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("protocols takes no arguments"))
		}
		if encode(stdout, map[string]any{"schema_version": 1, "production_enabled": true, "production_channels": manifest.Channels, "readonly_preflight_channels": manifest.Channels, "production_policy": "after_approval", "submission_approval_required": true, "channels": channels.Catalog()}) != nil {
			return 5
		}
		return 0
	}
	if command == "version" || command == "--version" {
		if len(rest) == 1 && rest[0] == "--json" {
			return encodeCode(stdout, map[string]any{"schema_version": 1, "version": Version, "commit": Commit, "build_date": BuildDate, "os": runtime.GOOS, "arch": runtime.GOARCH})
		}
		if len(rest) != 0 {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("version takes no arguments"))
		}
		fmt.Fprintln(stdout, Version)
		return 0
	}
	tools := artifact.Detect(*sdk, *javaHome)
	if command == "history" {
		return runHistory(ctx, mode, *root, rest, stdout, stderr)
	}
	if command == "report" {
		return runReport(ctx, mode, *root, rest, stdout, stderr)
	}
	if command == "setup" {
		return runSetup(ctx, mode, *root, rest, tools, os.Stdin, stdout, stderr)
	}
	if command == "batch" {
		return runBatch(ctx, mode, *root, rest, tools, stdout, stderr)
	}
	if command == "init" && mode == manifest.Production {
		return runPrepare(ctx, mode, *root, append([]string{"init"}, rest...), tools, stdout, stderr)
	}
	if command == "prepare" {
		return runPrepare(ctx, mode, *root, append([]string{"prepare"}, rest...), tools, stdout, stderr)
	}
	if command == "doctor" {
		return doctor(ctx, tools, rest, stdout, stderr)
	}
	if command == "init" && mode == manifest.Simulation {
		f := flags("simulate init", stderr)
		dir := f.String("dir", "", "new empty fixture directory")
		if err := parse(f, rest); err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
		if *dir == "" {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("--dir is required"))
		}
		path, err := simulation.Init(*dir)
		if err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
		if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "manifest": path}) != nil {
			return 5
		}
		return 0
	}
	if command == "serve" && mode == manifest.Simulation {
		return serve(ctx, rest, stdout, stderr)
	}
	if command == "stats" && mode == manifest.Simulation {
		f := flags("simulate stats", stderr)
		endpoint := f.String("store", "", "loopback fake-store URL")
		if err := parse(f, rest); err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
		client, err := simulation.NewClient(*endpoint)
		if err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
		result, err := client.Stats(ctx)
		if err != nil {
			return failure(stdout, mode, "retry_read", 3, err)
		}
		if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "stats": result}) != nil {
			return 5
		}
		return 0
	}
	if command == "state" {
		return runState(mode, *root, rest, stdout, stderr)
	}
	if command == "evidence" {
		return runConsoleEvidence(mode, *root, rest, stdout, stderr)
	}
	if command == "credentials" {
		return runCredentials(ctx, mode, rest, stdout, stderr)
	}
	if command == "account" {
		return runAccount(ctx, mode, rest, tools, stdout, stderr)
	}
	if !strings.Contains("|plan|status|watch|preflight|submit|resume|reconcile|", "|"+command+"|") {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("unsupported command; run release --help"))
	}
	f := flags(command, stderr)
	input := f.String("manifest", "", "YAML manifest path")
	id := f.String("release", "", "saved release ID")
	channels := f.String("channels", "", "comma-separated release subset")
	channel := f.String("channel", "", "single channel for reconciliation")
	endpoint := f.String("store", "", "simulation loopback URL")
	refresh := f.Bool("refresh", false, "query once without submitting")
	observer := f.String("observer", "api", "read-only observation source: api or browser (simulation only)")
	node := f.String("node", "", "absolute path to pinned Node.js (browser only)")
	worker := f.String("browser-worker", "", "absolute path to built browser worker (browser only)")
	interval := f.Duration("interval", time.Minute, "watch wake interval; durable next_check_at still applies")
	maxPolls := f.Int("max-polls", 0, "watch cycles; 0 runs until interrupted")
	credentialName := f.String("credential-name", "", "OPPO Keychain credential reference; persisted by preflight")
	approvedIntent := f.String("approve-intent", "", "exact reviewed production intent digest for submit/resume")
	approvedPreflight := f.String("approve-preflight", "", "exact reviewed production preflight binding for submit/resume")
	interactiveKeychain := f.Bool("keychain-interactive", false, "foreground macOS authorization prompts for channel credentials")
	accountConfig := f.String("account-config", "", "private references and public identity for five-channel production commands")
	replaceUploadAuthorization := f.String("replace-unused-upload-authorization", "", "OPPO resume only: exact unresolved authorization operation ID")
	renewUploadAuthorization := f.String("renew-expired-upload-authorization", "", "Huawei resume only: exact expired unused authorization operation ID")
	if err := parse(f, rest); err != nil {
		return failure(stdout, mode, "fix_config", 2, err)
	}
	seen := map[string]bool{}
	f.Visit(func(v *flag.Flag) { seen[v.Name] = true })
	observing := command == "watch" || (command == "status" && *refresh)
	if (*observer != "api" && *observer != "browser") || ((!observing) && (seen["observer"] || seen["node"] || seen["browser-worker"])) || (*observer != "browser" && (seen["node"] || seen["browser-worker"])) || (command != "watch" && (seen["interval"] || seen["max-polls"])) || (command == "watch" && (*interval < 100*time.Millisecond || *interval > 24*time.Hour || *maxPolls < 0)) {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("invalid observation flags; browser requires status --refresh or watch; interval must be 100ms to 24h and max-polls nonnegative"))
	}
	if command == "plan" {
		if *input == "" || *id != "" || *channels != "" || *channel != "" || *endpoint != "" || *refresh {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("plan requires only --manifest and never connects to a store"))
		}
	} else if *id == "" || *input != "" {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("command requires --release and no --manifest"))
	}
	online := command != "plan" && (command != "status" || *refresh)
	productionFlags := seen["credential-name"] || seen["approve-intent"] || seen["approve-preflight"] || seen["keychain-interactive"] || seen["account-config"] || seen["replace-unused-upload-authorization"] || seen["renew-expired-upload-authorization"]
	if seen["replace-unused-upload-authorization"] && (command != "resume" || !manifest.ValidHash(*replaceUploadAuthorization)) {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("upload authorization replacement requires production resume and an exact operation ID"))
	}
	if seen["renew-expired-upload-authorization"] && (command != "resume" || !manifest.ValidHash(*renewUploadAuthorization) || *accountConfig == "" || seen["replace-unused-upload-authorization"] || seen["credential-name"]) {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("upload authorization renewal requires Huawei production resume, account-config and an exact operation ID; OPPO flags cannot be combined"))
	}
	if productionFlags && (mode != manifest.Production || !online) {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("production flags require an online production command"))
	}
	if *channel != "" && *channels != "" {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("choose --channel or --channels"))
	}
	if (*refresh && command != "status") || (*channel != "" && command != "reconcile") || (command == "status" && !*refresh && (*channel != "" || *channels != "")) {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("flag is not valid for this command"))
	}
	if *channel != "" {
		*channels = *channel
	}
	if mode == manifest.Production && (online || *endpoint != "") {
		// One installation-wide state root prevents a new --data-dir from
		// escaping an unresolved business operation in the canonical ledger.
		if !online || *endpoint != "" || *observer != "api" || filepath.Clean(*root) != defaultRoot(manifest.Production) {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("production requires the canonical data directory, an API observer and a supported command"))
		}
		if command == "watch" {
			if *approvedIntent != "" || *approvedPreflight != "" || (*accountConfig != "" && *credentialName != "") {
				return failure(stdout, mode, "fix_config", 2, fmt.Errorf("watch accepts only read-only account configuration"))
			}
			return productionWatch(ctx, *root, *id, *accountConfig, *credentialName, *interactiveKeychain, engine.ParseChannels(*channels), tools, *interval, *maxPolls, stdout)
		}
		if *accountConfig != "" {
			if *credentialName != "" || seen["replace-unused-upload-authorization"] {
				return failure(stdout, mode, "fix_config", 2, fmt.Errorf("account-config cannot be combined with OPPO credential or authorization replacement flags"))
			}
			return runFiveProduction(ctx, command, defaultRoot(manifest.Production), *id, *accountConfig, *approvedIntent, *approvedPreflight, *interactiveKeychain, engine.ParseChannels(*channels), tools, stdout, *renewUploadAuthorization)
		}
		return runOPPOProduction(ctx, command, defaultRoot(manifest.Production), *id, *credentialName, *approvedIntent, *approvedPreflight, *interactiveKeychain, engine.ParseChannels(*channels), tools, stdout, *replaceUploadAuthorization)
	}
	var adapter protocol.Adapter
	observation := observationConfig{Source: *observer, NodePath: *node, WorkerPath: *worker, Origin: *endpoint}
	if online {
		var err error
		if *observer == "browser" {
			err = observation.resolve(ctx)
		} else {
			adapter, err = simulation.NewClient(*endpoint)
		}
		if err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
	} else if *endpoint != "" {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("cached status does not accept a store endpoint"))
	}
	if command == "watch" {
		return watch(ctx, mode, *root, *id, engine.ParseChannels(*channels), observation, adapter, *interval, *maxPolls, stdout)
	}
	s, err := state.Open(*root, mode, command == "status" && !*refresh)
	if err != nil {
		return failure(stdout, mode, "fix_config", 5, err)
	}
	defer s.Close()
	var inspector artifact.Inspector = tools
	if mode == manifest.Simulation {
		inspector = artifact.SimulationInspector{}
	}
	e := engine.Engine{Store: s, Inspector: inspector}
	if observing && *observer == "browser" {
		adapter, err = observation.adapter(s, *id)
		if err != nil {
			return failure(stdout, mode, "fix_config", 2, err)
		}
	}
	if adapter != nil {
		e.Adapter = adapter
	}
	var result engine.Summary
	subset := engine.ParseChannels(*channels)
	switch command {
	case "plan":
		result, err = e.Plan(ctx, *input)
	case "status":
		if *refresh {
			result, err = e.Refresh(ctx, *id, subset)
		} else {
			result, err = e.Status(*id)
		}
	case "preflight":
		result, err = e.Preflight(ctx, *id, subset)
	case "submit", "resume":
		result, err = e.Submit(ctx, *id, subset)
	case "reconcile":
		result, err = e.Reconcile(ctx, *id, subset)
	}
	if result.ReleaseID != "" {
		if encode(stdout, result) != nil {
			return 5
		}
		if err != nil {
			if ctx.Err() != nil {
				return 130
			}
			if observing {
				return observationExitCode(result)
			}
			return 3
		}
		return 0
	}
	if err != nil {
		code := 3
		if errors.Is(err, state.ErrReleaseNotFound) || errors.Is(err, engine.ErrInvalidInput) {
			return failure(stdout, mode, "fix_config", 2, err)
		}
		if errors.Is(err, state.ErrBusy) {
			return failure(stdout, mode, "wait", 3, err)
		}
		if command == "submit" || command == "resume" {
			return failureEffect(stdout, mode, "reconcile", code, err, "unknown")
		}
		if command == "plan" {
			code = 2
		}
		return failure(stdout, mode, "fix_config", code, err)
	}
	return 0
}

func help(out io.Writer) {
	fmt.Fprintln(out, `Usage: release [--data-dir DIR] [--sdk-root DIR] [--java-home DIR] COMMAND

Local commands: doctor, protocols, plan --manifest FILE, status --release ID,
                state backup --to DIR, state restore --from DIR, state recovery-status
Credentials: credentials import|check|delete --channel ID --account ID --name NAME
             import uses a hidden terminal prompt, --file FILE or explicit --stdin;
             --replace is required to update an existing value; never pass secrets as flags.
Account query: account inspect --channel CHANNEL --account ACCOUNT
               [--credential-name NAME] (--package PACKAGE | --apk FILE)
               [--client-id PUBLIC_ID] [--public-key-name NAME] [--app-id ID]
               Huawei uses service-account; Xiaomi needs a public certificate reference;
               Tencent requires its application AppID in addition to the account uid.
               Keychain-backed authentication and application query only; no publishing.
               Honor only: --replace-unknown-token PENDING_SHA256 archives the old
               unknown token intent and makes one separately recorded new grant.
Simulation: release simulate [--data-dir DIR] COMMAND
            init --dir DIR; serve --store-dir DIR [--listen 127.0.0.1:0];
            plan; preflight; submit; resume; reconcile; status [--refresh]; stats
            status --refresh [--observer api|browser];
            watch --release ID [--observer api|browser] [--interval 1m] [--max-polls N]
Browser simulation requires pinned Node.js and browser/dist/worker.js;
use --node ABS and --browser-worker ABS when installed outside the default paths.
Simulation online commands require --store http://127.0.0.1:PORT.
OPPO production: preflight --release ID [--credential-name NAME] [--keychain-interactive];
                 submit/resume --release ID --approve-intent DIGEST --approve-preflight BINDING;
                 reconcile --release ID; status --release ID --refresh.
Other channels: preflight --release ID --account-config PRIVATE_JSON [--keychain-interactive];
                submit/resume --release ID --account-config PRIVATE_JSON
                --approve-intent DIGEST --approve-preflight BINDING;
                reconcile --release ID --account-config PRIVATE_JSON;
                status --release ID --refresh --account-config PRIVATE_JSON.
Huawei resume may explicitly renew an expired unused authorization with
--renew-expired-upload-authorization OPERATION_ID and both reviewed approvals.
Production requires one frozen after_approval intent in the canonical data directory.
Production watch is read-only and supports the same account configuration.
Colleague workflow: setup --project ID [--apk ABS] [--replace] (interactive, offline);
                    prepare --project ID --batch ID --notes ABS --apk channel=ABS (repeat for every channel);
                    init --file release-input.json; prepare --file release-input.json;
                    batch create --file batch.json;
                    batch preflight --batch ID; batch status --batch ID [--refresh] [--format table|json];
                    batch review --batch ID --file ABS [--channels oppo,honor];
                    batch submit|resume --batch ID --approval ABS --approve-plan REVIEWED_DIGEST;
                    batch reconcile --batch ID [--channels oppo,honor];
                    batch watch --batch ID [--interval 1m] [--max-polls N];
                    history [--package PACKAGE] [--channel CHANNEL] [--limit 20] [--format table|json];
                    report --batch ID|--release ID --file ABS [--format markdown|json].
Console evidence: evidence template --release ID --channel CHANNEL --file NEW_JSON;
                  evidence import --file REVIEWED_JSON (human attestation only).
Recovery: state recovery-template --file NEW_JSON; state recovery-review --file JSON;
          state recovery-close --file JSON --approve-review EXACT_DIGEST.`)
}

func doctor(ctx context.Context, tools artifact.AndroidTools, args []string, stdout, stderr io.Writer) int {
	f := flags("doctor", stderr)
	apk := f.String("apk", "", "optional local test APK to inspect")
	if err := parse(f, args); err != nil {
		return failure(stdout, manifest.Production, "fix_config", 2, err)
	}
	checks := map[string]bool{}
	for name, path := range map[string]string{"apksigner": tools.APKSigner, "apkanalyzer": tools.APKAnalyzer} {
		verb := "version"
		if name == "apkanalyzer" {
			verb = "--version"
		}
		_, err := tools.Command(ctx, path, verb)
		checks[name] = err == nil
	}
	java := filepath.Join(tools.JavaHome, "bin", "java")
	if tools.JavaHome == "" {
		java = "java"
	}
	_, err := tools.Command(ctx, java, "-version")
	checks["java"] = err == nil
	var info *artifact.Info
	if *apk != "" {
		parsed, err := tools.Inspect(ctx, *apk)
		checks["test_apk_verified"] = err == nil
		if err == nil {
			info = &parsed
		}
	}
	tmp, err := os.MkdirTemp("", "publisher-doctor-")
	if err != nil {
		return failure(stdout, manifest.Production, "fix_config", 5, err)
	}
	defer os.RemoveAll(tmp)
	s, err := state.Open(filepath.Join(tmp, "probe"), manifest.Simulation, false)
	var sqliteVersion, journal string
	var synchronous, foreignKeys int
	if err == nil {
		err = s.Ping(ctx)
		if err == nil {
			err = s.DB.QueryRow("SELECT sqlite_version()").Scan(&sqliteVersion)
		}
		if err == nil {
			err = s.DB.QueryRow("PRAGMA journal_mode").Scan(&journal)
		}
		if err == nil {
			err = s.DB.QueryRow("PRAGMA synchronous").Scan(&synchronous)
		}
		if err == nil {
			err = s.DB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys)
		}
		s.Close()
	}
	checks["sqlite_wal_full"] = err == nil && journal == "wal" && synchronous == 2 && foreignKeys == 1
	if encode(stdout, map[string]any{"schema_version": 1, "os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version(), "tools": tools, "checks": checks, "sqlite_version": sqliteVersion, "apk": info, "channel_accounts": "not_configured_or_verified"}) != nil {
		return 5
	}
	for _, ok := range checks {
		if !ok {
			return 2
		}
	}
	return 0
}
func runState(mode, root string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("state requires backup, restore or recovery-status"))
	}
	if args[0] == "recovery-template" || args[0] == "recovery-review" || args[0] == "recovery-close" {
		return runRecoveryReview(mode, root, args, stdout, stderr)
	}
	action := args[0]
	f := flags("state "+action, stderr)
	to := f.String("to", "", "new backup directory")
	from := f.String("from", "", "backup directory")
	if err := parse(f, args[1:]); err != nil {
		return failure(stdout, mode, "fix_config", 2, err)
	}
	if action == "restore" {
		if *from == "" || *to != "" {
			return failure(stdout, mode, "fix_config", 2, fmt.Errorf("restore requires only --from"))
		}
		if err := state.Restore(root, *from, mode); err != nil {
			return failure(stdout, mode, "fix_config", 5, err)
		}
		if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "recovery_required": true, "next_action": "reconcile_full_account_inventory"}) != nil {
			return 5
		}
		return 0
	}
	if (action != "backup" && action != "recovery-status") || *from != "" || (action == "backup" && *to == "") || (action == "recovery-status" && *to != "") {
		return failure(stdout, mode, "fix_config", 2, fmt.Errorf("invalid state command arguments"))
	}
	s, err := state.Open(root, mode, action == "recovery-status")
	if err != nil {
		return failure(stdout, mode, "fix_config", 5, err)
	}
	defer s.Close()
	if action == "backup" {
		if err = s.Backup(*to); err != nil {
			return failure(stdout, mode, "fix_config", 5, err)
		}
		if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "backup": *to}) != nil {
			return 5
		}
		return 0
	}
	required, err := s.RecoveryRequired()
	if err != nil {
		return failure(stdout, mode, "fix_config", 5, err)
	}
	if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "recovery_required": required, "automatic_unlock_supported": false}) != nil {
		return 5
	}
	return 0
}

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	f := flags("simulate serve", stderr)
	dir := f.String("store-dir", "", "independent fake-store directory")
	listen := f.String("listen", "127.0.0.1:0", "literal loopback IP and port")
	fault := f.String("fault", "", "channel:drop_submit_once or channel:invalid_push_once")
	inconclusive := f.Bool("inconclusive", false, "reconciliation remains unknown")
	review := f.String("review", "in_review", "fake review state")
	availability := f.String("availability", "not_observed_live", "fake availability state")
	if err := parse(f, args); err != nil {
		return failure(stdout, manifest.Simulation, "fix_config", 2, err)
	}
	host, _, err := net.SplitHostPort(*listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || *dir == "" {
		return failure(stdout, manifest.Simulation, "fix_config", 2, fmt.Errorf("serve requires --store-dir and a literal loopback listen address"))
	}
	options := simulation.Options{Faults: map[string]string{}, Inconclusive: *inconclusive, Review: *review, Availability: *availability}
	if *fault != "" {
		pair := strings.SplitN(*fault, ":", 2)
		if len(pair) != 2 {
			return failure(stdout, manifest.Simulation, "fix_config", 2, fmt.Errorf("invalid fault format"))
		}
		options.Faults[pair[0]] = pair[1]
	}
	s, err := simulation.New(*dir, options)
	if err != nil {
		return failure(stdout, manifest.Simulation, "fix_config", 5, err)
	}
	defer s.Close()
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return failure(stdout, manifest.Simulation, "fix_config", 5, err)
	}
	defer l.Close()
	server := http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second}
	if encode(stdout, map[string]any{"schema_version": 1, "mode": manifest.Simulation, "store": "http://" + l.Addr().String()}) != nil {
		return 5
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(l) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return failure(stdout, manifest.Simulation, "fix_config", 5, err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
		<-done
	}
	return 0
}
