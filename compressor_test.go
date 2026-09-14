package compression

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCompressClearsOldToolResultBeforeSummarizing(t *testing.T) {
	oldResult := strings.Repeat("result ", 100)
	messages := []Message{
		{ID: "s1", Role: RoleSystem, Content: "follow system instructions"},
		{ID: "u1", Role: RoleUser, Content: "find the file"},
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search", Arguments: `{"q":"file"}`}}, Content: ""},
		{ID: "r1", Role: RoleTool, ToolCallID: "c1", ToolName: "search", Content: oldResult},
		{ID: "u2", Role: RoleUser, Content: "show the result"},
	}

	counter := RuneBudgetCounter{}
	before := counter.CountMessages(messages)
	marker := toolResultClearedText
	budget := before - (len([]rune(oldResult)) - len([]rune(marker))) + 5
	result := NewMiddleware(counter, nil).Compress(context.Background(), Request{Messages: messages, ContextLimit: budget})

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	if !hasActionType(result.Actions, "clear_tool_result") {
		t.Fatalf("actions = %#v, missing clear_tool_result", result.Actions)
	}
	if got := messageByID(result.Messages, "r1").Content; got != marker {
		t.Fatalf("tool result content = %q, want marker", got)
	}
	if got := messageByID(result.Messages, "u2").Content; got != "show the result" {
		t.Fatalf("latest user was changed: %q", got)
	}
	assertValidOutput(t, result, messages, budget)
}

func TestCompressUsesOneSourcedSummaryForOldHistory(t *testing.T) {
	messages := []Message{
		{ID: "s1", Role: RoleSystem, Content: "system"},
		{ID: "u1", Role: RoleUser, Content: strings.Repeat("old request ", 30)},
		{ID: "a1", Role: RoleAssistant, Content: strings.Repeat("old response ", 30)},
		{ID: "u2", Role: RoleUser, Content: "current request"},
	}
	counter := RuneBudgetCounter{}
	budget := counter.CountMessages([]Message{messages[0], messages[3]}) + 100
	middleware := NewMiddleware(counter, FakeSummarizer{Content: "old goal and result"})
	result := middleware.Compress(context.Background(), Request{Messages: messages, ContextLimit: budget})

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	if got := countActionType(result.Actions, "summarize"); got != 1 {
		t.Fatalf("summary action count = %d, want 1; actions=%#v", got, result.Actions)
	}
	if got := countSyntheticMessages(result.Messages); got != 1 {
		t.Fatalf("synthetic message count = %d, want 1; messages=%#v", got, result.Messages)
	}
	summary := syntheticMessage(result.Messages)
	if !strings.Contains(summary.Content, "old goal and result") {
		t.Fatalf("summary content = %q", summary.Content)
	}
	if got, want := strings.Join(summary.SourceIDs, ","), "u1,a1"; got != want {
		t.Fatalf("summary sources = %q, want %q", got, want)
	}
	if got := messageByID(result.Messages, "u2").Content; got != "current request" {
		t.Fatalf("latest user was changed: %q", got)
	}
	assertValidOutput(t, result, messages, budget)
}

func TestCompressSummaryFailureUsesDeterministicFallback(t *testing.T) {
	messages := []Message{
		{ID: "u1", Role: RoleUser, Content: strings.Repeat("old history ", 100)},
		{ID: "a1", Role: RoleAssistant, Content: strings.Repeat("old answer ", 100)},
		{ID: "u2", Role: RoleUser, Content: "current request"},
	}
	counter := RuneBudgetCounter{}
	budget := counter.CountMessages([]Message{messages[2]}) + 80
	result := NewMiddleware(counter, FakeSummarizer{Mode: FakeSummaryError, Err: errors.New("provider down")}).Compress(
		context.Background(), Request{Messages: messages, ContextLimit: budget},
	)

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	if !hasDiagnosticCode(result.Diagnostics, "summarizer_error") || !hasDiagnosticCode(result.Diagnostics, "fallback_applied") {
		t.Fatalf("diagnostics = %#v, missing summary failure or fallback", result.Diagnostics)
	}
	content := messageByID(result.Messages, "u1").Content
	if !strings.Contains(content, "历史内容已截断") && !strings.Contains(content, "历史内容已省略") {
		t.Fatalf("u1 was not truncated: %q", messageByID(result.Messages, "u1").Content)
	}
	assertValidOutput(t, result, messages, budget)
}

func TestCompressSummaryRemovesWholeToolRoundWithoutOrphans(t *testing.T) {
	messages := []Message{
		{ID: "a1", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "search", Arguments: strings.Repeat("x", 100)}}},
		{ID: "r1", Role: RoleTool, ToolCallID: "c1", ToolName: "search", Content: strings.Repeat("result ", 100)},
		{ID: "u1", Role: RoleUser, Content: "continue"},
	}
	counter := RuneBudgetCounter{}
	budget := counter.CountMessages([]Message{messages[2]}) + 100
	result := NewMiddleware(counter, FakeSummarizer{Content: "tool call completed"}).Compress(
		context.Background(), Request{Messages: messages, ContextLimit: budget},
	)

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	for _, message := range result.Messages {
		if message.Role == RoleTool || len(message.ToolCalls) > 0 {
			t.Fatalf("tool round was not removed atomically: %#v", result.Messages)
		}
	}
	assertValidOutput(t, result, messages, budget)
}

func TestCompressReturnsCannotFitWhenOnlyProtectedContentRemains(t *testing.T) {
	messages := []Message{
		{ID: "s1", Role: RoleSystem, Content: strings.Repeat("system rule ", 50)},
		{ID: "u1", Role: RoleUser, Content: strings.Repeat("current request ", 50)},
	}
	result := NewMiddleware(nil, nil).Compress(context.Background(), Request{Messages: messages, ContextLimit: 20})

	if result.Status != CannotFit {
		t.Fatalf("Status = %q, want cannot_fit; reason=%s", result.Status, result.FailureReason)
	}
	if result.AfterTokens <= 20 {
		t.Fatalf("AfterTokens = %d, want over budget", result.AfterTokens)
	}
	if !sameMessages(result.Messages, messages) {
		t.Fatalf("protected messages were changed: %#v", result.Messages)
	}
}

func assertValidOutput(t *testing.T, result Result, original []Message, budget int) {
	t.Helper()
	if result.AfterTokens > budget {
		t.Fatalf("AfterTokens = %d, budget = %d", result.AfterTokens, budget)
	}
	if validation := validateOutput(result.Messages, original); !validation.valid {
		t.Fatalf("output self-validation failed: %s", validation.reason)
	}
}

func messageByID(messages []Message, id string) Message {
	for _, message := range messages {
		if message.ID == id {
			return message
		}
	}
	return Message{}
}

func syntheticMessage(messages []Message) Message {
	for _, message := range messages {
		if message.Synthetic {
			return message
		}
	}
	return Message{}
}

func countSyntheticMessages(messages []Message) int {
	count := 0
	for _, message := range messages {
		if message.Synthetic {
			count++
		}
	}
	return count
}

func hasActionType(actions []Action, actionType string) bool {
	return countActionType(actions, actionType) > 0
}

func countActionType(actions []Action, actionType string) int {
	count := 0
	for _, action := range actions {
		if action.Type == actionType {
			count++
		}
	}
	return count
}

func sameMessages(left, right []Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].ID != right[index].ID || left[index].Role != right[index].Role || left[index].Content != right[index].Content {
			return false
		}
	}
	return true
}
