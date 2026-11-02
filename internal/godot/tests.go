package godot

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orebron/godot-mcp/internal/project"
)

// TestResult is a test run's outcome, read from the framework's JUnit report.
type TestResult struct {
	Framework    string        `json:"framework"`
	OK           bool          `json:"ok"`
	Tests        int           `json:"tests"`
	Failures     int           `json:"failures"`
	Skipped      int           `json:"skipped"`
	Failed       []TestFailure `json:"failed,omitempty"`
	ExitCode     int           `json:"exit_code"`
	ScriptErrors []Issue       `json:"script_errors,omitempty"`
}

// TestFailure is one failed or errored test case.
type TestFailure struct {
	Suite   string `json:"suite"`
	Name    string `json:"name"`
	Message string `json:"message"`
}

// The runners' entry scripts, as gdUnit4 6.x and GUT 9.x install them.
const (
	gdunitTool = "addons/gdUnit4/bin/GdUnitCmdTool.gd"
	gutTool    = "addons/gut/gut_cmdln.gd"
)

// DetectFramework picks the installed test framework: gdUnit4 first, since
// it is the one whose JUnit report the run reads by default.
func DetectFramework(p *project.Project, want string) (string, error) {
	have := map[string]bool{
		"gdunit4": isFile(filepath.Join(p.Dir, gdunitTool)),
		"gut":     isFile(filepath.Join(p.Dir, gutTool)),
	}
	if want != "" && want != "auto" {
		if !have[want] {
			return "", fmt.Errorf("%s is not installed in this project", want)
		}
		return want, nil
	}
	for _, f := range []string{"gdunit4", "gut"} {
		if have[f] {
			return f, nil
		}
	}
	return "", errors.New("no test framework found: install gdUnit4 (addons/gdUnit4) or GUT (addons/gut) from the Asset Library")
}

// DefaultTestDirs is res://test or res://tests, whichever exists.
func DefaultTestDirs(p *project.Project) ([]string, error) {
	for _, d := range []string{"test", "tests"} {
		if fi, err := os.Stat(filepath.Join(p.Dir, d)); err == nil && fi.IsDir() {
			return []string{"res://" + d}, nil
		}
	}
	return nil, errors.New("no res://test or res://tests folder; pass dirs")
}

// RunTests runs the framework's command-line runner headless and reads its
// JUnit report. The report goes to a folder of its own, removed afterwards.
func (e Exec) RunTests(ctx context.Context, p *project.Project, framework string, dirs []string, timeout time.Duration) (TestResult, error) {
	if err := os.MkdirAll(p.MCPDir(), 0o700); err != nil {
		return TestResult{}, err
	}
	reportDir, err := os.MkdirTemp(p.MCPDir(), "tests-")
	if err != nil {
		return TestResult{}, err
	}
	defer os.RemoveAll(reportDir)
	args, err := testArgs(p, framework, dirs, reportDir)
	if err != nil {
		return TestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := e.Command(ctx, args...)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return TestResult{}, fmt.Errorf("the tests did not finish within %s; a test may wait forever (an await without a timeout, pause_before_teardown)", timeout)
	}
	return readTestReport(framework, reportDir, out, cmd.ProcessState.ExitCode())
}

func testArgs(p *project.Project, framework string, dirs []string, reportDir string) ([]string, error) {
	args := []string{"--headless", "--path", p.Dir}
	if framework == "gut" {
		args = append(args, "-s", "res://"+gutTool, "-ginclude_subdirs", "-gexit", "-gignore_pause",
			"-gjunit_xml_file="+filepath.Join(reportDir, "junit.xml"))
		for _, d := range dirs {
			args = append(args, "-gdir="+d)
		}
		return args, nil
	}
	// gdUnit4 nests an absolute -rd under the project, so it gets a res:// path.
	reportRes, err := p.ToRes(reportDir)
	if err != nil {
		return nil, err
	}
	args = append(args, "-s", "res://"+gdunitTool, "--ignoreHeadlessMode")
	for _, d := range dirs {
		args = append(args, "-a", d)
	}
	return append(args, "-rd", reportRes, "-c"), nil
}

// readTestReport finds the JUnit file in reportDir and summarises it.
func readTestReport(framework, reportDir string, out []byte, code int) (TestResult, error) {
	issues := errorsOnly(ParseIssues(strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")))
	report := findReport(reportDir)
	if report == "" {
		return TestResult{}, fmt.Errorf("the %s run wrote no JUnit report (exit %d); its last output:\n%s", framework, code, tail(string(out), 30))
	}
	res, err := parseJUnit(report)
	if err != nil {
		return TestResult{}, err
	}
	res.Framework, res.ExitCode, res.ScriptErrors = framework, code, first(issues, 20)
	res.OK = res.Failures == 0 && res.Tests > 0 && len(issues) == 0
	return res, nil
}

// findReport returns the newest *.xml under dir, or "".
func findReport(dir string) string {
	var newest string
	var when time.Time
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".xml" {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().After(when) {
			newest, when = path, fi.ModTime()
		}
		return nil
	})
	return newest
}

func errorsOnly(issues []Issue) []Issue {
	var out []Issue
	for _, is := range issues {
		if is.Severity == "error" {
			out = append(out, is)
		}
	}
	return out
}

func first[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// junitSuite reads both a <testsuites> root and a single <testsuite> root,
// and suites nested in suites.
type junitSuite struct {
	Name   string       `xml:"name,attr"`
	Cases  []junitCase  `xml:"testcase"`
	Suites []junitSuite `xml:"testsuite"`
}

type junitCase struct {
	Name    string    `xml:"name,attr"`
	Failure *junitMsg `xml:"failure"`
	Error   *junitMsg `xml:"error"`
	Skipped *struct{} `xml:"skipped"`
}

type junitMsg struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// parseJUnit counts the cases themselves rather than trusting the
// frameworks' totals, which differ in what they count.
func parseJUnit(path string) (TestResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return TestResult{}, err
	}
	var root junitSuite
	if err := xml.Unmarshal(b, &root); err != nil {
		return TestResult{}, fmt.Errorf("reading the JUnit report %s: %w", path, err)
	}
	var res TestResult
	res.add(root)
	return res, nil
}

func (r *TestResult) add(s junitSuite) {
	for _, c := range s.Cases {
		r.Tests++
		if c.Skipped != nil {
			r.Skipped++
		}
		if msg := firstMessage(c.Failure, c.Error); msg != nil {
			r.Failures++
			r.Failed = append(r.Failed, TestFailure{Suite: s.Name, Name: c.Name, Message: msg.text()})
		}
	}
	for _, sub := range s.Suites {
		r.add(sub)
	}
}

func firstMessage(msgs ...*junitMsg) *junitMsg {
	for _, m := range msgs {
		if m != nil {
			return m
		}
	}
	return nil
}

func (m *junitMsg) text() string {
	t := strings.TrimSpace(m.Message)
	if body := strings.TrimSpace(m.Text); body != "" && body != t {
		t = strings.TrimSpace(t + "\n" + body)
	}
	return t
}
