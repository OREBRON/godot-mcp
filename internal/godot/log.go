package godot

import (
	"regexp"
	"strconv"
	"strings"
)

// Issue is one error or warning block from Godot's output, for example
//
//	SCRIPT ERROR: Parse Error: Function "foo()" not found in base self.
//	          at: GDScript::reload (res://player.gd:12)
type Issue struct {
	Severity string `json:"severity"` // "error" or "warning"
	Kind     string `json:"kind"`     // the prefix: SCRIPT ERROR, ERROR, WARNING, ...
	Message  string `json:"message"`
	File     string `json:"file,omitempty"` // res:// path when Godot names one
	Line     int    `json:"line,omitempty"`
	Where    string `json:"where,omitempty"` // the raw "at:" location
}

var (
	issuePrefixes = []struct{ prefix, severity string }{
		{"USER SCRIPT ERROR: ", "error"},
		{"SCRIPT ERROR: ", "error"},
		{"USER ERROR: ", "error"},
		{"ERROR: ", "error"},
		{"USER WARNING: ", "warning"},
		{"SCRIPT WARNING: ", "warning"},
		{"WARNING: ", "warning"},
	}
	resLoc  = regexp.MustCompile(`\((res://[^():]+):(\d+)\)`)
	resPath = regexp.MustCompile(`res://[^\s"'(),]+`)
	ansi    = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
)

// CleanLine removes colour codes and trailing whitespace.
func CleanLine(s string) string {
	return strings.TrimRight(ansi.ReplaceAllString(s, ""), " \t\r")
}

// ParseIssues finds error and warning blocks in Godot output lines.
func ParseIssues(lines []string) []Issue {
	var out []Issue
	for i := 0; i < len(lines); i++ {
		line := CleanLine(lines[i])
		var is *Issue
		for _, p := range issuePrefixes {
			if strings.HasPrefix(line, p.prefix) {
				is = &Issue{
					Severity: p.severity,
					Kind:     strings.TrimSuffix(p.prefix, ": "),
					Message:  strings.TrimSpace(line[len(p.prefix):]),
				}
				break
			}
		}
		if is == nil {
			continue
		}
		// The location is on the next "at:" line; backtrace lines follow it.
		if i+1 < len(lines) {
			next := strings.TrimSpace(CleanLine(lines[i+1]))
			if strings.HasPrefix(next, "at:") {
				is.Where = strings.TrimSpace(strings.TrimPrefix(next, "at:"))
				i++
			}
		}
		if m := resLoc.FindStringSubmatch(is.Where); m != nil {
			is.File = m[1]
			is.Line, _ = strconv.Atoi(m[2])
		} else if m := resPath.FindString(is.Message); m != "" {
			is.File = m
		}
		out = append(out, *is)
	}
	return out
}
