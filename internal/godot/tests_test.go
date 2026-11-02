package godot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseJUnit(t *testing.T) {
	dir := t.TempDir()
	reports := map[string]string{
		// gdUnit4 writes <testsuites>; failures carry a message and a body.
		"suites.xml": `<?xml version="1.0"?>
<testsuites name="all" tests="3">
  <testsuite name="test_player">
    <testcase name="test_jump"/>
    <testcase name="test_hit"><failure message="FAILED: res://test/test_player.gd:12">Expecting 2 but was 3</failure></testcase>
    <testcase name="test_later"><skipped/></testcase>
  </testsuite>
</testsuites>`,
		// GUT writes a single <testsuite> root in older versions.
		"suite.xml": `<testsuite name="res://test/test_enemy.gd"><testcase name="test_dies"><error message="crashed"/></testcase></testsuite>`,
	}
	want := map[string]TestResult{
		"suites.xml": {Tests: 3, Failures: 1, Skipped: 1, Failed: []TestFailure{{Suite: "test_player", Name: "test_hit", Message: "FAILED: res://test/test_player.gd:12\nExpecting 2 but was 3"}}},
		"suite.xml":  {Tests: 1, Failures: 1, Failed: []TestFailure{{Suite: "res://test/test_enemy.gd", Name: "test_dies", Message: "crashed"}}},
	}
	for name, xml := range reports {
		path := filepath.Join(dir, name)
		os.WriteFile(path, []byte(xml), 0o644)
		got, err := parseJUnit(path)
		w := want[name]
		if err != nil || got.Tests != w.Tests || got.Failures != w.Failures || got.Skipped != w.Skipped || len(got.Failed) != len(w.Failed) || got.Failed[0] != w.Failed[0] {
			t.Errorf("%s: got %+v, %v; want %+v", name, got, err, w)
		}
	}
}
