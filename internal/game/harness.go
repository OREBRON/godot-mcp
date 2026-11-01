package game

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

// The harness scene and the bridge are written into the project's data
// folder before each run and launched in place of the requested scene, so
// game_run needs nothing added to the project: no autoload, no plugin.
//
//go:embed harness/*.gd
var harnessFiles embed.FS

// InstallHarness writes the harness into dir, whose res:// path is resDir,
// and returns the res:// path of the scene to launch.
func InstallHarness(dir, resDir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	files := map[string][]byte{}
	for _, name := range []string{"harness.gd", "bridge.gd"} {
		b, err := harnessFiles.ReadFile("harness/" + name)
		if err != nil {
			return "", err
		}
		files[name] = b
	}
	files["harness.tscn"] = []byte(fmt.Sprintf(`[gd_scene load_steps=2 format=3]

[ext_resource type="Script" path="%s/harness.gd" id="1"]

[node name="GodotMCPHarness" type="Node"]
script = ExtResource("1")
`, resDir))
	for name, content := range files {
		if err := writeIfChanged(filepath.Join(dir, name), content); err != nil {
			return "", err
		}
	}
	return resDir + "/harness.tscn", nil
}

// writeIfChanged replaces path with content through a rename, so a game
// started by another server on the same project never reads half a file.
func writeIfChanged(path string, content []byte) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
