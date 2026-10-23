package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangyiliangnet/android-publisher-cli/internal/browser"
	"github.com/wangyiliangnet/android-publisher-cli/internal/engine"
	"github.com/wangyiliangnet/android-publisher-cli/internal/manifest"
	"github.com/wangyiliangnet/android-publisher-cli/internal/protocol"
	"github.com/wangyiliangnet/android-publisher-cli/internal/state"
)

const browserNodeVersion = "v24.22.0"

type observationConfig struct {
	Source, NodePath, WorkerPath, Origin string
}

// resolve checks local configuration before a state directory is opened. A
// browser observer can only reach the separately started loopback fake store.
func (c *observationConfig) resolve(ctx context.Context) error {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("browser simulation requires a literal loopback HTTP store origin")
	}
	host, port, err := net.SplitHostPort(u.Host)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("browser simulation requires a literal loopback HTTP store origin")
	}
	if c.NodePath == "" {
		c.NodePath = os.Getenv("PUBLISHER_NODE")
		if c.NodePath == "" {
			c.NodePath, err = exec.LookPath("node")
			if err != nil {
				return fmt.Errorf("browser requires Node.js %s; set --node or PUBLISHER_NODE", browserNodeVersion)
			}
		}
	}
	if !filepath.IsAbs(c.NodePath) || filepath.Clean(c.NodePath) != c.NodePath {
		return fmt.Errorf("--node must be an absolute path to Node.js %s", browserNodeVersion)
	}
	if c.WorkerPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("browser requires --browser-worker with an absolute path")
		}
		c.WorkerPath = filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "browser", "dist", "worker.js"))
	}
	if !filepath.IsAbs(c.WorkerPath) || filepath.Clean(c.WorkerPath) != c.WorkerPath {
		return fmt.Errorf("--browser-worker must be an absolute path")
	}
	info, err := os.Lstat(c.WorkerPath)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("browser worker is unavailable; build browser/dist/worker.js or set --browser-worker")
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(probe, c.NodePath, "--version")
	// In particular, NODE_OPTIONS must not preload arbitrary user code during
	// this configuration probe or during the separately sandboxed worker run.
	command.Env = []string{"PATH=" + filepath.Dir(c.NodePath) + ":/usr/bin:/bin", "LANG=C.UTF-8", "NO_COLOR=1"}
	var version versionOutput
	command.Stdout = &version
	command.Stderr = io.Discard
	command.WaitDelay = time.Second
	if command.Run() != nil || version.overflow || strings.TrimSpace(version.data) != browserNodeVersion {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("browser requires Node.js %s; set --node or PUBLISHER_NODE", browserNodeVersion)
	}
	return nil
}

// versionOutput drains a misconfigured executable without retaining its output.
type versionOutput struct {
	data     string
	overflow bool
}

func (v *versionOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(v.data)+n > 64 {
		v.overflow = true
		return n, nil
	}
	v.data += string(p)
	return n, nil
}

func (c observationConfig) adapter(s *state.Store, id string) (protocol.Adapter, error) {
	if s.Mode != manifest.Simulation {
		return nil, fmt.Errorf("production browser observation awaits account validation")
	}
	snapshot, err := s.Load(id)
	if err != nil {
		return nil, err
	}
	targets, err := s.Targets(id)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]browser.Target, len(targets))
	for _, t := range targets {
		if t.Phase != "submitted" {
			continue
		}
		identity, ok := snapshot.Manifest.Targets[t.Channel]
		if !ok || state.Scope(t.Channel, identity, snapshot.Manifest.PackageName) != t.Scope {
			return nil, fmt.Errorf("%w: browser target differs from frozen release", engine.ErrInvalidInput)
		}
		var frozen protocol.RemoteSnapshot
		if t.Preflight == "" || json.Unmarshal([]byte(t.Preflight), &frozen) != nil || frozen.Scope != t.Scope {
			return nil, fmt.Errorf("%w: browser observation requires the frozen preflight scope", engine.ErrInvalidInput)
		}
		kind := "submit"
		if t.Channel == "xiaomi" {
			kind = "push"
		}
		op, err := s.Operation(t.ID, kind)
		if err != nil {
			return nil, err
		}
		if op == nil || op.Effect != "confirmed_applied" || op.RemoteRef == "" || op.RemoteRef != t.RemoteRef || op.Request.TargetID != t.ID || op.Request.Scope != t.Scope || op.Request.Channel != t.Channel || op.Request.Kind != kind || op.Request.IntentDigest != snapshot.Digest || op.Request.PackageName != snapshot.Manifest.PackageName || op.Request.VersionCode != snapshot.Manifest.VersionCode || op.Request.AdapterBinding != frozen.AdapterBinding {
			return nil, fmt.Errorf("%w: browser target lacks a matching final submission receipt", engine.ErrInvalidInput)
		}
		expected[t.Scope] = browser.Target{Channel: t.Channel, Account: identity.AccountRef, AppID: identity.AppRef, Scope: t.Scope, PackageName: snapshot.Manifest.PackageName, VersionCode: snapshot.Manifest.VersionCode, RemoteRef: op.RemoteRef, AdapterBinding: frozen.AdapterBinding, IntentDigest: snapshot.Digest}
	}
	runner, err := browser.New(browser.Config{Root: s.Root, NodePath: c.NodePath, WorkerPath: c.WorkerPath, Origin: c.Origin, Simulation: true})
	if err != nil {
		return nil, err
	}
	return &browser.Adapter{Runner: runner, Targets: expected}, nil
}

type watchEvent struct {
	engine.Summary
	Sequence   int    `json:"sequence"`
	Phase      string `json:"phase"`
	Effect     string `json:"effect"`
	NextAction string `json:"next_action"`
}

// watch reopens state for each bounded refresh and releases its OS locks before
// waiting. Waking sooner never overrides the per-target durable next_check_at.
func watch(ctx context.Context, mode, root, id string, channels []string, config observationConfig, api protocol.Adapter, interval time.Duration, maxPolls int, out io.Writer) int {
	for cycle := 1; ; cycle++ {
		if ctx.Err() != nil {
			return 130
		}
		result, err := observationPoll(ctx, mode, root, id, channels, config, api)
		if result.ReleaseID == "" {
			code := 5
			if errors.Is(err, state.ErrReleaseNotFound) || errors.Is(err, engine.ErrInvalidInput) {
				code = 2
			}
			// A concurrent publisher may own the store for a single cycle. Leave
			// its work untouched and allow a later read to recover naturally.
			if !errors.Is(err, state.ErrBusy) {
				return failure(out, mode, "fix_config", code, err)
			}
			if encode(out, map[string]any{"schema_version": 1, "mode": mode, "sequence": cycle, "phase": "observation", "effect": "read_only", "error": state.ErrBusy.Error(), "next_action": "wait"}) != nil {
				return 5
			}
		} else {
			action := "wait_next_check"
			if err != nil {
				action = "retry_read_after_next_check"
				if observationExitCode(result) == 4 {
					action = "login_required"
				}
			}
			if encode(out, watchEvent{Summary: result, Sequence: cycle, Phase: "observation", Effect: "read_only", NextAction: action}) != nil {
				return 5
			}
		}
		if ctx.Err() != nil {
			return 130
		}
		if maxPolls != 0 && cycle >= maxPolls {
			if err != nil {
				return observationExitCode(result)
			}
			return 0
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 130
		case <-timer.C:
		}
	}
}

func observationExitCode(summary engine.Summary) int {
	for _, status := range summary.ObservationStatuses {
		if status.Status == "auth_required" {
			return 4
		}
	}
	return 3
}

func observationPoll(ctx context.Context, mode, root, id string, channels []string, config observationConfig, api protocol.Adapter) (result engine.Summary, err error) {
	s, err := state.Open(root, mode, false)
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil {
			err = closeErr
		}
	}()
	if config.Source == "browser" {
		api, err = config.adapter(s, id)
		if err != nil {
			return result, err
		}
	}
	e := engine.Engine{Store: s, Adapter: api}
	return e.Refresh(ctx, id, channels)
}
