# godot-mcp

An MCP server that lets AI agents (Claude Code, Cursor, Codex, Claude Desktop, ...) work on a Godot 4 project. With it an agent can:

- check GDScript with the project's own Godot,
- run the game and read its errors,
- look into the running game, send it input, pause it and step it frame by frame, and take screenshots of it,
- inspect and edit the scene open in the editor. These edits go through the editor's undo history.

The server is a single static binary for Linux, macOS and Windows (amd64 and arm64). Users don't need Node, Python or any other runtime.

```
MCP client (Claude Code, Cursor, ...)
   │ stdio
   ▼
godot-mcp (Go binary)
   ├── runs Godot itself: script_check, game_run, game_output, game_stop
   │
   ├── WebSocket on 127.0.0.1, token passed in the game's environment
   │   ▲
   │   └── the running game: a bridge node added by game_run's harness
   │       └── game_scene_tree, game_node_get, game_input, game_time, game_screenshot
   │
   │ WebSocket on 127.0.0.1, token from <project>/.godot/godot_mcp/bridge.json
   ▼
addons/godot_mcp (editor plugin, GDScript)
   └── scene_*, node_* and editor_screenshot; edits are undoable with Ctrl+Z
```

The plugin is the listener, so several agent sessions can share one open editor. The server spawns the game process itself and reads its stdout and stderr directly, instead of scraping the editor's Output panel. The game dials back to the server, so the `game_*` tools need neither the editor nor the plugin.

## Requirements

- Godot 4.4 or newer. Development and testing use 4.7.2.
- Nothing else on the user's machine. Go is needed only to build from source.

## Setup

1. **Get the binary.** Download the archive for your OS and CPU from the Releases page, or build it with `go install github.com/orebron/godot-mcp/cmd/godot-mcp@latest`.
2. **Add the plugin to your project.** Copy `addons/godot_mcp/` into your project's `addons/` folder (the release also has it as `godot_mcp_addon_<version>.zip`). Then enable it under *Project → Project Settings → Plugins*. The Output panel shows `Godot MCP: listening on 127.0.0.1:6510`.
3. **Configure your MCP client.** Run:

   ```bash
   godot-mcp config --project /path/to/your/project
   ```

   It prints a ready-to-paste `.mcp.json` block and a `claude mcp add` command. Both use absolute paths, which avoids the usual problems: GUI apps that don't see the shell's `PATH`, and the `cmd /c npx` workaround needed on Windows.

Without the editor open, `project_info`, `script_check` and the `game_*` tools still work.

### Finding Godot

The server looks for Godot in this order:

1. `--godot <path>`
2. `GODOT_PATH`
3. `PATH`: `godot`, `godot4`, `Godot`, plus `godot_console` on Windows
4. Common install locations:
   - macOS: `/Applications/Godot*.app`, `~/Applications`, Steam
   - Windows: winget, Scoop, Program Files, Steam
   - Linux: `~/.local/bin`, `/usr/local/bin`, Snap, Steam, Flatpak `org.godotengine.Godot`

On macOS, `GODOT_PATH` and `--godot` can point at the `.app` bundle itself.

## Tools

| Tool | Needs editor | What it does |
|---|---|---|
| `project_info` | no | Project, Godot version, editor connection and game status. Call it first. |
| `script_check` | no | Parses `.gd` files with the real Godot, so autoloads and `class_name` globals resolve. Returns file, line and message for each failure. |
| `test_run` | no | Runs the project's gdUnit4 or GUT tests headless; returns counts and each failing test, read from the JUnit report. |
| `game_run` | no | Runs the project or one scene, optionally headless. Returns the first output and parsed errors. |
| `game_output` | no | Reads more output using a cursor (`next_since`). `errors_only` returns just the errors. |
| `game_stop` | no | Stops the game and every process it started. |
| `game_scene_tree` | no | Live node tree of the running game, runtime nodes included. Absolute paths such as `/root/Game` reach autoloads. |
| `game_node_get` | no | Live properties and script variables of a node in the running game. |
| `game_node_set` | no | Sets live properties to set up a situation to test, all or none. Nothing is saved. |
| `game_wait` | no | Waits until a node exists, a signal is emitted or a property reaches a value, with a timeout. |
| `game_performance` | no | Frame rate over a sample, process and physics time, node and orphan counts, memory, draw calls. |
| `game_input` | no | Sends an InputMap action, a key, a mouse click or a mouse move. Pressed and released by default; `pressed` sends one half to hold an input. |
| `game_time` | no | Pauses, resumes, or steps exactly N physics frames and pauses again, for checks that come out the same every run. |
| `game_screenshot` | no (windowed game) | PNG of the game's current frame, with the logical size `game_input` positions use. |
| `scene_tree` | yes | Node tree of the edited scene: names, classes, scripts and instanced scenes. |
| `node_get` | yes | Properties of a node. By default only the ones that differ from their defaults. |
| `scene_open` / `scene_create` / `scene_save` | yes | Opens, creates and saves scenes. |
| `node_add` | yes | Adds a node by class (built-in or `class_name`) or instances a `.tscn`, optionally setting properties. |
| `node_set_properties` | yes | Sets several properties in one undoable action. Accepts JSON values or Godot's text form, `res://` paths for resources and scripts, and new resources inline: `{"type": "RectangleShape2D", "size": [16, 16]}`. |
| `node_delete` | yes | Deletes a node and its children. Undoable. |
| `node_move` | yes | Renames, reparents (keeping the global transform) or reorders a node, or toggles Editable Children on an instance. Undoable. |
| `node_duplicate` | yes | Copies a node with its children next to it. Undoable. |
| `resource_save` | yes | Writes a `.tres` of a built-in or `class_name` class with its properties, such as a material or an item's data. |
| `node_signal_connect` | yes | Connects (or disconnects) a signal to a method, saved with the scene, as the Node dock does. Undoable. |
| `project_settings_get` / `project_settings_set` | yes | Reads settings by prefix or name; changes existing ones and saves `project.godot`. Undoable. |
| `input_action_set` | yes | Creates, replaces or removes an input action with keys (physical), mouse buttons and joypad buttons. Undoable. |
| `editor_screenshot` | yes (windowed) | PNG of the editor's 2D or 3D viewport. |

Errors carry a stable `code` plus a `hint` that says what to do next. For example, `NODE_NOT_FOUND` lists the children that do exist at that path, and `PROPERTY_NOT_FOUND` suggests properties with similar names.

## Design notes

- **Godot exits with 0 even when a script fails to parse.** `script_check` therefore reads the error lines Godot prints, never the exit code.
- **New `class_name` scripts are unknown until Godot rescans the project.** `script_check` refreshes the class cache before parsing:
  - With the editor connected, it asks the editor to rescan (the editor owns `.godot/`).
  - Without the editor, it runs `godot --import`, and only when a `class_name` script changed.
- **No undo tool.** Godot does not give plugins the editor's own undo and redo, and calling the history's `UndoRedo` directly gets the editor's undo stack out of step, so the next Ctrl+Z undoes the wrong action. MCP edits stay in the editor's history, so the user can undo them with Ctrl+Z; an agent reverts its own change by making the opposite one.
- **The game_* tools add nothing to the project.** `game_run` writes a small harness scene and a bridge script into `.godot/godot_mcp/run/` (which Godot does not import and the default `.gitignore` excludes) and launches the harness instead of the requested scene. The harness adds the bridge to the root and changes to the real scene, so the game runs as it would on its own: the scene is the current scene and survives `change_scene_to_*` like any other. The server listens on a random `127.0.0.1` port for each run and passes the port and a token in the game's environment, so a game started any other way, an export included, never starts a bridge.
- **The bridge compiles with the project's warning settings.** A project may turn any GDScript warning into an error, and the bridge is compiled inside it, so it is written to raise none; a test compiles it with every warning Godot has set to error.
- **Headless games get their real window size.** The headless display server makes a 64x64 window; the harness sizes it like the window on screen, so the UI lays out the same and clicks land where they would.
- **Frame-exact checks.** `game_time step` waits for one more `physics_frame` signal than the frames asked for, because the signal comes before the nodes' `_physics_process`; pausing then stops the next frame. Hold an input with `game_input` `pressed: true`, step, release, and the result is the same every run.
- **One writer per project.** Scene edits go through the open editor. Writing a `.tscn` by hand while the editor has it open loses changes on the editor's next save.
- **Paths are portable.** The server converts every path to a `res://` path. It refuses paths that leave the project, including through `..\` (Godot reads `\` as a separator on every OS) and through symbolic links or junctions that point outside. It also refuses wrong letter case: a misspelled path works on macOS and Windows but breaks on Linux and in exported `.pck` files.
- **Process trees.** On Unix the game runs in its own process group. On Windows the server uses `taskkill /T`, because killing a parent there leaves its children running. `taskkill /T` only finds children while the game itself is still running. A process the game started that outlives it is not stopped on Windows; a Job Object would cover that and is not implemented yet.
- **Security.**
  - The plugin binds to `127.0.0.1` only. This also avoids the Windows Firewall prompt.
  - Clients must present a 32-byte random token. It is stored in `bridge.json`, inside a folder only its owner can enter on Linux and macOS (`0700`, the file itself `0600`). The file is written under a temporary name and renamed, so a server never reads half of it.
  - The token protects against other users on the same machine, not against other programs running as the same user.
  - On Windows the folder keeps the permissions of the project folder. A project under your user profile is private to you; a project in a shared place such as `C:\Projects` may be readable by other local accounts.

## Development

```bash
go test -short ./...                                    # unit tests, no Godot needed
go test ./e2e/                                          # real binary + headless Godot editor and game
GODOT_MCP_E2E_WINDOWED=1 go test ./e2e/ -run Screenshot # opens an editor window and a game window
```

The repository root is also a Godot project (`project.godot`, `demo/`) for working on the plugin. Go folders carry `.gdignore`, so the Godot editor skips them. The `export-ignore` rules in `.gitattributes` keep everything except `addons/godot_mcp/` out of Asset Library downloads.

Pushing a `v*` tag builds the six binaries with GoReleaser and attaches them, along with the plugin zip, to a GitHub release. Before tagging, update the plugin version in both `plugin.cfg` and `plugin.gd`.

### CI on self-hosted runners

[`ci.yml`](.github/workflows/ci.yml) runs vet, the unit tests and the end-to-end tests with Godot 4.7.2 on self-hosted runners. It selects them by the labels GitHub gives every self-hosted runner by default:

| Target | `runs-on` labels |
|---|---|
| Linux amd64 | `self-hosted`, `Linux`, `X64` (also runs the GoReleaser cross-build) |
| macOS arm64 | `self-hosted`, `macOS`, `ARM64` |
| macOS amd64 | `self-hosted`, `macOS`, `X64` |
| Windows amd64 | `self-hosted`, `Windows`, `X64` |

The tests run on Linux amd64 only by default. A job whose labels match no runner waits in the queue for a day before it fails, so a target is tested only once it has a runner. To choose the targets, set the repository variable `CI_TEST_RUNNERS` (*Settings → Secrets and variables → Actions → Variables*) to a JSON list of them. All four:

```json
[
  {"name": "Linux amd64",   "runner": ["self-hosted", "Linux", "X64"]},
  {"name": "macOS arm64",   "runner": ["self-hosted", "macOS", "ARM64"]},
  {"name": "macOS amd64",   "runner": ["self-hosted", "macOS", "X64"]},
  {"name": "Windows amd64", "runner": ["self-hosted", "Windows", "X64"]}
]
```

`name` appears in the job name and `runner` holds the labels, so a runner with labels of its own works too. The list must not be empty; delete the variable to go back to Linux only. The cross-build always runs on the Linux runner.

What each runner needs:

- **Access to this repository.** The repository is public. An organization runner serves it only if the runner's group has *Allow public repositories* checked (*Organization settings → Actions → Runner groups*). The box is off by default, and the `Default` group then shows *All repositories, excluding public repositories*. Without it, every job stays queued.
- **Git and internet access.** `actions/setup-go` downloads Go itself.
- **Linux and macOS:** `curl` and `unzip`.
- **Windows:** Windows PowerShell, which every Windows install has.
- **Godot:** nothing to install by hand. The [install scripts](.github/scripts/) download the official build once per version into the runner's tool cache and check it against the release's `SHA512-SUMS.txt`. Two ways to change this:
  - To reuse an existing install, set `GODOT_PATH` in the runner's `.env` file.
  - To download from a mirror instead of GitHub, set `GODOT_DOWNLOAD_BASE`.

Two safety measures:

- **Leftover processes.** The end-to-end tests keep their projects in the job's temp folder. A final step ends any Godot or godot-mcp process still running there. A cancelled job would otherwise leave editors running on the machine.
- **Fork pull requests.** A self-hosted runner executes whatever code a workflow hands it, and a pull request can change the workflow itself. The `if:` in `ci.yml` keeps honest fork pull requests off the runners, but a fork can delete it. The real protection is outside the YAML:
  - Keep *Settings → Actions → General → Fork pull request workflows* at *Require approval for all external contributors*, and read a fork's workflow changes before approving a run.
  - Give the runners for this repository a group of their own: *Selected repositories* with only this repository, and *Allow public repositories* checked. Checking that box on the `Default` group instead opens every runner in it to a public repository.
  - GitHub advises against self-hosted runners for public repositories. Use machines that hold nothing else of value, or run fork pull requests on GitHub-hosted runners.

## Roadmap

- An editor dock with a **Configure** button that downloads the right binary and writes the client configuration.
- Publishing on the Godot Asset Library, Homebrew, Scoop and winget.
- Signed and notarized binaries for macOS and Windows.
