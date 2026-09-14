package compression

import "testing"

func TestGroupMessagesKeepsSystemBlockAndCompleteToolRound(t *testing.T) {
	messages := []Message{
		{ID: "s1", Role: RoleSystem, Content: "system"},
		{ID: "d1", Role: RoleDeveloper, Content: "developer"},
		{ID: "u1", Role: RoleUser, Content: "request"},
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search"}, {ID: "c2", Name: "read"}}},
		{ID: "r2", Role: RoleTool, ToolCallID: "c2", ToolName: "read"},
		{ID: "r1", Role: RoleTool, ToolCallID: "c1", ToolName: "search"},
		{ID: "a2", Role: RoleAssistant, Content: "done"},
		{ID: "u2", Role: RoleUser, Content: "next"},
	}

	units := GroupMessages(messages)
	if got, want := len(units), 4; got != want {
		t.Fatalf("len(units) = %d, want %d", got, want)
	}
	if units[0].Kind != UnitSystem || len(units[0].Messages) != 2 {
		t.Fatalf("system block = %#v", units[0])
	}
	if units[2].Kind != UnitToolRound || units[2].Status != UnitComplete {
		t.Fatalf("tool round = %#v", units[2])
	}
	if got, want := len(units[2].Messages), 4; got != want {
		t.Fatalf("tool round message count = %d, want %d", got, want)
	}
}

func TestGroupMessagesMarksIncompleteRound(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
	}

	units := GroupMessages(messages)
	if len(units) != 1 || units[0].Status != UnitIncomplete {
		t.Fatalf("units = %#v, want one incomplete unit", units)
	}
}
