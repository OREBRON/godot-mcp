//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Godot's save calls do not report a failed write, so scene_save has to
// notice on its own that the file was not rewritten.
func TestSceneSaveReportsFailedWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only files")
	}
	s := start(t, true)
	s.ok("scene_create", map[string]any{"path": "res://locked/level.tscn", "root_type": "Node2D"})
	s.ok("node_add", map[string]any{"type": "Node2D", "name": "First"})
	s.ok("scene_save", nil)

	dir := filepath.Join(s.proj, "locked")
	file := filepath.Join(dir, "level.tscn")
	os.Chmod(file, 0o444)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755); os.Chmod(file, 0o644) })

	s.ok("node_add", map[string]any{"type": "Node2D", "name": "Second"})
	s.fails("scene_save", nil, "SAVE_FAILED")
	s.fails("scene_save", map[string]any{"path": "res://locked/copy.tscn"}, "SAVE_FAILED")

	os.Chmod(dir, 0o755)
	os.Chmod(file, 0o644)
	s.ok("scene_save", nil)
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), `name="Second"`) {
		t.Errorf("the save after unlocking did not write Second:\n%s", b)
	}
}
