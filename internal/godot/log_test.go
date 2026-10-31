package godot

import (
	"strings"
	"testing"
)

// Output of Godot 4.7.2 loading a broken script, as captured on macOS.
const sampleOutput = `Godot Engine v4.7.2.stable.official.ed1daf0bf - https://godotengine.org

SCRIPT ERROR: Parse Error: Cannot assign a value of type "String" as "int".
          at: GDScript::reload (res://bad.gd:3)
          GDScript backtrace (most recent call first):
              [0] _initialize (res://.godot/godot_mcp/check.gd:5)
ERROR: Failed to load script "res://bad.gd" with error "Parse error".
   at: load (modules/gdscript/gdscript_resource_format.cpp:46)
ERROR: Attempt to open script 'res://missing.gd' resulted in error 'File not found'.
   at: load_source_code (modules/gdscript/gdscript.cpp:1139)
WARNING: something odd
` + "\x1b[1;33mUSER WARNING: coloured\x1b[0m\r\n" + `GODOT_MCP_RESULT []`

func TestParseIssues(t *testing.T) {
	got := ParseIssues(strings.Split(sampleOutput, "\n"))
	want := []Issue{
		{Severity: "error", Kind: "SCRIPT ERROR", Message: `Parse Error: Cannot assign a value of type "String" as "int".`, File: "res://bad.gd", Line: 3, Where: "GDScript::reload (res://bad.gd:3)"},
		{Severity: "error", Kind: "ERROR", Message: `Failed to load script "res://bad.gd" with error "Parse error".`, File: "res://bad.gd", Where: "load (modules/gdscript/gdscript_resource_format.cpp:46)"},
		{Severity: "error", Kind: "ERROR", Message: `Attempt to open script 'res://missing.gd' resulted in error 'File not found'.`, File: "res://missing.gd", Where: "load_source_code (modules/gdscript/gdscript.cpp:1139)"},
		{Severity: "warning", Kind: "WARNING", Message: "something odd"},
		{Severity: "warning", Kind: "USER WARNING", Message: "coloured"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d issues, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

// File names may contain parentheses, colons and dots; the location still
// has to be found.
func TestParseIssuesOddFileNames(t *testing.T) {
	got := ParseIssues([]string{
		`SCRIPT ERROR: Parse Error: Unexpected "Indent".`,
		`          at: GDScript::reload (res://scripts/player (copy):v2.gd:3)`,
		`ERROR: Failed loading resource: res://levels/boss(1).tscn.`,
		`   at: _load (core/io/resource_loader.cpp:317)`,
		`ERROR: Cannot open file (res://a.gd).`,
	})
	if len(got) != 3 {
		t.Fatalf("got %d issues: %+v", len(got), got)
	}
	if got[0].File != "res://scripts/player (copy):v2.gd" || got[0].Line != 3 {
		t.Errorf("parenthesised name: %+v", got[0])
	}
	if got[1].File != "res://levels/boss(1).tscn" {
		t.Errorf("path followed by a full stop: %+v", got[1])
	}
	if got[2].File != "res://a.gd" {
		t.Errorf("path inside parentheses: %+v", got[2])
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"4.7.2.stable.official.ed1daf0bf\n":                    {Raw: "4.7.2.stable.official.ed1daf0bf", Major: 4, Minor: 7, Patch: 2},
		"4.4.stable.official.4c311cbee":                        {Raw: "4.4.stable.official.4c311cbee", Major: 4, Minor: 4},
		"Note: flatpak notice\r\n4.6.1.stable.flathub.abc\r\n": {Raw: "4.6.1.stable.flathub.abc", Major: 4, Minor: 6, Patch: 1},
	} {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := ParseVersion("garbage"); err == nil {
		t.Error("ParseVersion(garbage) did not fail")
	}
	if (Version{Major: 4, Minor: 3}).AtLeast(MinMajor, MinMinor) {
		t.Error("4.3 passed the minimum version check")
	}
}
