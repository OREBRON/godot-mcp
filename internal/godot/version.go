package godot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Version is a parsed Godot version such as 4.7.2.stable.official.ed1daf0bf.
type Version struct {
	Raw   string `json:"raw"`
	Major int    `json:"major"`
	Minor int    `json:"minor"`
	Patch int    `json:"patch"`
}

// MinMajor and MinMinor are the oldest Godot the plugin is written for.
const (
	MinMajor = 4
	MinMinor = 4
)

// AtLeast reports whether v is major.minor or newer.
func (v Version) AtLeast(major, minor int) bool {
	return v.Major > major || (v.Major == major && v.Minor >= minor)
}

// ParseVersion reads the output of `godot --version`.
func ParseVersion(out string) (Version, error) {
	// The version is the last non-empty line; Flatpak may print notices first.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	raw := strings.TrimSpace(lines[len(lines)-1])
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return Version{}, fmt.Errorf("unexpected godot --version output %q", raw)
	}
	v := Version{Raw: raw}
	var err error
	if v.Major, err = strconv.Atoi(parts[0]); err != nil {
		return Version{}, fmt.Errorf("unexpected godot --version output %q", raw)
	}
	if v.Minor, err = strconv.Atoi(parts[1]); err != nil {
		return Version{}, fmt.Errorf("unexpected godot --version output %q", raw)
	}
	if len(parts) > 2 {
		v.Patch, _ = strconv.Atoi(parts[2]) // "stable" when there is no patch number
	}
	return v, nil
}

// GetVersion runs `godot --version`.
func (e Exec) GetVersion(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := e.Command(ctx, "--version").Output()
	if err != nil {
		return Version{}, fmt.Errorf("running %s --version: %w", e, err)
	}
	return ParseVersion(string(out))
}
