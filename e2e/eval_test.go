package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Agent evaluations: a real Claude Code agent gets a task in a user's words
// and only this server's tools, and a grader checks the project or the answer
// it leaves. They spend API money, so they run only with GODOT_MCP_EVAL=1.

// evalTask is one task. setup runs on the fresh project copy before the
// editor starts; verify returns what is wrong, nothing when the task passed.
type evalTask struct {
	name   string
	setup  func(t *testing.T, proj string)
	prompt string
	verify func(s *session, answer string) []string
}

type toolError struct {
	Tool, Text string
}

type evalResult struct {
	Task     string
	Problems []string
	Answer   string
	Stop     string // the result event's subtype, "success" when the agent finished
	Turns    int
	Cost     float64
	Duration time.Duration
	Calls    map[string]int
	Errors   []toolError
}

func TestAgentEval(t *testing.T) {
	if os.Getenv("GODOT_MCP_EVAL") != "1" {
		t.Skip("agent evaluations spend API money; set GODOT_MCP_EVAL=1 to run them")
	}
	out := evalOutDir(t)
	var results []evalResult
	for _, task := range evalTasks {
		t.Run(task.name, func(t *testing.T) {
			results = append(results, runTask(t, task, out))
		})
	}
	summary := summarize(results)
	write(t, filepath.Join(out, "summary.md"), summary)
	t.Logf("results in %s\n%s", out, summary)
}

func evalOutDir(t *testing.T) string {
	if dir := os.Getenv("GODOT_MCP_EVAL_OUT"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	dir, err := os.MkdirTemp("", "godot-mcp-eval-")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// runTask gives the task a fresh project copy and a headless editor of its
// own, runs the agent on it and grades what it left.
func runTask(t *testing.T, task evalTask, out string) evalResult {
	timeout, err := time.ParseDuration(envOr("GODOT_MCP_EVAL_TIMEOUT", "10m"))
	if err != nil {
		t.Fatalf("GODOT_MCP_EVAL_TIMEOUT: %v", err)
	}
	s := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Minute)
	t.Cleanup(cancel)
	s.ctx = ctx // newSession's few minutes are too short for an agent
	if task.setup != nil {
		task.setup(t, s.proj)
	}
	s.openEditor(true)
	res := s.runAgent(task.prompt, filepath.Join(out, task.name+".jsonl"), timeout)
	res.Task = task.name
	if res.Stop != "success" {
		res.Problems = append(res.Problems, "the agent stopped with "+res.Stop)
	}
	// The user's next Ctrl+S: a scene the agent changed on disk but not in
	// the editor loses that change, so the grader sees what the user keeps.
	if text, isErr := s.call("scene_save", nil); isErr && !strings.Contains(text, "NO_SCENE_OPEN") {
		res.Problems = append(res.Problems, "saving the edited scene after the agent: "+text)
	}
	res.Problems = append(res.Problems, task.verify(s, res.Answer)...)
	for _, p := range res.Problems {
		t.Error(p)
	}
	return res
}

// runAgent runs Claude Code headless in the project with only the godot
// server and the file tools, writing its stream-json transcript to transcript.
func (s *session) runAgent(prompt, transcript string, timeout time.Duration) evalResult {
	t := s.t
	cfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"godot": map[string]any{
		"command": s.bin, "args": []string{"--project", s.proj, "--godot", s.godotPath},
	}}})
	cfgPath := filepath.Join(t.TempDir(), "mcp.json")
	write(t, cfgPath, string(cfg))
	f, err := os.Create(transcript)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(s.ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(cfgPath)...)
	cmd.Dir, cmd.Stdin, cmd.Stdout = s.proj, strings.NewReader(prompt), f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	began := time.Now()
	runErr := cmd.Run()
	res := parseTranscript(t, transcript)
	if res.Duration == 0 {
		res.Duration = time.Since(began)
	}
	if res.Stop == "" {
		res.Stop = fmt.Sprintf("no result (%v): %s", runErr, strings.TrimSpace(stderr.String()))
	}
	return res
}

// claudeArgs runs one non-interactive turn. The prompt comes on stdin, as a
// positional argument after --mcp-config would be read as another config.
func claudeArgs(mcpConfig string) []string {
	return []string{
		"-p", "--output-format", "stream-json", "--verbose",
		// No user, project or local settings: the user's hooks, plugins and
		// CLAUDE.md stay out, while the login still works (--bare would need
		// ANTHROPIC_API_KEY).
		"--setting-sources", "",
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		"--tools", "Read,Edit,Write,Glob,Grep",
		"--allowedTools", "mcp__godot__*,Read,Edit,Write,Glob,Grep",
		"--permission-mode", "dontAsk", // anything not allowed is denied, never asked
		"--model", envOr("GODOT_MCP_EVAL_MODEL", "sonnet"),
		"--max-budget-usd", envOr("GODOT_MCP_EVAL_BUDGET_USD", "1"),
		"--no-session-persistence",
	}
}

type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Result     string  `json:"result"`
	NumTurns   int     `json:"num_turns"`
	Cost       float64 `json:"total_cost_usd"`
	DurationMS int64   `json:"duration_ms"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// parseTranscript counts the tool calls and their errors and reads the
// answer, turns, cost and duration from the final result event.
func parseTranscript(t *testing.T, path string) evalResult {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	res := evalResult{Calls: map[string]int{}}
	names := map[string]string{} // tool_use id -> tool name
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var ev streamEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if ev.Type == "result" {
			res.Stop, res.Answer, res.Turns, res.Cost = ev.Subtype, ev.Result, ev.NumTurns, ev.Cost
			res.Duration = time.Duration(ev.DurationMS) * time.Millisecond
		}
		var blocks []contentBlock
		_ = json.Unmarshal(ev.Message.Content, &blocks) // a plain string holds no tool calls
		res.addBlocks(blocks, names)
	}
	return res
}

func (r *evalResult) addBlocks(blocks []contentBlock, names map[string]string) {
	for _, b := range blocks {
		switch {
		case b.Type == "tool_use":
			names[b.ID] = b.Name
			r.Calls[b.Name]++
		case b.Type == "tool_result" && b.IsError:
			r.Errors = append(r.Errors, toolError{names[b.ToolUseID], resultText(b.Content)})
		}
	}
}

// resultText reads a tool result's content, a string or a list of text parts.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct{ Text string }
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

func summarize(results []evalResult) string {
	var b strings.Builder
	b.WriteString("| Task | Result | Turns | Tool calls | Tool errors | Cost | Duration |\n|---|---|---|---|---|---|---|\n")
	var cost float64
	passed := 0
	for _, r := range results {
		verdict := "FAIL"
		if len(r.Problems) == 0 {
			verdict = "pass"
			passed++
		}
		calls := 0
		for _, n := range r.Calls {
			calls += n
		}
		cost += r.Cost
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | $%.3f | %s |\n", r.Task, verdict, r.Turns, calls, len(r.Errors), r.Cost, r.Duration.Round(time.Second))
	}
	fmt.Fprintf(&b, "\n%d of %d passed, $%.2f in all.\n", passed, len(results), cost)
	for _, r := range results {
		b.WriteString(details(r))
	}
	return b.String()
}

func details(r evalResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s\n\n", r.Task)
	for _, p := range r.Problems {
		fmt.Fprintf(&b, "- problem: %s\n", p)
	}
	for _, n := range slices.Sorted(maps.Keys(r.Calls)) {
		fmt.Fprintf(&b, "- %s x%d\n", strings.TrimPrefix(n, "mcp__godot__"), r.Calls[n])
	}
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "- error from %s: %s\n", strings.TrimPrefix(e.Tool, "mcp__godot__"), oneLine(e.Text, 400))
	}
	fmt.Fprintf(&b, "\nAnswer: %s\n", oneLine(r.Answer, 600))
	return b.String()
}

// The transcript parser runs without Godot or an agent.
func TestParseTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	write(t, path, `{"type":"system","subtype":"init"}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"mcp__godot__node_add","input":{}},{"type":"tool_use","id":"b","name":"Read","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","is_error":true,"content":[{"type":"text","text":"NODE_NOT_FOUND"}]},{"type":"tool_result","tool_use_id":"b","content":"ok"}]}}
{"type":"user","message":{"content":"plain text"}}
{"type":"result","subtype":"success","result":"done","num_turns":3,"total_cost_usd":0.25,"duration_ms":1500}
`)
	r := parseTranscript(t, path)
	want := evalResult{Answer: "done", Stop: "success", Turns: 3, Cost: 0.25, Duration: 1500 * time.Millisecond,
		Calls: map[string]int{"mcp__godot__node_add": 1, "Read": 1}, Errors: []toolError{{"mcp__godot__node_add", "NODE_NOT_FOUND"}}}
	if fmt.Sprint(r) != fmt.Sprint(want) {
		t.Errorf("parseTranscript = %+v, want %+v", r, want)
	}
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
