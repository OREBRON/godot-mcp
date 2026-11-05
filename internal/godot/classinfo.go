package godot

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/orebron/godot-mcp/internal/bridge"
	"github.com/orebron/godot-mcp/internal/project"
)

//go:embed class_info.gd
var classInfoScript []byte

// ClassInfo describes a built-in or class_name class as the project's Godot
// sees it: its inheritance chain and the properties, methods, signals,
// constants and enums it declares, or with inherited all it has. A non-empty
// filter keeps the members whose name contains it.
func (e Exec) ClassInfo(ctx context.Context, p *project.Project, class string, inherited bool, filter string) (json.RawMessage, error) {
	dir, runner, err := writeRunner(p, "class-", "class_info.gd", classInfoScript)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, _ := e.Command(ctx, "--headless", "--path", p.Dir, "--script", runner, "--", class, strconv.FormatBool(inherited), filter).CombinedOutput()
	if ctx.Err() != nil {
		return nil, errors.New("the class lookup did not finish within a minute; a script may run code at load time that never returns (static initialisers, @tool scripts)")
	}
	return readClassInfo(string(out))
}

// readClassInfo takes the answer from the printed result line, since Godot
// exits with 0 whatever happens. An answer holding an error becomes that error.
func readClassInfo(out string) (json.RawMessage, error) {
	line, found := resultLine(strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n"))
	if !found {
		return nil, fmt.Errorf("the class lookup did not report a result\nLast output:\n%s", tail(out, 20))
	}
	var res struct {
		Error *bridge.Error `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &res); err != nil {
		return nil, fmt.Errorf("the class lookup reported an unreadable result: %w", err)
	}
	if res.Error != nil {
		return nil, res.Error
	}
	return json.RawMessage(line), nil
}
