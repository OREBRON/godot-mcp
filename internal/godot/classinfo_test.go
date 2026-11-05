package godot

import (
	"errors"
	"strings"
	"testing"

	"github.com/orebron/godot-mcp/internal/bridge"
)

func TestReadClassInfo(t *testing.T) {
	answer := `{"class":"DemoPlayer","inherits":["CharacterBody2D"]}`
	got, err := readClassInfo("Godot Engine v4.7.2\r\nGODOT_MCP_RESULT " + answer + "\r\n")
	if err != nil || string(got) != answer {
		t.Errorf("answer: got %s, %v", got, err)
	}

	_, err = readClassInfo(`GODOT_MCP_RESULT {"error":{"code":"CLASS_NOT_FOUND","message":"there is no class named \"Nope\"","hint":"did you mean: Node"}}`)
	var be *bridge.Error
	if !errors.As(err, &be) || be.Code != "CLASS_NOT_FOUND" || be.Hint != "did you mean: Node" {
		t.Errorf("error answer: got %v", err)
	}

	// A script that hangs or crashes before reporting leaves no result line.
	if _, err := readClassInfo("SCRIPT ERROR: boom\n"); err == nil || !strings.Contains(err.Error(), "SCRIPT ERROR: boom") {
		t.Errorf("no result: got %v", err)
	}
}
