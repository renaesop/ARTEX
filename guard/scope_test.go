package guard

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

func TestMissingScopeReviewerBlocksEveryTool(t *testing.T) {
	g := New()
	loads := 0
	ctx := intercept.WithTaskConstraints(context.Background(), func() ([]db.Constraint, error) {
		loads++
		return []db.Constraint{{Kind: "deny", Text: "Do not access example.test"}}, nil
	})
	for _, tool := range []string{"Bash", "shell_send", "unlisted_custom_tool"} {
		blocked, _, _ := g.Hooks().PreToolUse(ctx, tool, json.RawMessage(`{"command":"curl http://192.0.2.1"}`))
		if !blocked {
			t.Fatalf("%s bypassed mandatory scope review", tool)
		}
	}
	if loads != 3 {
		t.Fatal("constraints were not refreshed on each action")
	}
	for _, entry := range g.Audit() {
		if entry.Action != "block" || !strings.Contains(entry.Reason, "origin IP") {
			t.Fatalf("misleading audit: %+v", entry)
		}
	}
}
