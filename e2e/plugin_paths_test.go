package e2e

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orebron/godot-mcp/internal/bridge"
)

// A client holding the bridge token can skip the server's path checks, so
// the plugin must refuse paths outside the project on its own.
func TestPluginRejectsPathsOutsideProject(t *testing.T) {
	s := start(t, true)
	c := bridge.New(filepath.Join(s.proj, ".godot", "godot_mcp"))
	t.Cleanup(c.Close)
	outside := t.TempDir()
	// A folder link inside the project that leads out of it.
	linked := os.Symlink(outside, filepath.Join(s.proj, "shared")) == nil

	for _, tc := range []struct {
		command string
		params  map[string]any
	}{
		{"scene_create", map[string]any{"path": filepath.Join(outside, "a.tscn"), "root_type": "Node2D"}},
		{"scene_create", map[string]any{"path": "res://../escape.tscn", "root_type": "Node2D"}},
		{"resource_save", map[string]any{"path": filepath.Join(outside, "b.tres"), "type": "Gradient"}},
		{"scene_open", map[string]any{"path": filepath.Join(s.proj, "demo", "main.tscn")}},
		{"scene_save", map[string]any{"path": filepath.Join(outside, "c.tscn")}},
		{"node_add", map[string]any{"scene": filepath.Join(s.proj, "demo", "player.tscn")}},
		{"scene_create", map[string]any{"path": "res://shared/d.tscn", "root_type": "Node2D"}},
		{"resource_save", map[string]any{"path": "res://shared/e.tres", "type": "Gradient"}},
	} {
		if strings.HasPrefix(fmt.Sprint(tc.params["path"]), "res://shared/") && !linked {
			continue // no links on this system
		}
		if tc.command == "scene_save" || tc.command == "node_add" {
			if _, err := c.Call(s.ctx, "scene_open", map[string]any{"path": "res://demo/main.tscn"}, time.Minute); err != nil {
				t.Fatal(err)
			}
		}
		_, err := c.Call(s.ctx, tc.command, tc.params, time.Minute)
		var be *bridge.Error
		if !errors.As(err, &be) || be.Code != "INVALID_PATH" {
			t.Errorf("%s %v: got %v, want INVALID_PATH", tc.command, tc.params, err)
		}
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Errorf("files were written outside the project: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.proj), "escape.tscn")); err == nil {
		t.Error("res://../escape.tscn was written above the project")
	}
}
