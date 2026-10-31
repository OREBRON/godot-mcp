// Package godot finds the Godot binary, runs it and reads its output.
package godot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/orebron/godot-mcp/internal/proc"
)

// Exec is how to start Godot: a program plus fixed leading arguments
// (Flatpak needs "run org.godotengine.Godot" before Godot's own flags).
type Exec struct {
	Path   string   `json:"path"`
	Args   []string `json:"args,omitempty"`
	Source string   `json:"source"` // where it was found, for diagnostics
}

// Command builds a Godot command with process-tree handling already set up.
func (e Exec) Command(ctx context.Context, args ...string) *exec.Cmd {
	all := append(append([]string{}, e.Args...), args...)
	cmd := exec.CommandContext(ctx, e.Path, all...)
	proc.Prepare(cmd)
	return cmd
}

func (e Exec) String() string {
	return strings.TrimSpace(e.Path + " " + strings.Join(e.Args, " "))
}

// Locate finds Godot. Order: the explicit override, GODOT_PATH, PATH, then
// the usual install folders of each OS.
func Locate(override string) (Exec, error) {
	if override != "" {
		return fromUser(override, "--godot flag")
	}
	if env := os.Getenv("GODOT_PATH"); env != "" {
		return fromUser(env, "GODOT_PATH")
	}
	for _, name := range pathNames() {
		if p, err := exec.LookPath(name); err == nil {
			return Exec{Path: p, Source: "PATH (" + name + ")"}, nil
		}
	}
	for _, c := range defaultLocations() {
		if isFile(c) {
			return Exec{Path: c, Source: "default location"}, nil
		}
	}
	if runtime.GOOS == "linux" {
		if fp, err := exec.LookPath("flatpak"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if exec.CommandContext(ctx, fp, "info", "org.godotengine.Godot").Run() == nil {
				return Exec{Path: fp, Args: []string{"run", "org.godotengine.Godot"}, Source: "Flatpak"}, nil
			}
		}
	}
	return Exec{}, errors.New(notFoundHelp())
}

func fromUser(p, source string) (Exec, error) {
	p = filepath.Clean(p)
	// Accept the macOS app bundle as well as the binary inside it.
	if runtime.GOOS == "darwin" && strings.HasSuffix(strings.ToLower(p), ".app") {
		p = filepath.Join(p, "Contents", "MacOS", "Godot")
	}
	if !isFile(p) {
		if lp, err := exec.LookPath(p); err == nil {
			p = lp
		} else {
			return Exec{}, fmt.Errorf("%s points to %q, which is not an executable file", source, p)
		}
	}
	return Exec{Path: p, Source: source}, nil
}

func pathNames() []string {
	if runtime.GOOS == "windows" {
		// winget installs both; godot.exe output reaches a pipe.
		return []string{"godot", "godot_console", "Godot"}
	}
	return []string{"godot", "godot4", "Godot"}
}

func defaultLocations() []string {
	home, _ := os.UserHomeDir()
	var c []string
	switch runtime.GOOS {
	case "darwin":
		c = append(c,
			"/Applications/Godot.app/Contents/MacOS/Godot",
			filepath.Join(home, "Applications/Godot.app/Contents/MacOS/Godot"),
		)
		c = append(c, globNewest("/Applications/Godot*.app/Contents/MacOS/Godot")...)
		c = append(c, filepath.Join(home, "Library/Application Support/Steam/steamapps/common/Godot Engine/Godot.app/Contents/MacOS/Godot"))
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		pf := os.Getenv("ProgramFiles")
		pf86 := os.Getenv("ProgramFiles(x86)")
		c = append(c,
			filepath.Join(local, "Microsoft", "WinGet", "Links", "godot.exe"),
			filepath.Join(home, "scoop", "shims", "godot.exe"),
		)
		c = append(c, globNewest(filepath.Join(pf, "Godot*", "Godot*.exe"))...)
		c = append(c, globNewest(filepath.Join(local, "Programs", "Godot*", "Godot*.exe"))...)
		c = append(c, globNewest(filepath.Join(pf86, "Steam", "steamapps", "common", "Godot Engine", "godot*.exe"))...)
	default:
		c = append(c,
			filepath.Join(home, ".local", "bin", "godot"),
			"/usr/local/bin/godot",
			"/snap/bin/godot",
		)
		c = append(c, globNewest(filepath.Join(home, ".local", "share", "Steam", "steamapps", "common", "Godot Engine", "godot*"))...)
	}
	return c
}

// globNewest returns matches sorted so that higher version names come first.
func globNewest(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	sort.Sort(sort.Reverse(sort.StringSlice(m)))
	return m
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func notFoundHelp() string {
	var how string
	switch runtime.GOOS {
	case "darwin":
		how = "install it with `brew install --cask godot` or set GODOT_PATH to /Applications/Godot.app"
	case "windows":
		how = "install it with `winget install GodotEngine.GodotEngine` or set GODOT_PATH to the full path of Godot_v4.x_win64.exe"
	default:
		how = "put `godot` on PATH, install the Flatpak org.godotengine.Godot, or set GODOT_PATH to the binary"
	}
	return "Godot was not found: " + how + ". You can also pass --godot <path> in the MCP server config."
}
