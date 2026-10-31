// Command godot-mcp is an MCP server that lets AI agents work on a Godot 4
// project: check scripts, run the game and read its errors, and edit scenes
// in the open editor through the godot_mcp plugin.
//
//	godot-mcp [--project DIR] [--godot PATH]   serve MCP on stdin/stdout
//	godot-mcp config [--project DIR]           print MCP client configuration
//	godot-mcp version                          print the version
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/orebron/godot-mcp/internal/project"
	"github.com/orebron/godot-mcp/internal/server"
)

func main() {
	// stdout carries the MCP protocol; anything else goes to stderr.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && (args[0] == "config" || args[0] == "version" || args[0] == "serve") {
		cmd, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("godot-mcp", flag.ExitOnError)
	projectFlag := fs.String("project", "", "Godot project folder (default: GODOT_PROJECT, then the nearest folder with project.godot above the working directory)")
	godotFlag := fs.String("godot", "", "Godot binary or macOS .app (default: GODOT_PATH, then PATH and the usual install folders)")
	fs.Parse(args)

	switch cmd {
	case "version":
		fmt.Println("godot-mcp", server.Version)
	case "config":
		printConfig(*projectFlag, *godotFlag)
	default:
		dir, err := findProject(*projectFlag)
		if err != nil {
			slog.Warn("no Godot project yet; tools will report it", "err", err)
		}
		s := server.New(server.Options{ProjectDir: dir, ProjectErr: err, GodotPath: *godotFlag})
		// Clients and supervisors stop servers with SIGTERM as often as with
		// Ctrl+C. Both must reach the normal shutdown, which ends the game:
		// it runs in its own process group and would outlive the server.
		// (Windows delivers neither; there the client closes stdin.)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := s.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("server stopped", "err", err)
			os.Exit(1)
		}
	}
}

func findProject(flagDir string) (string, error) {
	dir := flagDir
	if dir == "" {
		dir = os.Getenv("GODOT_PROJECT")
	}
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(filepath.Join(abs, "project.godot")); err != nil {
			return "", fmt.Errorf("%s has no project.godot", abs)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return project.Find(wd)
}

// printConfig prints ready-to-paste client configuration with absolute
// paths, so no client has to find the binary on PATH (GUI apps often do not
// see the shell's PATH, and Windows clients cannot start npx-style shims).
func printConfig(projectDir, godotPath string) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot find own path:", err)
		os.Exit(1)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, err := findProject(projectDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning:", err, "- the config below has no --project; the client's working folder decides")
	}
	args := []string{}
	if dir != "" {
		args = append(args, "--project", dir)
	}
	if godotPath != "" {
		args = append(args, "--godot", godotPath)
	}
	entry := map[string]any{"command": exe, "args": args}
	cfg, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"godot": entry}}, "", "  ")

	fmt.Println("# .mcp.json (Claude Code, project scope), Cursor (.cursor/mcp.json), Claude Desktop:")
	fmt.Println(string(cfg))
	fmt.Println()
	fmt.Println("# Claude Code from a terminal:")
	line := "claude mcp add godot -- " + shellQuote(exe)
	for _, a := range args {
		line += " " + shellQuote(a)
	}
	fmt.Println(line)
}

// shellQuote wraps s in double quotes, which both POSIX shells and
// PowerShell accept for paths with spaces. Backslashes stay as they are.
func shellQuote(s string) string { return `"` + s + `"` }
