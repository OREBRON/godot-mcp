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
	// The location ends the "at:" line, and file names may hold "(" and ":",
	// so match up to the last ":<line>)".
	resLoc  = regexp.MustCompile(`\((res://.+):(\d+)\)\s*$`)
	resPath = regexp.MustCompile(`res://[^\s"']+`)
	ansi    = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")
)

// trimPath drops sentence punctuation that follows a path in a message, as
// in "Failed loading resource: res://a.gd.", and a closing parenthesis that
// has no opening one in the path.
func trimPath(p string) string {
	p = strings.TrimRight(p, ".,;:")
	if strings.HasSuffix(p, ")") && strings.Count(p, "(") < strings.Count(p, ")") {
		p = strings.TrimSuffix(p, ")")
	}
	return p
}

// CleanLine removes colour codes and trailing whitespace.
func CleanLine(s string) string {
	return strings.TrimRight(ansi.ReplaceAllString(s, ""), " \t\r")
}

// ParseIssues finds error and warning blocks in Godot output lines.
func ParseIssues(lines []string) []Issue {
	var out []Issue
	for i := 0; i < len(lines); i++ {
		is, ok := issueStart(CleanLine(lines[i]))
		if !ok {
			continue
		}
		// The location is on the next "at:" line; backtrace lines follow it.
		if i+1 < len(lines) {
			if where, ok := strings.CutPrefix(strings.TrimSpace(CleanLine(lines[i+1])), "at:"); ok {
				is.Where = strings.TrimSpace(where)
				i++
			}
		}
		is.findFile()
		out = append(out, is)
	}
	return out
}

// issueStart recognises the first line of an error or warning block.
func issueStart(line string) (Issue, bool) {
	for _, p := range issuePrefixes {
		if msg, ok := strings.CutPrefix(line, p.prefix); ok {
			return Issue{Severity: p.severity, Kind: strings.TrimSuffix(p.prefix, ": "), Message: strings.TrimSpace(msg)}, true
		}
	}
	return Issue{}, false
}

// findFile takes the res:// file and line from the location, or failing
// that a res:// path named in the message.
func (is *Issue) findFile() {
	if m := resLoc.FindStringSubmatch(is.Where); m != nil {
		is.File = m[1]
		is.Line, _ = strconv.Atoi(m[2])
	} else if m := resPath.FindString(is.Message); m != "" {
		is.File = trimPath(m)
	}
}
