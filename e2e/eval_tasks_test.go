package e2e

import (
	"os"
	"path/filepath"
	"strings"
)

// evalTasks are the tasks TestAgentEval gives the agent, in a user's words.
// The prompts never name a tool: finding the right one is part of the task.
var evalTasks = []evalTask{
	{
		name:   "find_dependents",
		prompt: "I'm thinking about deleting res://demo/player.tscn. Which scenes would break if I did? Don't delete anything, just tell me.",
		verify: verifyDependents,
	},
}

func verifyDependents(s *session, answer string) []string {
	var problems []string
	if !strings.Contains(answer, "main.tscn") {
		problems = append(problems, "the answer does not name main.tscn")
	}
	if _, err := os.Stat(filepath.Join(s.proj, "demo", "player.tscn")); err != nil {
		problems = append(problems, "player.tscn is gone: "+err.Error())
	}
	return problems
}
