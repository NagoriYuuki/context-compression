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
	"text/tabwriter"
)

// Observes actual calls at the interface boundary, not the number of summary Actions.
type demoSummarizer struct {
	fake      FakeSummarizer
	calls     int
	sourceIDs []string
	maxTokens int
}

func (s *demoSummarizer) Summarize(ctx context.Context, units []Unit, maxTokens int) (Summary, error) {
	s.calls++
	s.maxTokens = maxTokens
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
			t.Log("\n" + compressionDemoReport(tc.description, request, original, result, summarizer))

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
			if !t.Failed() {
				t.Log("检查通过：实际路径与展示一致；关键内容完整；工具配对/参数、来源、顺序及 JSON 类型有效；调用方输入未变。")
			}
		})
	}
}

func compressionDemoReport(description string, request Request, original []Message, result Result, summarizer *demoSummarizer) string {
	var b strings.Builder
	counter := RuneBudgetCounter{}
	budget := request.ContextLimit - request.OutputReserve - request.SafetyMargin
	fmt.Fprintf(&b, "%s\n计数方式：RuneBudgetCounter 估算；摘要：人工编写的 Fake；工具记录：虚构离线样例。\n", description)
	fmt.Fprintf(&b, "输入预算 = %d（容量）- %d（输出预留）- %d（安全余量）= %d\n", request.ContextLimit, request.OutputReserve, request.SafetyMargin, budget)
	fmt.Fprintf(&b, "Status=%s | BeforeTokens=%d → AfterTokens=%d | 节省=%d | 发生压缩=%t\n", result.Status, result.BeforeTokens, result.AfterTokens, result.BeforeTokens-result.AfterTokens, len(result.Actions) > 0)
	if result.BeforeTokens > budget {
		fmt.Fprintf(&b, "触发原因：输入超预算 %d；压缩后预算余量=%d（负数表示仍超限）。\n", result.BeforeTokens-budget, budget-result.AfterTokens)
	} else {
		b.WriteString("触发原因：无，输入已在预算内。\n")
	}
	fmt.Fprintf(&b, "摘要器实际调用次数=%d | Fake 模式=%s", summarizer.calls, summarizer.fake.Mode)
	if summarizer.calls > 0 {
		fmt.Fprintf(&b, " | 摘要目标预算=%d | 实际输入来源=%v", summarizer.maxTokens, summarizer.sourceIDs)
	}
	b.WriteString("\n\n按执行顺序列出 Actions（Token 数对应各 Action 的目标范围，不是统一的全局值）：\n")
	if len(result.Actions) == 0 {
		b.WriteString("  无\n")
	}
	for i, action := range result.Actions {
		fmt.Fprintf(&b, "  %d. %s | unit=%s | %d → %d | %s\n", i+1, action.Type, action.UnitID, action.BeforeTokens, action.AfterTokens, action.Reason)
	}

	coveredBy := make(map[string]string)
	for _, message := range result.Messages {
		if message.Synthetic {
			for _, id := range message.SourceIDs {
				coveredBy[id] = message.ID
			}
		}
	}
	outputIDs := make([]string, 0, len(result.Messages))
	for _, message := range result.Messages {
		outputIDs = append(outputIDs, message.ID)
	}
	fmt.Fprintf(&b, "\n实际输出顺序：%s\n", strings.Join(outputIDs, " → "))
	b.WriteString("\n逐消息对照（按输入顺序，新增摘要附后；预览最多 32 字符）：\n")
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tRole\t估算前→后\t处理结果\t压缩前预览\t压缩后预览")
	for _, before := range original {
		after := messageByID(result.Messages, before.ID)
		change := "保留"
		afterTokens := fmt.Sprint(counter.CountMessages([]Message{after}))
		if after.ID == "" {
			afterTokens, change = "—", "移除"
			if coveredBy[before.ID] != "" {
				change = "并入 " + coveredBy[before.ID]
			}
		} else if !reflect.DeepEqual(before, after) {
			change = "替换内容"
		}
		fmt.Fprintf(w, "%s\t%s\t%d→%s\t%s\t%s\t%s\n", before.ID, before.Role, counter.CountMessages([]Message{before}), afterTokens, change, demoPreview(before.Content), demoPreview(after.Content))
	}
	for _, message := range result.Messages {
		if message.Synthetic {
			fmt.Fprintf(w, "%s\t%s\t—→%d\t新增摘要\t—\t%s\n", message.ID, message.Role, counter.CountMessages([]Message{message}), demoPreview(message.Content))
		}
	}
	w.Flush()

	b.WriteString("\n替换内容全文 / 摘要权限与来源：\n")
	changed := false
	for _, message := range result.Messages {
		before := messageByID(original, message.ID)
		if reflect.DeepEqual(message, before) {
			continue
		}
		changed = true
		fmt.Fprintf(&b, "  id=%s role=%s content_type=%q synthetic=%t SourceIDs=%v\n%s\n", message.ID, message.Role, message.ContentType, message.Synthetic, message.SourceIDs, message.Content)
	}
	if !changed {
		b.WriteString("  无\n")
	}
	b.WriteString("\n原始 Tool Call → Result 去向（参数为输入原文，测试检查保留参数完全一致）：\n")
	for _, message := range original {
		for _, call := range message.ToolCalls {
			for _, toolResult := range original {
				if toolResult.Role != RoleTool || toolResult.ToolCallID != call.ID {
					continue
				}
				fate := "保留调用及结果，结果内容见上表"
				if coveredBy[message.ID] != "" {
					fate = "整轮并入 " + coveredBy[message.ID]
				}
				fmt.Fprintf(&b, "  %s → %s → %s | name=%s args=%s | %s\n", message.ID, call.ID, toolResult.ID, call.Name, call.Arguments, fate)
			}
		}
	}
	b.WriteString("\n任务继续性证据（检查原文保留；不代表已让真实模型继续执行）：\n")
	for _, evidence := range continuationEvidence {
		after := messageByID(result.Messages, evidence.id)
		ok := strings.Contains(after.Content, evidence.text) && reflect.DeepEqual(messageByID(original, evidence.id), after)
		fmt.Fprintf(&b, "  %s | id=%s | 完整保留=%t | %s\n", evidence.label, evidence.id, ok, after.Content)
	}
	b.WriteString("\nDiagnostics：\n")
	if len(result.Diagnostics) == 0 {
		b.WriteString("  无\n")
	}
	for _, diagnostic := range result.Diagnostics {
		fmt.Fprintf(&b, "  %s | %s | %s | message=%s unit=%s\n", diagnostic.Level, diagnostic.Code, diagnostic.Message, diagnostic.MessageID, diagnostic.UnitID)
	}
	if result.FailureReason != "" {
		fmt.Fprintf(&b, "FailureReason=%s\n调用方处理：此结果不可直接发送给模型，应扩大输入预算或由调用方明确调整必要上下文。\n", result.FailureReason)
	}
	if os.Getenv("COMPRESSION_DEMO_FULL") == "1" {
		beforeJSON, _ := json.MarshalIndent(original, "", "  ")
		afterJSON, _ := json.MarshalIndent(result.Messages, "", "  ")
		fmt.Fprintf(&b, "\n完整输入 JSON：\n%s\n完整输出 JSON：\n%s\n", beforeJSON, afterJSON)
	}
	return b.String()
}

func demoPreview(content string) string {
	runes := []rune(strings.ReplaceAll(content, "\n", "\\n"))
	if len(runes) > 32 {
		return string(runes[:32]) + "…"
	}
	if len(runes) == 0 {
		return "—"
	}
	return string(runes)
}
