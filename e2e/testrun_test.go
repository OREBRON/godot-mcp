package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Stand-ins for the frameworks' command-line runners. They read the
// arguments the real ones take (GUT 9.x, gdUnit4 6.x), write a JUnit report
// where those say, and exit as the real ones do.
const fakeGUT = `extends SceneTree
func _initialize() -> void:
	var report := ""
	var dirs := []
	for arg in OS.get_cmdline_args():
		if arg.begins_with("-gjunit_xml_file="):
			report = arg.trim_prefix("-gjunit_xml_file=")
		if arg.begins_with("-gdir="):
			dirs.append(arg.trim_prefix("-gdir="))
	var f := FileAccess.open(report, FileAccess.WRITE)
	f.store_string('<testsuites><testsuite name="%s"><testcase name="test_ok"/><testcase name="test_bad"><failure message="expected 1, got 2"/></testcase></testsuite></testsuites>' % ",".join(dirs))
	f.close()
	quit(1)
`

const fakeGdUnit = `extends SceneTree
func _initialize() -> void:
	var args := OS.get_cmdline_args()
	if not args.has("--ignoreHeadlessMode"):
		quit(103)
		return
	var dir := ProjectSettings.globalize_path(args[args.find("-rd") + 1]).path_join("report_1")
	DirAccess.make_dir_recursive_absolute(dir)
	var f := FileAccess.open(dir.path_join("results.xml"), FileAccess.WRITE)
	f.store_string('<testsuites><testsuite name="%s"><testcase name="test_fine"/></testsuite></testsuites>' % args[args.find("-a") + 1])
	f.close()
	quit(0)
`

func TestTestRun(t *testing.T) {
	s := start(t, true)
	s.fails("test_run", nil, "no test framework found")

	write(t, filepath.Join(s.proj, "addons", "gut", "gut_cmdln.gd"), fakeGUT)
	s.fails("test_run", nil, "no res://test")
	write(t, filepath.Join(s.proj, "test", "test_player.gd"), "extends Node\n")
	gut := s.ok("test_run", nil)
	failed, _ := json.Marshal(gut["failed"])
	if gut["framework"] != "gut" || gut["tests"] != 2.0 || gut["failures"] != 1.0 || gut["ok"] != false || gut["exit_code"] != 1.0 ||
		!strings.Contains(string(failed), `"name":"test_bad"`) || !strings.Contains(string(failed), `"suite":"res://test"`) {
		t.Errorf("GUT run = %v", gut)
	}

	write(t, filepath.Join(s.proj, "addons", "gdUnit4", "bin", "GdUnitCmdTool.gd"), fakeGdUnit)
	if r := s.ok("test_run", nil); r["framework"] != "gdunit4" || r["tests"] != 1.0 || r["ok"] != true {
		t.Errorf("gdUnit4 run (picked first) = %v", r)
	}
	if r := s.ok("test_run", map[string]any{"framework": "gut"}); r["framework"] != "gut" {
		t.Errorf("framework gut = %v", r)
	}
	s.fails("test_run", map[string]any{"dirs": []any{"res://nope"}}, "does not exist")
}
