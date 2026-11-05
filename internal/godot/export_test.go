package godot

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// As Godot 4.7.2 prints it on macOS when the Linux templates are missing.
const missingTemplatesOutput = "ERROR: Cannot export project with preset \"Linux\" due to configuration errors:\r\n" +
	"No export template found at the expected path:\n" +
	"/Users/me/Library/Application Support/Godot/export_templates/4.7.2.stable/linux_debug.x86_64\n" +
	"No export template found at the expected path:\n" +
	"\x1b[0m/Users/me/Library/Application Support/Godot/export_templates/4.7.2.stable/linux_release.x86_64\n" +
	"\n" +
	"   at: _fs_changed (editor/editor_node.cpp:1401)\n" +
	"ERROR: Project export for preset \"Linux\" failed.\n"

func TestMissingTemplates(t *testing.T) {
	dir := "/Users/me/Library/Application Support/Godot/export_templates/4.7.2.stable/"
	want := []string{dir + "linux_debug.x86_64", dir + "linux_release.x86_64"}
	if got := MissingTemplates(missingTemplatesOutput); !slices.Equal(got, want) {
		t.Errorf("MissingTemplates = %q, want %q", got, want)
	}
	if got := MissingTemplates("ERROR: Project export for preset \"Linux\" failed.\n"); got != nil {
		t.Errorf("MissingTemplates(other failure) = %q, want none", got)
	}
}

func TestOutputStat(t *testing.T) {
	dir := t.TempDir()
	missing := OutputStat(filepath.Join(dir, "none"))
	if missing.Exists {
		t.Error("OutputStat of a missing path exists")
	}
	app := filepath.Join(dir, "Game.app")
	os.MkdirAll(filepath.Join(app, "Contents"), 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "a"), make([]byte, 3), 0o644)
	os.WriteFile(filepath.Join(app, "b"), make([]byte, 4), 0o644)
	before := OutputStat(app)
	if !before.Exists || before.Size != 7 || !before.WrittenSince(missing) {
		t.Errorf("OutputStat(folder) = %+v; want 7 bytes, new", before)
	}
	if before.WrittenSince(before) {
		t.Error("an untouched output counts as written")
	}
	os.WriteFile(filepath.Join(app, "b"), make([]byte, 5), 0o644)
	if !OutputStat(app).WrittenSince(before) {
		t.Error("a rewritten output does not count as written")
	}
}
