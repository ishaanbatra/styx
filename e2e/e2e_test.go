//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rpc is one JSON-RPC exchange helper over the server's stdio.
type mcpClient struct {
	t      *testing.T
	stdin  io.WriteCloser
	out    *bufio.Scanner
	nextID int
}

func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write %s: %v", method, err)
	}
	deadline := time.After(60 * time.Second)
	for {
		lineCh := make(chan string, 1)
		go func() {
			if c.out.Scan() {
				lineCh <- c.out.Text()
			} else {
				close(lineCh)
			}
		}()
		select {
		case <-deadline:
			c.t.Fatalf("timeout waiting for response to %s", method)
		case line, ok := <-lineCh:
			if !ok {
				c.t.Fatalf("server closed stdout waiting for %s", method)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if id, _ := m["id"].(float64); int(id) == c.nextID {
				return m
			}
			// notifications (progress) fall through the loop
		}
	}
}

func (c *mcpClient) toolCall(name string, args any) (map[string]any, bool) {
	c.t.Helper()
	resp := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		c.t.Fatalf("tools/call %s: no result in %v", name, resp)
	}
	isErr, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		c.t.Fatalf("tools/call %s: empty content", name)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		payload = map[string]any{"_raw": text}
	}
	return payload, isErr
}

// startServer builds the isolated environment and spawns `styx mcp`.
func startServer(t *testing.T) (*mcpClient, string) {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(repoRoot, "bin", "styx")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("run `make build` first (or `make e2e`): %v", err)
	}

	home := t.TempDir()
	// Fake CLIs: fakeagent as both `claude` and `codex` on PATH.
	fakeBinDir := filepath.Join(home, "fakebin")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeagent, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "fakeagent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(fakeBinDir, name), fakeagent, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A temp git repo to be the launch project.
	proj := filepath.Join(home, "demo-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"commit", "--allow-empty", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = proj
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@test",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	srv := exec.Command(bin, "mcp")
	srv.Dir = proj
	srv.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKEAGENT_TEXT=e2e-ok",
	)
	stdin, err := srv.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); srv.Process.Kill(); srv.Wait() })

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	c := &mcpClient{t: t, stdin: stdin, out: sc}

	init := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "0"},
	})
	if fmt.Sprint(init["result"].(map[string]any)["serverInfo"].(map[string]any)["name"]) != "styx" {
		t.Fatalf("bad initialize: %v", init)
	}
	// initialized notification (no id, no response expected)
	c.stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	return c, proj
}

func TestFirstContact(t *testing.T) {
	c, _ := startServer(t)

	// tools/list: all 13 tools present.
	resp := c.call("tools/list", nil)
	tools, _ := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 13 {
		t.Fatalf("want 13 tools, got %d", len(tools))
	}

	// route: pure local decision.
	route, isErr := c.toolCall("route", map[string]any{"task": "implement the retry logic", "verb": "implement"})
	if isErr || route["channel"] == "" {
		t.Fatalf("route failed: %v", route)
	}

	// budget_status: all four channels.
	if _, isErr := c.toolCall("budget_status", map[string]any{}); isErr {
		t.Fatal("budget_status errored")
	}

	// dispatch cli=claude WITHOUT project: resolves via server cwd (Task 4).
	disp, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "claude", "message": "reply ok", "risk": "read",
	})
	if isErr {
		t.Fatalf("naive dispatch must succeed via cwd project: %v", disp)
	}
	if disp["text"] != "e2e-ok" {
		t.Fatalf("want fakeagent text, got %v", disp["text"])
	}
	if _, ok := disp["duration_s"]; !ok {
		t.Fatal("dispatch result missing duration_s (Task 10)")
	}

	// thread_status without project: [] shape + the thread just created.
	ts, isErr := c.toolCall("thread_status", map[string]any{})
	if isErr {
		t.Fatalf("thread_status errored: %v", ts)
	}
	threads, ok := ts["threads"].([]any)
	if !ok {
		t.Fatalf("threads must be an array (never null), got %T", ts["threads"])
	}
	if len(threads) != 1 || !strings.Contains(threads[0].(string), "claude") {
		t.Fatalf("want the claude thread listed, got %v", threads)
	}

	// unknown project: loud error listing the registry (Task 4). "nope-not-real"
	// is a relative bogus alias, and the server's cwd is the registered
	// demo-proj directory — so this also regression-guards Task 12b: the
	// existence-gated isUnder fallback in internal/target must NOT let a typo'd
	// relative alias silently resolve to the cwd project.
	errRes, isErr := c.toolCall("thread_status", map[string]any{"project": "nope-not-real"})
	if !isErr {
		t.Fatalf("unknown project must error, got %v", errRes)
	}
	if raw, _ := errRes["_raw"].(string); !strings.Contains(raw, "registered projects") {
		t.Fatalf("error must list registered projects, got %v", errRes)
	}
}

func TestVersionVerb(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	out, err := exec.Command(filepath.Join(repoRoot, "bin", "styx"), "version").CombinedOutput()
	if err != nil {
		t.Fatalf("styx version: %v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "styx ") {
		t.Fatalf("want 'styx <version>', got %q", out)
	}
}

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("STYX_E2E_LIVE") != "1" {
		t.Skip("set STYX_E2E_LIVE=1 for real-CLI smoke (uses quota)")
	}
	// Live mode: real PATH (no fakes), real ollama.
	// 1. doctor
	repoRoot, _ := filepath.Abs("..")
	bin := filepath.Join(repoRoot, "bin", "styx")
	if out, err := exec.Command(bin, "doctor").CombinedOutput(); err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	// 2. ollama one-shot with the model default (Task 3), via a live server.
	//    Reuses startServer but strips the fake PATH prefix and cwd-runs in
	//    this repo. Implemented as a minimal inline variant:
	srv := exec.Command(bin, "mcp")
	srv.Dir = repoRoot
	stdin, _ := srv.StdinPipe()
	stdout, _ := srv.StdoutPipe()
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); srv.Process.Kill(); srv.Wait() }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	c := &mcpClient{t: t, stdin: stdin, out: sc}
	c.call("initialize", map[string]any{"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "e2e-live", "version": "0"}})
	stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	res, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "ollama", "message": "Reply with exactly: pong", "risk": "read",
	})
	if isErr {
		t.Fatalf("live ollama dispatch with default model failed: %v", res)
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(res["text"])), "pong") {
		t.Logf("note: unexpected text %v (model behavior, not plumbing)", res["text"])
	}
}
