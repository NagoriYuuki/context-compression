package compression

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
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
	messages = append(messages, recentToolRound()...)

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
	for _, message := range recentToolRound() {
		if !reflect.DeepEqual(messageByID(result.Messages, message.ID), message) {
			t.Fatalf("recent ToolRound changed: %#v", result.Messages)
		}
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
	messages = append(messages, recentToolRound()...)
	counter := RuneBudgetCounter{}
	budget := counter.CountMessages(messages[2:]) + 100
	result := NewMiddleware(counter, FakeSummarizer{Content: "tool call completed"}).Compress(
		context.Background(), Request{Messages: messages, ContextLimit: budget},
	)

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	if messageByID(result.Messages, "a1").ID != "" || messageByID(result.Messages, "r1").ID != "" {
		t.Fatalf("old tool round was not removed atomically: %#v", result.Messages)
	}
	for _, message := range recentToolRound() {
		if !reflect.DeepEqual(messageByID(result.Messages, message.ID), message) {
			t.Fatalf("recent ToolRound changed: %#v", result.Messages)
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
	if len(result.Actions) != 0 || !hasDiagnosticCode(result.Diagnostics, "fallback_no_change") || hasDiagnosticCode(result.Diagnostics, "fallback_applied") {
		t.Fatalf("unchanged protected content must not report an applied fallback: %#v", result)
	}
}

func TestCompressKeepsLatestUserTaggedLogAfterOmittingOldLog(t *testing.T) {
	messages := []Message{
		{ID: "s1", Role: RoleSystem, Content: "system"},
		{ID: "old", Role: RoleUser, Content: strings.Repeat("old log ", 100), Tags: []string{"log"}},
		{ID: "latest", Role: RoleUser, Content: strings.Repeat("current request ", 50), Tags: []string{"log"}},
	}
	result := NewMiddleware(nil, nil).Compress(context.Background(), Request{Messages: messages, ContextLimit: 100})
	if result.Status != CannotFit || result.AfterTokens <= 100 || result.FailureReason == "" {
		t.Fatalf("expected explicit over-budget result: %#v", result)
	}
	if !reflect.DeepEqual(messageByID(result.Messages, "latest"), messages[2]) {
		t.Fatalf("latest request changed or removed: %#v", result.Messages)
	}
	if messageByID(result.Messages, "old").ID != "" {
		t.Fatal("old log should have been omitted before returning CannotFit")
	}
	for _, action := range result.Actions {
		if action.UnitID == "latest" {
			t.Fatalf("latest request must not be a fallback target: %#v", action)
		}
	}
}

func TestCompressPreservesLatestCompletedToolRound(t *testing.T) {
	for _, tail := range []string{"unread_result", "with_followup", "with_incomplete_round"} {
		t.Run(tail, func(t *testing.T) {
			messages := []Message{
				{ID: "system", Role: RoleSystem, Content: "只读排查。"},
				{ID: "current", Role: RoleUser, Content: "读取连接池配置后，解释超时原因。"},
				{ID: "call", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "read_config", Arguments: `{"path":"pool.conf"}`}}},
				{ID: "result", Role: RoleTool, ToolCallID: "c", ToolName: "read_config", Content: strings.Repeat("max_connections=8; timeout_ms=3000;\n", 50)},
			}
			if tail == "with_followup" {
				messages = append(messages, Message{ID: "followup", Role: RoleAssistant, Content: "已读取配置，下一步核对并发。"})
			}
			if tail == "with_incomplete_round" {
				messages = append(messages, Message{ID: "pending-call", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "pending", Name: "read_logs", Arguments: `{}`}}})
			}
			original := CloneMessages(messages)
			spy := &demoSummarizer{fake: FakeSummarizer{Content: "不能替代当前工具结果。"}}
			result := NewMiddleware(nil, spy).Compress(context.Background(), Request{Messages: messages, ContextLimit: 200})
			if result.Status != CannotFit || result.AfterTokens <= 200 || result.FailureReason == "" {
				t.Fatalf("necessary ToolRound should remain over budget: %#v", result)
			}
			if !reflect.DeepEqual(result.Messages, original) || !reflect.DeepEqual(messages, original) {
				t.Fatalf("current ToolRound or input changed: %#v", result.Messages)
			}
			if spy.calls != 0 || len(result.Actions) != 0 {
				t.Fatalf("protected context must not enter clearing, summary or fallback: calls=%d actions=%v", spy.calls, result.Actions)
			}
		})
	}
}

func TestCompressLabelsJSONReplacementAsText(t *testing.T) {
	for _, toolResult := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool_result=%t", toolResult), func(t *testing.T) {
			history := Message{ID: "history", Role: RoleAssistant, ContentType: "json", Content: `{"logs":"` + strings.Repeat("entry ", 200) + `"}`}
			messages := []Message{{ID: "s1", Role: RoleSystem, Content: "system"}}
			if toolResult {
				messages = append(messages, Message{ID: "call", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: `{}`}}})
				history.Role, history.ToolCallID, history.ToolName = RoleTool, "c1", "read"
			}
			messages = append(messages, history, Message{ID: "latest", Role: RoleUser, Content: "continue"})
			budget := 150
			if toolResult {
				messages = append(messages, recentToolRound()...)
				budget += (RuneBudgetCounter{}).CountMessages(recentToolRound())
			}
			before := CloneMessages(messages)
			result := NewMiddleware(nil, FakeSummarizer{Mode: FakeSummaryError}).Compress(context.Background(), Request{Messages: messages, ContextLimit: budget})
			got := messageByID(result.Messages, "history")
			if result.Status != Degraded || got.Content == history.Content || got.ContentType != "text" {
				t.Fatalf("expected a text replacement that fits: result=%#v history=%#v", result, got)
			}
			if !reflect.DeepEqual(messages, before) {
				t.Fatal("Compress changed caller input")
			}
		})
	}
}

func recentToolRound() []Message {
	return []Message{
		{ID: "a-recent", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c-recent", Name: "read_recent", Arguments: `{}`}}},
		{ID: "r-recent", Role: RoleTool, ToolCallID: "c-recent", ToolName: "read_recent", Content: "最近结果，须保留。"},
	}
}

func TestCompressReusesPreviousSummary(t *testing.T) {
	first := NewMiddleware(nil, FakeSummarizer{Content: longSessionSummary}).Compress(
		context.Background(), Request{Messages: loadLongSession(t), ContextLimit: 900},
	)
	if first.Status != Degraded {
		t.Fatalf("initial compression failed: %#v", first)
	}
	oldSummary := syntheticMessage(first.Messages)
	for _, stage := range []string{"clear_tool_result", "summarize", "fallback"} {
		t.Run(stage, func(t *testing.T) {
			messages := CloneMessages(first.Messages)
			budget := 780
			fake := FakeSummarizer{Content: "旧日志已核查，结论另存。"}
			if stage == "clear_tool_result" {
				messages = append(messages,
					Message{ID: "u-next", Role: RoleUser, Content: "检查下一段离线日志。"},
					Message{ID: "a-next", Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c-next", Name: "read_logs", Arguments: `{"path":"next.log"}`}}},
					Message{ID: "r-next", Role: RoleTool, ToolCallID: "c-next", ToolName: "read_logs", Content: strings.Repeat("ordinary access log line\n", 100)},
					Message{ID: "a-next-followup", Role: RoleAssistant, Content: "已检查这段日志，没有新增关键事实。"},
					Message{ID: "u-final", Role: RoleUser, Content: "继续保留已有结论。"},
				)
				messages = append(messages, recentToolRound()...)
				budget = 1100
			}
			if stage == "fallback" {
				fake.Mode = FakeSummaryError
			}
			original := CloneMessages(messages)
			spy := &demoSummarizer{fake: fake}
			result := NewMiddleware(nil, spy).Compress(context.Background(), Request{Messages: messages, ContextLimit: budget})
			if result.Status != Degraded || result.AfterTokens > budget || result.FailureReason != "" {
				t.Fatalf("previous output could not be reused: %#v", result)
			}
			if !reflect.DeepEqual(messages, original) {
				t.Fatal("Compress mutated its input")
			}
			for _, evidence := range continuationEvidence {
				if !reflect.DeepEqual(messageByID(result.Messages, evidence.id), messageByID(original, evidence.id)) {
					t.Fatalf("protected task information changed: %s", evidence.id)
				}
			}
			if stage == "clear_tool_result" {
				if spy.calls != 0 || !hasActionType(result.Actions, stage) || !reflect.DeepEqual(messageByID(result.Messages, oldSummary.ID), oldSummary) {
					t.Fatal("tool clearing must preserve the existing summary, including its historical sources")
				}
			} else if stage == "summarize" {
				summary := syntheticMessage(result.Messages)
				if spy.calls != 1 || !hasActionType(result.Actions, stage) || messageByID(result.Messages, oldSummary.ID).ID != "" || !reflect.DeepEqual(summary.SourceIDs, []string{oldSummary.ID}) || summary.Role != RoleAssistant {
					t.Fatalf("new summary must reference the immediate input summary: %#v", summary)
				}
			} else {
				summary := messageByID(result.Messages, oldSummary.ID)
				if spy.calls != 1 || !hasActionType(result.Actions, "truncate") || !hasDiagnosticCode(result.Diagnostics, "summarizer_error") || summary.Content == oldSummary.Content || !reflect.DeepEqual(summary.SourceIDs, oldSummary.SourceIDs) || summary.Role != RoleAssistant {
					t.Fatalf("fallback must retain the existing summary's role and sources: %#v", summary)
				}
			}
		})
	}
}

type testTokenCounter func([]Message) int

func (f testTokenCounter) CountMessages(messages []Message) int { return f(messages) }

func TestCompressValidatesAgainstUnmodifiedInput(t *testing.T) {
	messages := []Message{
		{ID: "system", Role: RoleSystem, Content: "original system"},
		{ID: "old", Role: RoleUser, Content: strings.Repeat("old history ", 100)},
		{ID: "current", Role: RoleUser, Content: "continue"},
	}
	original := CloneMessages(messages)
	// Deliberately corrupt a working message through an injected dependency.
	// The final validation must compare it to the untouched caller input.
	counter := testTokenCounter(func(working []Message) int {
		for i := range working {
			if working[i].Role == RoleSystem {
				working[i].Content = "corrupted system"
			}
		}
		return (RuneBudgetCounter{}).CountMessages(working)
	})
	result := NewMiddleware(counter, nil).Compress(context.Background(), Request{Messages: messages, ContextLimit: 100})
	if !reflect.DeepEqual(messages, original) {
		t.Fatal("caller input changed")
	}
	if result.Status != CannotFit || result.AfterTokens > 100 || !strings.Contains(result.FailureReason, "System/Developer") {
		t.Fatalf("final validation missed a changed System message: %#v", result)
	}
}

func TestCompressSummaryFailureModesConverge(t *testing.T) {
	for _, tc := range []struct {
		name, diagnostic string
		mode             FakeSummaryMode
		acceptedSummary  bool
	}{
		{"error", "summarizer_error", FakeSummaryError, false},
		{"empty", "empty_summary", FakeSummaryEmpty, false},
		{"bad_source", "invalid_summary_sources", FakeSummaryBadSource, false},
		{"larger_than_input", "summary_not_smaller", FakeSummaryLarge, false},
		{"deadline", "summarizer_canceled", FakeSummaryWait, false},
		{"canceled", "summarizer_canceled", FakeSummaryWait, false},
		{"smaller_but_over_budget", "fallback_applied", FakeSummarySuccess, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if tc.name == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			if tc.name == "canceled" {
				cancel()
			}
			fake := FakeSummarizer{Mode: tc.mode, Content: "历史已处理。"}
			if tc.name == "larger_than_input" {
				fake.Content = strings.Repeat("oversized summary ", 500)
			}
			if tc.acceptedSummary {
				fake.Content = strings.Repeat("已完成历史步骤，等待下一步。", 20)
			}
			spy := &demoSummarizer{fake: fake}
			messages := []Message{
				{ID: "system", Role: RoleSystem, Content: "不能修改生产数据。"},
				{ID: "history", Role: RoleUser, Content: strings.Repeat("old history ", 250)},
				{ID: "latest", Role: RoleUser, Content: "继续当前任务。"},
			}
			original := CloneMessages(messages)
			request := Request{Messages: messages, ContextLimit: 380, OutputReserve: 200, SafetyMargin: 100}
			result := NewMiddleware(nil, spy).Compress(ctx, request)
			if result.Status != Degraded || result.AfterTokens > 80 || result.FailureReason != "" {
				t.Fatalf("fallback did not fit: status=%s tokens=%d reason=%q", result.Status, result.AfterTokens, result.FailureReason)
			}
			if spy.calls != 1 || !hasDiagnosticCode(result.Diagnostics, tc.diagnostic) || !hasDiagnosticCode(result.Diagnostics, "fallback_applied") || !hasActionType(result.Actions, "truncate") {
				t.Fatalf("expected one summary attempt then fallback: calls=%d actions=%v diagnostics=%v", spy.calls, result.Actions, result.Diagnostics)
			}
			if hasActionType(result.Actions, "summarize") != tc.acceptedSummary {
				t.Fatalf("unexpected summary acceptance: %#v", result.Actions)
			}
			if tc.acceptedSummary {
				summary := syntheticMessage(result.Messages)
				if summary.Role != RoleAssistant || !reflect.DeepEqual(summary.SourceIDs, []string{"history"}) {
					t.Fatalf("fallback changed summary role or provenance: %#v", summary)
				}
			} else if countSyntheticMessages(result.Messages) != 0 {
				t.Fatal("rejected provider output must not become a summary")
			}
			if !reflect.DeepEqual(messages, original) || !reflect.DeepEqual(messageByID(result.Messages, "system"), original[0]) || !reflect.DeepEqual(messageByID(result.Messages, "latest"), original[2]) {
				t.Fatal("fallback modified caller input or protected context")
			}
		})
	}
}

// A stuck provider must not block Compress, even when the caller supplies a
// context without a deadline. The caller's own cancellation still wins.
func TestCompressBoundsSummarizerWithInternalTimeout(t *testing.T) {
	for _, tc := range []struct {
		name       string
		timeout    time.Duration
		callerCtx  func() (context.Context, context.CancelFunc)
		diagnostic string
	}{
		{
			"internal_timeout_without_caller_deadline",
			20 * time.Millisecond,
			func() (context.Context, context.CancelFunc) { return context.Background(), func() {} },
			"summarizer_timeout",
		},
		{
			"caller_cancellation_wins_over_internal_timeout",
			time.Hour,
			func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			"summarizer_canceled",
		},
		{
			"disabled_internal_timeout_defers_to_caller",
			0,
			func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 20*time.Millisecond)
			},
			"summarizer_canceled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.callerCtx()
			defer cancel()
			messages := []Message{
				{ID: "system", Role: RoleSystem, Content: "不能修改生产数据。"},
				{ID: "history", Role: RoleUser, Content: strings.Repeat("old history ", 250)},
				{ID: "latest", Role: RoleUser, Content: "继续当前任务。"},
			}
			original := CloneMessages(messages)
			spy := &demoSummarizer{fake: FakeSummarizer{Mode: FakeSummaryWait}}
			middleware := NewMiddleware(nil, spy)
			middleware.SummarizeTimeout = tc.timeout

			start := time.Now()
			result := middleware.Compress(ctx, Request{Messages: messages, ContextLimit: 380, OutputReserve: 200, SafetyMargin: 100})
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("Compress blocked on a stuck summarizer for %s", elapsed)
			}

			if result.Status != Degraded || result.AfterTokens > 80 || result.FailureReason != "" {
				t.Fatalf("a stuck summarizer must still degrade within budget: %#v", result)
			}
			if spy.calls != 1 || !hasDiagnosticCode(result.Diagnostics, tc.diagnostic) || !hasActionType(result.Actions, "truncate") {
				t.Fatalf("want one bounded attempt then fallback with %s: calls=%d actions=%v diagnostics=%v", tc.diagnostic, spy.calls, result.Actions, result.Diagnostics)
			}
			if countSyntheticMessages(result.Messages) != 0 {
				t.Fatal("an abandoned summary must not reach the output")
			}
			if !reflect.DeepEqual(messages, original) ||
				!reflect.DeepEqual(messageByID(result.Messages, "system"), original[0]) ||
				!reflect.DeepEqual(messageByID(result.Messages, "latest"), original[2]) {
				t.Fatal("the timeout path modified caller input or protected context")
			}
		})
	}
}

func TestNewMiddlewareAppliesDefaultSummarizeTimeout(t *testing.T) {
	if got := NewMiddleware(nil, nil).SummarizeTimeout; got != DefaultSummarizeTimeout {
		t.Fatalf("SummarizeTimeout = %s, want %s", got, DefaultSummarizeTimeout)
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
