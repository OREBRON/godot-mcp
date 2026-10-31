#!/usr/bin/env bash
# Ends Godot editors, games and godot-mcp servers that a cancelled or failed
# test run left behind. A self-hosted runner keeps running between jobs, and
# Godot runs in its own process group, out of reach of the runner's cleanup.
# Only processes working under RUNNER_TEMP match: the tests keep their
# projects and binaries there.
set -u
temp="${RUNNER_TEMP:?RUNNER_TEMP is not set}"
# "[-]-path" matches the Godot argument "--path" without matching this line.
for pattern in "[-]-path ${temp}" "${temp}/.*godot-mcp"; do
  if pkill -9 -f "$pattern"; then
    echo "ended processes matching: $pattern"
  fi
done
exit 0
