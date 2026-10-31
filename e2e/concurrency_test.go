package e2e

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Two sessions switching scenes at the same time: every reply must describe
// the scene its own command worked on.
func TestConcurrentSessions(t *testing.T) {
	s := start(t, true)
	other, _ := s.connect()

	call := func(cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
		res, err := cs.CallTool(s.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Errorf("%s(%v): %v %+v", name, args, err, res)
			return nil
		}
		var m map[string]any
		json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &m)
		return m
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("res://race/level_%d.tscn", i)
			r := call(s.cs, "scene_create", map[string]any{"path": path, "root_type": "Node2D"})
			if r != nil && (r["scene"] != path || r["root"].(map[string]any)["name"] != fmt.Sprintf("Level%d", i)) {
				t.Errorf("scene_create %s answered %v", path, r)
			}
		}()
		go func() {
			defer wg.Done()
			r := call(other, "scene_open", map[string]any{"path": "res://demo/main.tscn"})
			if r != nil && r["scene"] != "res://demo/main.tscn" {
				t.Errorf("scene_open answered %v", r)
			}
			if r := call(other, "scene_tree", nil); r != nil && r["scene"] == "res://demo/main.tscn" {
				if r["tree"].(map[string]any)["name"] != "Main" {
					t.Errorf("scene_tree of main.tscn shows %v", r["tree"])
				}
			}
		}()
	}
	wg.Wait()
}
