package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orebron/godot-mcp/internal/game"
	"github.com/orebron/godot-mcp/internal/godot"
)

// The harness and bridge compile inside the user's project, with its warning
// settings, which may turn any warning into an error. They must compile with
// every warning this Godot knows set to error.
func TestHarnessUnderStrictWarnings(t *testing.T) {
	g := findGodot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	proj := t.TempDir()
	write(t, filepath.Join(proj, "project.godot"), "config_version=5\n")
	write(t, filepath.Join(proj, "list_warnings.gd"), `extends SceneTree
func _initialize() -> void:
	for p in ProjectSettings.get_property_list():
		if str(p.name).begins_with("debug/gdscript/warnings/") and p.type == TYPE_INT:
			print("WARNING_SETTING ", p.name)
	quit()
`)
	out, err := g.Command(ctx, "--headless", "--path", proj, "--script", "res://list_warnings.gd").CombinedOutput()
	if err != nil {
		t.Fatalf("listing warnings: %v\n%s", err, out)
	}
	var strict strings.Builder
	strict.WriteString("config_version=5\n\n[debug]\n\n")
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "WARNING_SETTING debug/"); ok {
			strict.WriteString(name + "=2\n")
			count++
		}
	}
	if count < 20 {
		t.Fatalf("found only %d warning settings:\n%s", count, out)
	}
	write(t, filepath.Join(proj, "project.godot"), strict.String())

	runDir := filepath.Join(proj, ".godot", "godot_mcp", "run")
	if _, err := game.InstallHarness(runDir, "res://.godot/godot_mcp/run"); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"harness.gd", "bridge.gd"} {
		out, _ := g.Command(ctx, "--headless", "--path", proj, "--check-only", "--script", "res://.godot/godot_mcp/run/"+script).CombinedOutput()
		for _, is := range godot.ParseIssues(strings.Split(string(out), "\n")) {
			if is.Severity == "error" {
				t.Errorf("%s with all %d warnings as errors: %s (%s)", script, count, is.Message, is.Where)
			}
		}
	}
	_ = os.Remove(filepath.Join(proj, "list_warnings.gd"))
}
