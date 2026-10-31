#!/usr/bin/env bash
# Installs the official Godot editor on a Linux or macOS runner and exports
# GODOT_PATH. The download is checked against the release's SHA512-SUMS.txt
# and kept in the runner's tool cache, so a self-hosted runner downloads each
# version once.
#
# Environment:
#   GODOT_VERSION        required, e.g. 4.7.2
#   GODOT_PATH           optional: a Godot already installed on the runner
#                        (set it in the runner's .env file); skips the download
#   GODOT_DOWNLOAD_BASE  optional: a mirror of the release assets
#   RUNNER_OS, RUNNER_TOOL_CACHE, GITHUB_ENV   set by the runner
set -euo pipefail

if [ -n "${GODOT_PATH:-}" ] && [ -e "$GODOT_PATH" ]; then
  echo "Using the Godot installed on this runner: $GODOT_PATH"
  echo "GODOT_PATH=$GODOT_PATH" >> "$GITHUB_ENV"
  exit 0
fi

for tool in curl unzip; do
  command -v "$tool" >/dev/null || { echo "::error::$tool is required on the runner to install Godot" >&2; exit 1; }
done

version="${GODOT_VERSION:?GODOT_VERSION is not set}"
base="${GODOT_DOWNLOAD_BASE:-https://github.com/godotengine/godot/releases/download/${version}-stable}"
cache="${RUNNER_TOOL_CACHE:?RUNNER_TOOL_CACHE is not set}/godot"
dir="$cache/$version"
case "${RUNNER_OS:?RUNNER_OS is not set}" in
  Linux)
    asset="Godot_v${version}-stable_linux.x86_64.zip"
    bin="$dir/Godot_v${version}-stable_linux.x86_64"
    exe="$bin" ;;
  macOS)
    asset="Godot_v${version}-stable_macos.universal.zip" # runs on Intel and Apple Silicon
    bin="$dir/Godot.app"
    exe="$bin/Contents/MacOS/Godot" ;;
  *)
    echo "::error::install-godot.sh does not support $RUNNER_OS; use install-godot.ps1 on Windows" >&2
    exit 1 ;;
esac

if [ ! -e "$exe" ]; then
  mkdir -p "$cache"
  # Download next to the cache so the final move is an atomic rename.
  tmp="$(mktemp -d "$cache/download.XXXXXX")"
  trap 'rm -rf "$tmp"' EXIT
  echo "Downloading $asset from $base"
  curl -fsSL --retry 3 -o "$tmp/$asset" "$base/$asset"
  curl -fsSL --retry 3 -o "$tmp/SHA512-SUMS.txt" "$base/SHA512-SUMS.txt"
  want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA512-SUMS.txt")"
  if [ -z "$want" ]; then
    echo "::error::$asset is not listed in SHA512-SUMS.txt" >&2
    exit 1
  fi
  if command -v sha512sum >/dev/null; then
    got="$(sha512sum "$tmp/$asset" | awk '{ print $1 }')"
  else
    got="$(shasum -a 512 "$tmp/$asset" | awk '{ print $1 }')"
  fi
  if [ "$got" != "$want" ]; then
    echo "::error::SHA-512 of $asset does not match SHA512-SUMS.txt" >&2
    exit 1
  fi
  unzip -q "$tmp/$asset" -d "$tmp/out"
  # Another job on this machine may have installed it meanwhile.
  if [ ! -e "$exe" ]; then
    rm -rf "$dir"
    mv "$tmp/out" "$dir"
  fi
  chmod +x "$exe"
fi

echo "GODOT_PATH=$bin" >> "$GITHUB_ENV"
"$exe" --headless --version
