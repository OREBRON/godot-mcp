//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// Only the owner may read the bridge token, so the folder and the file are
// private.
func TestBridgeFilesArePrivate(t *testing.T) {
	s := start(t, true)
	dir := filepath.Join(s.proj, ".godot", "godot_mcp")
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "bridge.json"): 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "bridge.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("bridge.json.tmp was left behind: %v", err)
	}
	s.ok("project_info", nil)
}
