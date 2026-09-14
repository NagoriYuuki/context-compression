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

func hasDiagnosticCode(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
