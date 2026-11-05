package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func toolNames(t *testing.T, groups []string) []string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := New(Options{Tools: groups}).MCP().Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestToolGroups(t *testing.T) {
	if all := toolNames(t, nil); len(all) != 32 {
		t.Errorf("no groups gave %d tools, want all 32", len(all))
	}
	some := toolNames(t, []string{"project", "game"})
	for _, n := range some {
		if !strings.HasPrefix(n, "game_") && !slices.Contains([]string{"project_info", "script_check", "test_run", "export_build", "class_info"}, n) {
			t.Errorf("project,game includes %s", n)
		}
	}
	if len(some) != 16 {
		t.Errorf("project,game gave %d tools, want 16: %v", len(some), some)
	}
	if err := CheckToolGroups([]string{"game", "nodes"}); err == nil || !strings.Contains(err.Error(), `"nodes"`) {
		t.Errorf("CheckToolGroups(nodes) = %v", err)
	}
}
