package compression

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestLongSessionFixturePreservesContinuationState(t *testing.T) {
	data, err := os.ReadFile("fixtures/long_session.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var messages []Message
	if err := json.Unmarshal(data, &messages); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	counter := RuneBudgetCounter{}
	result := NewMiddleware(counter, FakeSummarizer{Content: "历史中已经确定压缩旧 Tool Result，并保留任务约束和失败原因。"}).Compress(
		context.Background(),
		Request{Messages: messages, ContextLimit: 550},
	)

	if result.Status != Degraded {
		t.Fatalf("Status = %q, want degraded; reason=%s diagnostics=%#v", result.Status, result.FailureReason, result.Diagnostics)
	}
	for _, id := range []string{"s1", "u-goal", "u-constraint", "r-failure", "a-decision", "u-pending", "u-latest"} {
		if messageByID(result.Messages, id).ID != id {
			t.Fatalf("message %q was not preserved: %#v", id, result.Messages)
		}
	}
	if messageByID(result.Messages, "u-latest").Content != "继续完成实现，并给出可以 review 的 commit。" {
		t.Fatalf("latest user changed: %#v", messageByID(result.Messages, "u-latest"))
	}
	if validation := validateOutput(result.Messages, messages); !validation.valid {
		t.Fatalf("output validation failed: %s", validation.reason)
	}
}
