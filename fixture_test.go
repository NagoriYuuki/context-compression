package compression

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLongSessionFixturePreservesContinuationState(t *testing.T) {
	messages := loadLongSession(t)
	original := loadLongSession(t) // Independent snapshot; do not use the production clone helper as the oracle.
	result := NewMiddleware(nil, FakeSummarizer{Content: longSessionSummary}).Compress(
		context.Background(), Request{Messages: messages, ContextLimit: 900},
	)
	if result.Status != Degraded || result.AfterTokens > 900 || !hasActionType(result.Actions, "summarize") {
		t.Fatalf("expected sourced summary within budget: %#v", result)
	}
	assertLongSessionEvidence(t, messages, original, result)
}

// This is a hand-written Fake response, based on the unprotected history in the fixture.
const longSessionSummary = "已读取 10:00–10:05 的离线调用日志，并比较成功请求与超时请求。超时请求出现连接池等待，关键事实另存；根因仍待验证。"

var continuationEvidence = []struct {
	label, id, text string
}{
	{"系统指令", "s1", "不把推测当作事实"},
	{"目标", "u-goal", "定位订单 API 超时原因"},
	{"用户约束", "u-constraint", "不得修改生产数据"},
	{"失败原因", "r-failure", `"cause":"missing SELECT permission"`},
	{"关键决策", "a-decision", "改用离线日志排查"},
	{"重要工具结果", "r-config", `"max_connections":8`},
	{"关键事实", "a-fact", "超时集中在 inventory.reserve"},
	{"未完成事项", "u-pending", "核对连接池上限"},
	{"最新请求", "u-latest", "继续定位超时原因并提出修改建议"},
}

func loadLongSession(t *testing.T) []Message {
	t.Helper()
	data, err := os.ReadFile("fixtures/long_session.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var messages []Message
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	return messages
}

// Check concrete task content and Tool metadata independently of validateOutput.
// This fixture has complete ToolRounds; an entire round may instead appear in summary sources.
func assertLongSessionEvidence(t *testing.T, input, original []Message, result Result) {
	t.Helper()
	if !reflect.DeepEqual(input, original) {
		t.Fatal("Compress mutated the caller's messages or nested metadata")
	}
	counter := RuneBudgetCounter{}
	if result.BeforeTokens != counter.CountMessages(original) || result.AfterTokens != counter.CountMessages(result.Messages) {
		t.Fatal("reported token estimates differ from the actual messages")
	}
	for _, evidence := range continuationEvidence {
		before, after := messageByID(original, evidence.id), messageByID(result.Messages, evidence.id)
		if !strings.Contains(after.Content, evidence.text) || !reflect.DeepEqual(before, after) {
			t.Errorf("%s (%s) lost or changed: want=%q got=%q", evidence.label, evidence.id, before.Content, after.Content)
		}
	}

	positions := make(map[string]int)
	for i, message := range original {
		positions[message.ID] = i
	}
	last := -1
	calls, results := make(map[string]ToolCall), make(map[string]int)
	for _, message := range result.Messages {
		anchor := message.ID
		if message.Synthetic {
			if message.Role != RoleAssistant || len(message.ToolCalls) != 0 || len(message.SourceIDs) == 0 {
				t.Fatalf("invalid summary role or provenance: %#v", message)
			}
			anchor = message.SourceIDs[0]
			for _, id := range message.SourceIDs {
				if _, exists := positions[id]; !exists || messageByID(result.Messages, id).ID != "" {
					t.Fatalf("summary source %q is unknown or was not replaced", id)
				}
			}
		}
		position, exists := positions[anchor]
		if !exists || position <= last {
			t.Fatalf("output order or ID is invalid at %q", message.ID)
		}
		last = position
		if len(message.ToolCalls) != 0 && !reflect.DeepEqual(message.ToolCalls, messageByID(original, message.ID).ToolCalls) {
			t.Fatalf("Tool Call metadata changed: %q", message.ID)
		}
		for _, call := range message.ToolCalls {
			if _, exists := calls[call.ID]; exists {
				t.Fatalf("duplicate Tool Call %q", call.ID)
			}
			calls[call.ID] = call
		}
		if message.Role == RoleTool {
			call, exists := calls[message.ToolCallID]
			before := messageByID(original, message.ID)
			if !exists || call.Name != message.ToolName || message.ToolCallID != before.ToolCallID || message.ToolName != before.ToolName {
				t.Fatalf("orphaned or changed Tool Result: %#v", message)
			}
			results[message.ToolCallID]++
		}
		if message.ContentType == "json" && !json.Valid([]byte(message.Content)) {
			t.Fatalf("message %q is labeled JSON but is not valid JSON", message.ID)
		}
	}
	for id := range calls {
		if results[id] != 1 {
			t.Errorf("Tool Call %q has %d results, want exactly one", id, results[id])
		}
	}
}
