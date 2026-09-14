package compression

import "testing"

func TestRuneBudgetCounterCountsUnicodeAndToolFields(t *testing.T) {
	counter := RuneBudgetCounter{}
	messages := []Message{
		{
			ID:        "assistant-1",
			Role:      RoleAssistant,
			Content:   "你好",
			ToolCalls: []ToolCall{{ID: "c1", Name: "搜索", Arguments: `{"q":"go"}`}},
		},
		{ID: "tool-1", Role: RoleTool, Content: "结果", ToolCallID: "c1", ToolName: "搜索"},
	}

	// Message overhead: 2*4. Content: 2+2. Tool result metadata: 2+2.
	// Tool call: 4 + 2 + 2 + 10 runes for the JSON argument.
	// Total: 8 + 4 + 4 + 18 = 34.
	if got, want := counter.CountMessages(messages), 34; got != want {
		t.Fatalf("CountMessages() = %d, want %d", got, want)
	}
}

func TestFixedTokenCounterUsesIDsAndFallback(t *testing.T) {
	counter := FixedTokenCounter{
		Tokens: map[string]int{"m1": 10},
		Fallback: RuneBudgetCounter{
			MessageOverhead: 1,
			ToolOverhead:    1,
		},
	}
	messages := []Message{{ID: "m1", Content: "ignored"}, {ID: "m2", Content: "abc"}}

	if got, want := counter.CountMessages(messages), 14; got != want {
		t.Fatalf("CountMessages() = %d, want %d", got, want)
	}
}

func TestInputBudget(t *testing.T) {
	request := Request{ContextLimit: 100, OutputReserve: 20, SafetyMargin: 5}
	if got, want := mustInputBudget(t, request), 75; got != want {
		t.Fatalf("InputBudget() = %d, want %d", got, want)
	}
}

func TestInputBudgetRejectsInvalidValues(t *testing.T) {
	tests := []Request{
		{ContextLimit: 0},
		{ContextLimit: 10, OutputReserve: -1},
		{ContextLimit: 10, SafetyMargin: -1},
		{ContextLimit: 10, OutputReserve: 5, SafetyMargin: 5},
	}
	for _, request := range tests {
		if _, err := InputBudget(request); err == nil {
			t.Fatalf("InputBudget(%+v) returned nil error", request)
		}
	}
}

func mustInputBudget(t *testing.T, request Request) int {
	t.Helper()
	budget, err := InputBudget(request)
	if err != nil {
		t.Fatalf("InputBudget() returned error: %v", err)
	}
	return budget
}
