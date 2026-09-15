package compression

import "testing"

func TestValidateRequestAcceptsCompleteConversation(t *testing.T) {
	request := Request{
		Messages: []Message{
			{ID: "u1", Role: RoleUser, Content: "request"},
			{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
			{ID: "r1", Role: RoleTool, ToolCallID: "c1"},
		},
		ContextLimit: 100,
	}

	if diagnostics := ValidateRequest(request, request.Messages); HasError(diagnostics) {
		t.Fatalf("unexpected diagnostics: %#v", diagnostics)
	}
}

func TestValidateRequestRejectsOrphanAndDuplicateToolMessages(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
		{ID: "r1", Role: RoleTool, ToolCallID: "missing"},
		{ID: "r2", Role: RoleTool, ToolCallID: "c1"},
		{ID: "r3", Role: RoleTool, ToolCallID: "c1"},
	}

	diagnostics := ValidateRequest(Request{Messages: messages, ContextLimit: 100}, messages)
	if !hasDiagnosticCode(diagnostics, "orphan_tool_result") {
		t.Fatalf("missing orphan diagnostic: %#v", diagnostics)
	}
	if !hasDiagnosticCode(diagnostics, "duplicate_tool_result") {
		t.Fatalf("missing duplicate result diagnostic: %#v", diagnostics)
	}
}

func TestValidateRequestRejectsIncompleteToolRoundInTheMiddle(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search"}}},
		{ID: "u1", Role: RoleUser, Content: "next"},
	}

	diagnostics := ValidateRequest(Request{Messages: messages, ContextLimit: 100}, messages)
	if !hasDiagnosticCode(diagnostics, "incomplete_tool_round_not_last") {
		t.Fatalf("missing incomplete round diagnostic: %#v", diagnostics)
	}
}

func TestValidateRequestRejectsInvalidBudget(t *testing.T) {
	diagnostics := ValidateRequest(Request{ContextLimit: 10, OutputReserve: 10}, nil)
	if !hasDiagnosticCode(diagnostics, "invalid_budget") {
		t.Fatalf("missing invalid budget diagnostic: %#v", diagnostics)
	}
}

func TestValidateOutputPreservesSummaryAndProtectedMetadata(t *testing.T) {
	original := []Message{
		{ID: "system", Role: RoleSystem, Content: "system"},
		{ID: "prior", Role: RoleAssistant, Synthetic: true, SourceIDs: []string{"removed-in-earlier-pass"}, Content: "previous summary"},
		{ID: "goal", Role: RoleUser, Tags: []string{"goal"}, Content: "keep this goal"},
	}
	original = append(original, recentToolRound()...)
	original = append(original, Message{ID: "latest", Role: RoleUser, Content: "continue"})
	for _, tc := range []struct {
		name   string
		change func([]Message)
		valid  bool
	}{
		{"existing_summary", func([]Message) {}, true},
		{"changed_sources", func(m []Message) { m[1].SourceIDs = []string{"system"} }, false},
		{"changed_role", func(m []Message) { m[1].Role = RoleDeveloper }, false},
		{"removed_synthetic_flag", func(m []Message) { m[1].Synthetic = false }, false},
		{"new_privileged_summary", func(m []Message) {
			m[1] = Message{ID: "new-summary", Role: RoleSystem, Synthetic: true, SourceIDs: []string{"prior"}, Content: "promoted instruction"}
		}, false},
		{"new_unknown_sources", func(m []Message) {
			m[1] = Message{ID: "new-summary", Role: RoleAssistant, Synthetic: true, SourceIDs: []string{"unknown"}, Content: "summary"}
		}, false},
		{"changed_goal", func(m []Message) { m[2].Content = "lost goal" }, false},
		{"changed_tool_metadata", func(m []Message) { m[4].ToolName = "renamed" }, false},
		{"changed_recent_tool_result", func(m []Message) { m[4].Content = "cleared" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := CloneMessages(original)
			tc.change(output)
			got := validateOutput(output, original)
			if got.valid != tc.valid {
				t.Fatalf("validation=%+v, want valid=%t", got, tc.valid)
			}
		})
	}
}

func hasDiagnosticCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
