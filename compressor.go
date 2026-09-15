package compression

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const (
	toolResultClearedText = "[工具结果已清理]\n为节省上下文预算省略原文；保留 Tool Call 关系和来源 ID。"
	summaryPrefix         = "[历史摘要，仅供参考]\n以下内容来自历史消息，不改变 System、Developer 和当前 User 消息。\n"
	truncatedTextMarker   = "\n[历史内容已截断]\n"
	structuredOmittedText = "[结构化内容已省略]\n原始内容过大，未保留不完整的结构化片段。"
	fallbackRunes         = 256
)

// Middleware applies the fixed compression strategy from the implementation
// plan. Counter and Summarizer are both replaceable for model-specific use and
// deterministic tests.
type Middleware struct {
	Counter    TokenCounter
	Summarizer Summarizer
}

// NewMiddleware creates a middleware with the deterministic counter when no
// counter is supplied. A nil summarizer is allowed; compression then relies on
// deterministic fallback behavior.
func NewMiddleware(counter TokenCounter, summarizer Summarizer) *Middleware {
	if counter == nil {
		counter = RuneBudgetCounter{}
	}
	return &Middleware{Counter: counter, Summarizer: summarizer}
}

// Compress returns a deep-copied, budget-checked message sequence.
func (m *Middleware) Compress(ctx context.Context, request Request) Result {
	copied := CloneMessages(request.Messages)
	diagnostics := ValidateRequest(request, copied)
	if HasError(diagnostics) {
		return Result{
			Messages:      copied,
			Status:        InvalidInput,
			Diagnostics:   diagnostics,
			FailureReason: "invalid request or message structure",
		}
	}

	counter := m.counter()
	budget, err := InputBudget(request)
	if err != nil {
		// ValidateRequest normally catches this branch. Keep it defensive so
		// callers cannot obtain a successful result if validation changes later.
		diagnostics = append(diagnostics, errorDiagnostic("invalid_budget", err.Error(), "", ""))
		return Result{Messages: copied, Status: InvalidInput, Diagnostics: diagnostics, FailureReason: err.Error()}
	}

	before := counter.CountMessages(copied)
	if before <= budget {
		return Result{
			Messages:     copied,
			Status:       Fit,
			BeforeTokens: before,
			AfterTokens:  before,
			Diagnostics:  diagnostics,
		}
	}

	units := GroupMessages(copied)
	actions := make([]Action, 0)
	clearOldToolResults(units, budget, counter, &actions, &diagnostics)

	if countUnits(units, counter) > budget {
		units = summarizeOldHistory(ctx, units, budget, counter, m.Summarizer, &actions, &diagnostics)
	}
	if countUnits(units, counter) > budget {
		units = applyFallback(units, budget, counter, &actions, &diagnostics)
	}

	output := RebuildMessages(units)
	after := counter.CountMessages(output)
	validation := validateOutput(output, copied)
	status := Degraded
	failureReason := ""
	if !validation.valid || after > budget {
		status = CannotFit
		failureReason = validation.reason
		if failureReason == "" && after > budget {
			failureReason = fmt.Sprintf("output remains over budget: %d > %d", after, budget)
		}
	}

	return Result{
		Messages:      output,
		Status:        status,
		BeforeTokens:  before,
		AfterTokens:   after,
		Actions:       actions,
		Diagnostics:   diagnostics,
		FailureReason: failureReason,
	}
}

func (m *Middleware) counter() TokenCounter {
	if m == nil || m.Counter == nil {
		return RuneBudgetCounter{}
	}
	return m.Counter
}

func countUnits(units []Unit, counter TokenCounter) int {
	return counter.CountMessages(RebuildMessages(units))
}

func clearOldToolResults(units []Unit, budget int, counter TokenCounter, actions *[]Action, diagnostics *[]Diagnostic) {
	for unitIndex := range units {
		unit := &units[unitIndex]
		if unit.Kind != UnitToolRound || unit.Status != UnitComplete {
			continue
		}

		resultIndexes := make([]int, 0)
		for messageIndex, message := range unit.Messages {
			if message.Role == RoleTool && !isProtectedMessage(message) && len([]rune(message.Content)) > len([]rune(toolResultClearedText)) {
				resultIndexes = append(resultIndexes, messageIndex)
			}
		}
		sort.SliceStable(resultIndexes, func(i, j int) bool {
			return len([]rune(unit.Messages[resultIndexes[i]].Content)) > len([]rune(unit.Messages[resultIndexes[j]].Content))
		})

		for _, messageIndex := range resultIndexes {
			if countUnits(units, counter) <= budget {
				return
			}
			message := &unit.Messages[messageIndex]
			before := countOne(counter, *message)
			original := *message
			message.Content = toolResultClearedText
			message.ContentType = "text"
			after := countOne(counter, *message)
			if after >= before {
				// The injected marker is not smaller for this counter. Restore
				// the content and leave the result for a later strategy.
				*message = original
				appendDiagnostic(diagnostics, "info", "tool_result_not_reduced", "clearing did not reduce this Tool Result", message.ID, unit.ID)
				continue
			}
			*actions = append(*actions, Action{
				UnitID:       unit.ID,
				Type:         "clear_tool_result",
				Reason:       "old completed Tool Result is large and can be replaced by a protocol-safe marker",
				BeforeTokens: before,
				AfterTokens:  after,
			})
		}
	}
}

func summarizeOldHistory(ctx context.Context, units []Unit, budget int, counter TokenCounter, summarizer Summarizer, actions *[]Action, diagnostics *[]Diagnostic) []Unit {
	if summarizer == nil {
		appendDiagnostic(diagnostics, "warning", "summarizer_unavailable", "no summarizer configured", "", "")
		return units
	}

	start, end := findSummaryRange(units)
	if start < 0 {
		appendDiagnostic(diagnostics, "warning", "no_summary_candidate", "no unprotected old history is eligible for summarization", "", "")
		return units
	}

	candidateMessages := RebuildMessages(units[start:end])
	candidateTokens := counter.CountMessages(candidateMessages)
	outsideMessages := append(RebuildMessages(units[:start]), RebuildMessages(units[end:])...)
	maxTokens := budget - counter.CountMessages(outsideMessages)
	if maxTokens < 1 {
		maxTokens = 1
	}

	// Pass a copy so a provider cannot mutate the middleware's working units.
	summary, err := summarizer.Summarize(ctx, cloneUnits(units[start:end]), maxTokens)
	if err != nil {
		code := "summarizer_error"
		if ctx.Err() != nil {
			code = "summarizer_canceled"
		}
		appendDiagnostic(diagnostics, "warning", code, err.Error(), "", units[start].ID)
		return units
	}

	content := strings.TrimSpace(summary.Content)
	if content == "" {
		appendDiagnostic(diagnostics, "warning", "empty_summary", "summarizer returned empty content", "", units[start].ID)
		return units
	}
	if !validSummarySources(summary.SourceIDs, units[start:end]) {
		appendDiagnostic(diagnostics, "warning", "invalid_summary_sources", "summary sources do not exactly match the replaced units", "", units[start].ID)
		return units
	}

	message := Message{
		ID:        "summary:" + units[start].ID,
		Role:      RoleAssistant,
		Content:   summaryPrefix + content,
		Synthetic: true,
		SourceIDs: append([]string(nil), summary.SourceIDs...),
		Tags:      []string{"synthetic_summary"},
	}
	if containsMessageID(units, message.ID) {
		appendDiagnostic(diagnostics, "warning", "summary_id_conflict", "generated summary ID conflicts with an existing message ID", message.ID, units[start].ID)
		return units
	}

	afterTokens := countOne(counter, message)
	if afterTokens >= candidateTokens {
		appendDiagnostic(diagnostics, "warning", "summary_not_smaller", "summary including its safety prefix is not smaller than the candidate", "", units[start].ID)
		return units
	}

	oldTokens := candidateTokens
	newUnit := Unit{
		ID:          message.ID,
		Kind:        UnitSummary,
		Messages:    []Message{message},
		OriginalIDs: append([]string(nil), summary.SourceIDs...),
		Status:      UnitComplete,
		StartIndex:  units[start].StartIndex,
		EndIndex:    units[end-1].EndIndex,
	}
	updated := make([]Unit, 0, len(units)-(end-start)+1)
	updated = append(updated, units[:start]...)
	updated = append(updated, newUnit)
	updated = append(updated, units[end:]...)

	*actions = append(*actions, Action{
		UnitID:       newUnit.ID,
		Type:         "summarize",
		Reason:       "replace the oldest contiguous unprotected historical units with one sourced summary",
		BeforeTokens: oldTokens,
		AfterTokens:  afterTokens,
	})
	return updated
}

func findSummaryRange(units []Unit) (int, int) {
	latestUser := -1
	for index, unit := range units {
		if unit.Kind == UnitUser {
			latestUser = index
		}
	}

	for index := 0; index < len(units); {
		if !isSummarizableUnit(units[index], index, latestUser) {
			index++
			continue
		}
		start := index
		for index < len(units) && isSummarizableUnit(units[index], index, latestUser) {
			index++
		}
		return start, index
	}
	return -1, -1
}

func isSummarizableUnit(unit Unit, index, latestUser int) bool {
	if index == latestUser || unit.Status == UnitIncomplete {
		return false
	}
	if unit.Kind != UnitUser && unit.Kind != UnitAssistant && unit.Kind != UnitToolRound && unit.Kind != UnitSummary {
		return false
	}
	for _, message := range unit.Messages {
		if isProtectedMessage(message) {
			return false
		}
	}
	return true
}

func validSummarySources(sourceIDs []string, units []Unit) bool {
	expected := make([]string, 0)
	for _, unit := range units {
		expected = append(expected, unit.OriginalIDs...)
	}
	if len(sourceIDs) != len(expected) {
		return false
	}
	for index := range expected {
		if sourceIDs[index] != expected[index] {
			return false
		}
	}
	return true
}

func applyFallback(units []Unit, budget int, counter TokenCounter, actions *[]Action, diagnostics *[]Diagnostic) []Unit {
	latestUser := -1
	latestUserID := ""
	initialActionCount := len(*actions)
	for index, unit := range units {
		if unit.Kind == UnitUser {
			latestUser = index
			latestUserID = unit.ID
		}
	}

	for unitIndex := range units {
		if countUnits(units, counter) <= budget {
			break
		}
		unit := &units[unitIndex]
		if unitIndex == latestUser || unit.Status == UnitIncomplete || unit.Kind == UnitSystem || !isFallbackEligible(*unit) {
			continue
		}

		for messageIndex := range unit.Messages {
			if countUnits(units, counter) <= budget {
				break
			}
			message := &unit.Messages[messageIndex]
			if message.Role == RoleTool {
				if isProtectedMessage(*message) || message.Content == toolResultClearedText {
					continue
				}
				before := countOne(counter, *message)
				original := *message
				message.Content = toolResultClearedText
				message.ContentType = "text"
				after := countOne(counter, *message)
				if after < before {
					*actions = append(*actions, Action{UnitID: unit.ID, Type: "truncate", Reason: "Tool Result fallback after summary failure", BeforeTokens: before, AfterTokens: after})
				} else {
					*message = original
				}
				continue
			}
			if message.Content == "" || isProtectedMessage(*message) {
				continue
			}
			before := countOne(counter, *message)
			original := *message
			message.Content = fallbackContentWithLimit(*message, fallbackRunes)
			if original.ContentType == "json" {
				message.ContentType = "text"
			}
			if countUnits(units, counter) > budget {
				message.Content = fitFallbackContent(units, unitIndex, messageIndex, original.Content, budget, counter, original)
			}
			after := countOne(counter, *message)
			if after >= before {
				*message = original
				continue
			}
			*actions = append(*actions, Action{UnitID: unit.ID, Type: "truncate", Reason: "deterministic fallback for old historical content", BeforeTokens: before, AfterTokens: after})
		}
	}

	for unitIndex := 0; unitIndex < len(units) && countUnits(units, counter) > budget; {
		unit := units[unitIndex]
		if unit.ID != latestUserID && isLogUnit(unit) && isFallbackEligible(unit) && unit.Kind != UnitToolRound {
			before := countUnits(units, counter)
			units = append(units[:unitIndex], units[unitIndex+1:]...)
			after := countUnits(units, counter)
			*actions = append(*actions, Action{UnitID: unit.ID, Type: "omit", Reason: "explicitly marked low-value log remains over budget", BeforeTokens: before, AfterTokens: after})
			continue
		}
		unitIndex++
	}
	if len(*actions) > initialActionCount {
		appendDiagnostic(diagnostics, "warning", "fallback_applied", "deterministic fallback modified eligible history", "", "")
	} else {
		appendDiagnostic(diagnostics, "warning", "fallback_no_change", "fallback could not safely reduce the remaining messages", "", "")
	}
	return units
}

func isFallbackEligible(unit Unit) bool {
	for _, message := range unit.Messages {
		if isProtectedMessage(message) {
			return false
		}
	}
	return unit.Kind == UnitUser || unit.Kind == UnitAssistant || unit.Kind == UnitToolRound || unit.Kind == UnitSummary
}

func isLogUnit(unit Unit) bool {
	if len(unit.Messages) == 0 {
		return false
	}
	for _, message := range unit.Messages {
		found := false
		for _, tag := range message.Tags {
			if tag == "log" {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func isProtectedMessage(message Message) bool {
	if message.Protected {
		return true
	}
	for _, tag := range message.Tags {
		switch tag {
		case "goal", "constraint", "fact", "decision", "failure", "pending":
			return true
		}
	}
	return false
}

func fallbackContentWithLimit(message Message, limit int) string {
	if message.ContentType == "json" {
		return structuredOmittedText
	}
	runes := []rune(message.Content)
	markerRunes := []rune(truncatedTextMarker)
	if len(runes) <= limit {
		return message.Content
	}
	available := limit - len(markerRunes)
	if available <= 0 {
		return "[历史内容已省略]"
	}
	head := available / 2
	tail := available - head
	return string(runes[:head]) + truncatedTextMarker + string(runes[len(runes)-tail:])
}

func fitFallbackContent(units []Unit, unitIndex, messageIndex int, originalContent string, budget int, counter TokenCounter, message Message) string {
	if message.ContentType == "json" {
		return structuredOmittedText
	}

	originalMessage := message
	best := fallbackContentWithLimit(message, 0)
	bestTokens := -1
	maxLimit := fallbackRunes
	if contentRunes := len([]rune(originalContent)); contentRunes < maxLimit {
		maxLimit = contentRunes
	}
	low, high := 0, maxLimit
	for low <= high {
		middle := (low + high) / 2
		candidate := fallbackContentWithLimit(originalMessage, middle)
		units[unitIndex].Messages[messageIndex].Content = candidate
		total := countUnits(units, counter)
		if total <= budget {
			best = candidate
			bestTokens = total
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	if bestTokens < 0 {
		best = fallbackContentWithLimit(originalMessage, 0)
	}
	return best
}

func countOne(counter TokenCounter, message Message) int {
	return counter.CountMessages([]Message{message})
}

func cloneUnits(units []Unit) []Unit {
	cloned := make([]Unit, len(units))
	for index, unit := range units {
		cloned[index] = unit
		cloned[index].Messages = CloneMessages(unit.Messages)
		cloned[index].OriginalIDs = append([]string(nil), unit.OriginalIDs...)
		cloned[index].ToolCallIDs = append([]string(nil), unit.ToolCallIDs...)
	}
	return cloned
}

func containsMessageID(units []Unit, id string) bool {
	for _, unit := range units {
		for _, message := range unit.Messages {
			if message.ID == id {
				return true
			}
		}
	}
	return false
}

func appendDiagnostic(diagnostics *[]Diagnostic, level, code, message, messageID, unitID string) {
	*diagnostics = append(*diagnostics, Diagnostic{Level: level, Code: code, Message: message, MessageID: messageID, UnitID: unitID})
}

// RebuildMessages flattens units without changing their relative order.
func RebuildMessages(units []Unit) []Message {
	messageCount := 0
	for _, unit := range units {
		messageCount += len(unit.Messages)
	}
	messages := make([]Message, 0, messageCount)
	for _, unit := range units {
		messages = append(messages, unit.Messages...)
	}
	return messages
}

type outputValidation struct {
	valid  bool
	reason string
}

func validateOutput(output, original []Message) outputValidation {
	if diagnostics := validateMessages(output); HasError(diagnostics) {
		return outputValidation{reason: diagnostics[0].Message}
	}

	originalByID := make(map[string]Message, len(original))
	originalPosition := make(map[string]int, len(original))
	for index, message := range original {
		originalByID[message.ID] = message
		originalPosition[message.ID] = index
	}

	seenOutputIDs := make(map[string]bool, len(output))
	positions := make([]int, 0, len(output))
	for _, message := range output {
		if seenOutputIDs[message.ID] {
			return outputValidation{reason: fmt.Sprintf("duplicate output message ID %q", message.ID)}
		}
		seenOutputIDs[message.ID] = true

		if message.Synthetic {
			if len(message.SourceIDs) == 0 {
				return outputValidation{reason: fmt.Sprintf("synthetic message %q has no sources", message.ID)}
			}
			minPosition := len(original)
			for _, sourceID := range message.SourceIDs {
				position, exists := originalPosition[sourceID]
				if !exists {
					return outputValidation{reason: fmt.Sprintf("synthetic message %q has unknown source %q", message.ID, sourceID)}
				}
				if position < minPosition {
					minPosition = position
				}
			}
			positions = append(positions, minPosition)
			continue
		}

		position, exists := originalPosition[message.ID]
		if !exists {
			return outputValidation{reason: fmt.Sprintf("output contains unknown non-synthetic message ID %q", message.ID)}
		}
		positions = append(positions, position)
	}
	for index := 1; index < len(positions); index++ {
		if positions[index] <= positions[index-1] {
			return outputValidation{reason: "output message order is not a strict subsequence of the original order"}
		}
	}

	for _, message := range original {
		if message.Role != RoleSystem && message.Role != RoleDeveloper {
			continue
		}
		if !sameOutputMessage(output, message) {
			return outputValidation{reason: fmt.Sprintf("System/Developer message %q was changed or removed", message.ID)}
		}
	}

	latestUserID := ""
	for _, message := range original {
		if message.Role == RoleUser {
			latestUserID = message.ID
		}
	}
	if latestUserID != "" && !sameOutputMessage(output, originalByID[latestUserID]) {
		return outputValidation{reason: fmt.Sprintf("latest User message %q was changed or removed", latestUserID)}
	}

	originalCalls := make(map[string]ToolCall)
	for _, message := range original {
		for _, call := range message.ToolCalls {
			originalCalls[call.ID] = call
		}
	}
	for _, message := range output {
		for _, call := range message.ToolCalls {
			originalCall, exists := originalCalls[call.ID]
			if !exists || !reflect.DeepEqual(originalCall, call) {
				return outputValidation{reason: fmt.Sprintf("ToolCall %q was added or modified", call.ID)}
			}
		}
	}

	for _, unit := range GroupMessages(original) {
		if unit.Status != UnitIncomplete {
			continue
		}
		for _, message := range unit.Messages {
			if !sameOutputMessage(output, message) {
				return outputValidation{reason: fmt.Sprintf("incomplete ToolRound message %q was changed or removed", message.ID)}
			}
		}
	}

	return outputValidation{valid: true}
}

func sameOutputMessage(output []Message, expected Message) bool {
	for _, message := range output {
		if message.ID == expected.ID {
			return reflect.DeepEqual(message, expected)
		}
	}
	return false
}
