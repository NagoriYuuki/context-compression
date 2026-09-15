package compression

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Observes actual calls at the interface boundary, not the number of summary Actions.
type demoSummarizer struct {
	fake      FakeSummarizer
	calls     int
	sourceIDs []string
}

func (s *demoSummarizer) Summarize(ctx context.Context, units []Unit, maxTokens int) (Summary, error) {
	s.calls++
	for _, unit := range units {
		s.sourceIDs = append(s.sourceIDs, unit.OriginalIDs...)
	}
	return s.fake.Summarize(ctx, units, maxTokens)
}

// Run with: go test -count=1 -run '^TestCompressionDemo$' -v
// Every scenario uses the same offline task; CannotFit keeps only its necessary context.
func TestCompressionDemo(t *testing.T) {
	cases := []struct {
		name, description string
		budget            int
		mode              FakeSummaryMode
		status            FitStatus
		calls             int
		actions           []string
		diagnostic        string
	}{
		{"fit", "预算充足，消息原样返回", 4500, FakeSummarySuccess, Fit, 0, nil, ""},
		{"clear_tool_result", "清理大段工具日志后即满足预算", 1700, FakeSummarySuccess, Degraded, 0, []string{"clear_tool_result"}, ""},
		{"summary", "清理后仍超限，调用一次 Fake 摘要替换完整旧历史", 900, FakeSummarySuccess, Degraded, 1, []string{"clear_tool_result", "summarize"}, ""},
		{"fallback", "Fake 摘要报错，不重试，确定性截断旧文本", 900, FakeSummaryError, Degraded, 1, []string{"clear_tool_result", "truncate"}, "summarizer_error"},
		{"cannot_fit", "仅保留必要上下文及工具配对，仍超预算，明确返回 CannotFit", 300, FakeSummarySuccess, CannotFit, 0, nil, "fallback_no_change"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			messages, original := loadLongSession(t), loadLongSession(t)
			if tc.status == CannotFit {
				// Remove just the four process/log messages; retain the failed and important ToolRounds.
				isHistory := func(message Message) bool {
					return slices.Contains([]string{"u-history", "a-logs", "r-logs", "a-logs-followup"}, message.ID)
				}
				messages = slices.DeleteFunc(messages, isHistory)
				original = slices.DeleteFunc(original, isHistory)
			}
			request := Request{Messages: messages, ContextLimit: tc.budget + 300, OutputReserve: 200, SafetyMargin: 100}
			summarizer := &demoSummarizer{fake: FakeSummarizer{Mode: tc.mode, Content: longSessionSummary}}
			result := NewMiddleware(nil, summarizer).Compress(context.Background(), request)
			report := compressionDemoReport(tc.description, request, original, result)
			t.Log("\n" + report)
			if os.Getenv("COMPRESSION_DEMO_FULL") == "1" {
				details, err := json.MarshalIndent(struct {
					Input        []Message
					Output       Result
					SummaryCalls int
				}{original, result, summarizer.calls}, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				t.Log("完整消息元数据及诊断：\n" + string(details))
			}

			if result.Status != tc.status || summarizer.calls != tc.calls {
				t.Fatalf("want status=%s calls=%d, got status=%s calls=%d", tc.status, tc.calls, result.Status, summarizer.calls)
			}
			var actionTypes []string
			for _, action := range result.Actions {
				if !slices.Contains(actionTypes, action.Type) {
					actionTypes = append(actionTypes, action.Type)
				}
			}
			if !slices.Equal(actionTypes, tc.actions) {
				t.Fatalf("advertised path was not reached: actions=%v want=%v", actionTypes, tc.actions)
			}
			if tc.diagnostic != "" && !hasDiagnosticCode(result.Diagnostics, tc.diagnostic) {
				t.Fatalf("missing diagnostic %s", tc.diagnostic)
			}
			if result.Status == CannotFit {
				if result.AfterTokens <= tc.budget || result.FailureReason == "" || !reflect.DeepEqual(original, result.Messages) {
					t.Fatal("CannotFit must explain the excess and preserve all necessary context")
				}
			} else if result.AfterTokens > tc.budget || result.FailureReason != "" {
				t.Fatal("successful result must fit the input budget")
			}
			if result.Status == Fit && !reflect.DeepEqual(original, result.Messages) {
				t.Fatal("Fit must leave messages and metadata unchanged")
			}
			if tc.name == "summary" {
				summary := syntheticMessage(result.Messages)
				wantSources := []string{"u-history", "a-logs", "r-logs", "a-logs-followup"}
				if countSyntheticMessages(result.Messages) != 1 || !slices.Equal(summary.SourceIDs, wantSources) || !slices.Equal(summarizer.sourceIDs, wantSources) || !strings.Contains(summary.Content, longSessionSummary) || !strings.HasPrefix(summary.Content, "[历史摘要，仅供参考]") {
					t.Fatalf("expected one intact, low-authority summary of the entire old range: %#v", summary)
				}
			} else if countSyntheticMessages(result.Messages) != 0 {
				t.Fatal("this scenario should not create a summary")
			}
			assertLongSessionEvidence(t, messages, original, result)
			if !t.Failed() && tc.name == "summary" && os.Getenv("UPDATE_DEMO") == "1" {
				if err := os.WriteFile("DEMO.md", []byte(report), 0644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// The default presentation contains all message bodies in their actual order.
// Debug metadata stays in the opt-in JSON output above.
func compressionDemoReport(description string, request Request, original []Message, result Result) string {
	var b strings.Builder
	budget := request.ContextLimit - request.OutputReserve - request.SafetyMargin
	fmt.Fprintf(&b, "# 上下文压缩前后全文\n\n%s。\n\n", description)
	fmt.Fprintf(&b, "输入预算：%d · 状态：%s · 估算 Token：%d → %d\n\n", budget, result.Status, result.BeforeTokens, result.AfterTokens)
	b.WriteString("离线 Fake 摘要。以下正文按实际消息顺序完整展示。\n\n")
	if result.FailureReason != "" {
		fmt.Fprintf(&b, "无法满足预算：%s\n\n", result.FailureReason)
	}
	fmt.Fprintf(&b, "## 压缩前：完整原文（%d 条消息）\n\n", len(original))
	writeDemoConversation(&b, original)
	fmt.Fprintf(&b, "## 压缩后：完整内容（%d 条消息）\n\n", len(result.Messages))
	writeDemoConversation(&b, result.Messages)
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writeDemoConversation(b *strings.Builder, messages []Message) {
	roles := map[Role]string{
		RoleSystem: "系统", RoleDeveloper: "开发者", RoleUser: "用户",
		RoleAssistant: "助手", RoleTool: "工具结果",
	}
	for i, message := range messages {
		label := roles[message.Role]
		if message.Synthetic {
			label += "（历史摘要）"
		}
		if message.Role == RoleTool {
			label += " · " + message.ToolName + "（" + message.ToolCallID + "）"
		}
		fmt.Fprintf(b, "### %d. %s\n\n", i+1, label)
		if message.Content != "" {
			fmt.Fprintf(b, "```text\n%s\n```\n\n", message.Content)
		}
		for _, call := range message.ToolCalls {
			fmt.Fprintf(b, "工具调用：%s（%s）\n\n```text\n%s\n```\n\n", call.Name, call.ID, call.Arguments)
		}
	}
}
