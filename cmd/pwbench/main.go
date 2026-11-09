// Command pwbench measures fixed workloads in an isolated pitwall installation.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/quanticstudios/pitwall/internal/drawbench"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "pwbench:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "fixture" {
		return fixture(args[1:])
	}
	if len(args) > 0 && args[0] == "compare" {
		if len(args) != 3 {
			return errors.New("usage: pwbench compare a.json b.json")
		}
		var a, b drawbench.Result
		if err := readJSON(args[1], &a); err != nil {
			return err
		}
		if err := readJSON(args[2], &b); err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		if err := drawbench.Compare(w, a, b); err != nil {
			return err
		}
		return w.Flush()
	}
	f := flag.NewFlagSet("pwbench", flag.ContinueOnError)
	workload := f.String("workload", "spinners", "spinners, titles, flood or scroll")
	seconds := f.Float64("seconds", 10, "seconds measured from the first window frame")
	binary := f.String("pitwall", "./pitwall", "pitwall build to measure")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !validWorkload(*workload) || math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds <= 0 || *seconds > 3600 {
		return errors.New("choose a known workload and seconds in (0, 3600]")
	}
	if os.Getenv("DISPLAY") == "" || os.Getenv("WAYLAND_DISPLAY") != "" {
		return errors.New("run inside agent-ws: agent-ws run draw-bench --wait -- ./pwbench ...")
	}
	path, err := filepath.Abs(*binary)
	if err != nil {
		return err
	}
	return benchmark(path, *workload, time.Duration(*seconds*float64(time.Second)), out)
}

func validWorkload(s string) bool {
	return s == "spinners" || s == "titles" || s == "flood" || s == "scroll"
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

type child struct {
	cmd  *exec.Cmd
	done chan error
}

func start(binary string, args []string, dir string, env []string, log io.Writer) (*child, error) {
	c := &child{cmd: exec.Command(binary, args...), done: make(chan error, 1)}
	c.cmd.Env, c.cmd.Dir = env, dir
	c.cmd.Stdout, c.cmd.Stderr = log, log
	if err := c.cmd.Start(); err != nil {
		return nil, err
	}
	go func() { c.done <- c.cmd.Wait(); close(c.done) }()
	return c, nil
}

func (c *child) stop() {
	if c == nil {
		return
	}
	select {
	case <-c.done:
		return
	default:
	}
	_ = c.cmd.Process.Signal(os.Interrupt)
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}

func benchmark(binary, workload string, duration time.Duration, out io.Writer) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	results, err := os.MkdirTemp("", "pwbench-results-")
	if err != nil {
		return err
	}
	fmt.Fprintln(out, results)
	private, err := os.MkdirTemp("", "pwbench-private-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	// HOME isolates daemon startup's skill refresh as well as agent discovery.
	env := isolatedEnv(private)
	for _, p := range []string{"home", "config/pitwall", "state/pitwall", "runtime/pitwall"} {
		if err := os.MkdirAll(filepath.Join(private, p), 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(private, "config/pitwall/config.toml"), []byte("[updates]\ncheck = false\nscreen_patterns = false\n"), 0o600); err != nil {
		return err
	}
	// An existing state directory suppresses the first-run welcome card.
	if err := os.WriteFile(filepath.Join(private, "state/pitwall/gui.json"), []byte("{}\n"), 0o600); err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(results, "process.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	version := exec.Command(binary, "--version")
	version.Env, version.Dir = env, private
	build, err := version.Output()
	if err != nil {
		return err
	}
	d, err := start(binary, []string{"daemon"}, private, env, log)
	if err != nil {
		return err
	}
	defer d.stop()
	socket := filepath.Join(private, "runtime/pitwall/pitwall.sock")
	var nc net.Conn
	if err := waitFor(ctx, 15*time.Second, func() (bool, error) {
		var err error
		nc, err = net.DialTimeout("unix", socket, 100*time.Millisecond)
		return err == nil, nil
	}); err != nil {
		return fmt.Errorf("daemon startup: %w", err)
	}
	defer nc.Close()
	c := proto.NewConn(nc)
	if err := c.Send(proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "cli"}); err != nil {
		return err
	}
	request := func(msg any) (model.State, error) {
		if err := nc.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			return model.State{}, err
		}
		if err := c.Send(msg); err != nil {
			return model.State{}, err
		}
		if err := c.Send(proto.Sync{}); err != nil {
			return model.State{}, err
		}
		for {
			m, err := c.Recv()
			if err != nil {
				return model.State{}, err
			}
			switch m := m.(type) {
			case proto.Error:
				return model.State{}, errors.New(m.Message)
			case proto.StateMsg:
				return m.State, nil
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	panes := 1
	if workload == "titles" {
		panes = 8
	}
	var state model.State
	for i := 0; i < panes; i++ {
		state, err = request(proto.NewSession{Cwd: private, Cmd: []string{exe, "fixture", workload, fmt.Sprintf("Implement parser %d", i+1)}})
		if err != nil {
			return err
		}
	}
	if len(state.Panes) != panes {
		return fmt.Errorf("got %d panes, want %d", len(state.Panes), panes)
	}
	if workload == "titles" {
		for i, p := range state.Panes {
			provider := []model.Provider{model.ProviderClaude, model.ProviderCodex}[i%2]
			payload, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "prompt": fmt.Sprintf("Implement parser %d", i+1)})
			if _, err := request(proto.AgentEvent{Pane: p.ID, Provider: provider, Payload: payload}); err != nil {
				return err
			}
		}
	}
	metrics := filepath.Join(results, "frames.json")
	guiEnv := append(append([]string{}, env...), "PITWALL_DRAW_BENCH="+metrics, "PITWALL_DRAW_BENCH_DURATION="+duration.String(), "vblank_mode=0", "__GL_SYNC_TO_VBLANK=0")
	g, err := start(binary, nil, private, guiEnv, log)
	if err != nil {
		return err
	}
	defer g.stop()
	if err := waitFor(ctx, 20*time.Second, func() (bool, error) {
		select {
		case err := <-g.done:
			return false, fmt.Errorf("GUI exited before its first frame: %v", err)
		default:
		}
		_, err := os.Stat(metrics + ".ready")
		return err == nil, nil
	}); err != nil {
		return err
	}
	ready, err := os.ReadFile(metrics + ".ready")
	if err != nil {
		return err
	}
	began, err := time.Parse(time.RFC3339Nano, string(ready))
	if err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(began.Add(duration)))
	defer timer.Stop()
	tick := time.NewTicker(time.Second / 60)
	defer tick.Stop()
	steps := 0
measure:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-g.done:
			return fmt.Errorf("GUI exited during workload: %v", err)
		case <-timer.C:
			break measure
		case <-tick.C:
			if workload == "scroll" {
				lines := 3
				if steps%240 >= 120 {
					lines = -3
				}
				if _, err := request(proto.Scroll{Pane: state.Panes[0].ID, Lines: lines}); err != nil {
					return err
				}
				steps++
			}
		}
	}
	for _, ws := range state.Workspaces {
		if _, err := request(proto.KillSession{WorkspaceID: ws.ID}); err != nil {
			return err
		}
	}
	select {
	case err := <-g.done:
		if err != nil {
			return err
		}
	case <-time.After(10 * time.Second):
		return errors.New("GUI did not close after ending its session")
	}
	d.stop()
	r := drawbench.Result{Workload: workload, Seconds: duration.Seconds(), Build: strings.TrimSpace(string(build)), VSync: "off"}
	if err := readJSON(metrics, &r.Frames); err != nil {
		return err
	}
	if r.Frames.Drawn == 0 {
		return errors.New("GUI recorded no frames")
	}
	r.GUISeconds = (g.cmd.ProcessState.UserTime() + g.cmd.ProcessState.SystemTime()).Seconds()
	r.DaemonSeconds = (d.cmd.ProcessState.UserTime() + d.cmd.ProcessState.SystemTime()).Seconds()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(results, "result.json"), append(data, '\n'), 0o600)
}

func isolatedEnv(dir string) []string {
	var env []string
	for _, s := range os.Environ() {
		key, _, _ := strings.Cut(s, "=")
		if key == "vblank_mode" || key == "__GL_SYNC_TO_VBLANK" {
			continue
		}
		if key == "HOME" || strings.HasPrefix(key, "XDG_") && key != "XDG_SESSION_TYPE" || strings.HasPrefix(key, "PITWALL_") || strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "CODEX_") {
			continue
		}
		env = append(env, s)
	}
	return append(env, "HOME="+filepath.Join(dir, "home"), "XDG_CONFIG_HOME="+filepath.Join(dir, "config"), "XDG_STATE_HOME="+filepath.Join(dir, "state"), "XDG_RUNTIME_DIR="+filepath.Join(dir, "runtime"), "XDG_CACHE_HOME="+filepath.Join(dir, "cache"), "XDG_DATA_HOME="+filepath.Join(dir, "data"))
}

func waitFor(ctx context.Context, timeout time.Duration, ready func() (bool, error)) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		ok, err := ready()
		if err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("startup timed out")
		case <-tick.C:
		}
	}
}
