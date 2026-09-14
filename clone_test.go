package compression

import (
	"reflect"
	"testing"
)

func TestCloneMessagesPreservesValueAndSeparatesNestedSlices(t *testing.T) {
	input := []Message{
		{
			ID:          "assistant-1",
			Role:        RoleAssistant,
			Content:     "call a tool",
			ContentType: "text",
			ToolCalls: []ToolCall{{
				ID:        "call-1",
				Name:      "search",
				Arguments: `{"q":"context"}`,
			}},
			Protected: true,
			Tags:      []string{"goal", "decision"},
			Synthetic: true,
			SourceIDs: []string{"user-1", "assistant-0"},
		},
	}

	cloned := CloneMessages(input)
	if !reflect.DeepEqual(cloned, input) {
		t.Fatalf("clone differs from input:\nclone=%#v\ninput=%#v", cloned, input)
	}

	cloned[0].ToolCalls[0].Arguments = `{"q":"changed"}`
	cloned[0].Tags[0] = "changed"
	cloned[0].SourceIDs[0] = "changed"

	if input[0].ToolCalls[0].Arguments != `{"q":"context"}` {
		t.Fatalf("mutating cloned ToolCalls changed input")
	}
	if input[0].Tags[0] != "goal" {
		t.Fatalf("mutating cloned Tags changed input")
	}
	if input[0].SourceIDs[0] != "user-1" {
		t.Fatalf("mutating cloned SourceIDs changed input")
	}
}

func TestCloneMessagesPreservesNilSlices(t *testing.T) {
	if cloned := CloneMessages(nil); cloned != nil {
		t.Fatalf("nil input should produce nil output, got %#v", cloned)
	}

	input := []Message{{ID: "m1"}}
	cloned := CloneMessages(input)
	if cloned[0].ToolCalls != nil || cloned[0].Tags != nil || cloned[0].SourceIDs != nil {
		t.Fatalf("nil nested slices should remain nil: %#v", cloned[0])
	}
}
