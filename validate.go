package compression

import "fmt"

// ValidateRequest validates both budget parameters and the normalized message
// protocol. It returns diagnostics instead of mutating the input.
func ValidateRequest(request Request, messages []Message) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	if _, err := InputBudget(request); err != nil {
		diagnostics = append(diagnostics, Diagnostic{
			Level:   "error",
			Code:    "invalid_budget",
			Message: err.Error(),
		})
	}

	diagnostics = append(diagnostics, validateMessages(messages)...)
	return diagnostics
}

func validateMessages(messages []Message) []Diagnostic {
	diagnostics := make([]Diagnostic, 0)
	messageIDs := make(map[string]int, len(messages))
	toolCalls := make(map[string]int)
	toolResults := make(map[string]int)

	for index, message := range messages {
		if message.ID == "" {
			diagnostics = append(diagnostics, errorDiagnostic("empty_message_id", "message ID cannot be empty", "", ""))
		} else if previous, exists := messageIDs[message.ID]; exists {
			diagnostics = append(diagnostics, errorDiagnostic(
				"duplicate_message_id",
				fmt.Sprintf("message ID %q is duplicated at indexes %d and %d", message.ID, previous, index),
				message.ID,
				"",
			))
		} else {
			messageIDs[message.ID] = index
		}

		if !validRole(message.Role) {
			diagnostics = append(diagnostics, errorDiagnostic("invalid_role", fmt.Sprintf("unknown role %q", message.Role), message.ID, ""))
		}

		if len(message.ToolCalls) > 0 && message.Role != RoleAssistant {
			diagnostics = append(diagnostics, errorDiagnostic("tool_call_wrong_role", "ToolCalls are only valid on assistant messages", message.ID, ""))
		}
		for _, call := range message.ToolCalls {
			if call.ID == "" {
				diagnostics = append(diagnostics, errorDiagnostic("empty_tool_call_id", "ToolCall ID cannot be empty", message.ID, ""))
				continue
			}
			if previous, exists := toolCalls[call.ID]; exists {
				diagnostics = append(diagnostics, errorDiagnostic(
					"duplicate_tool_call_id",
					fmt.Sprintf("ToolCall ID %q is duplicated at indexes %d and %d", call.ID, previous, index),
					message.ID,
					"",
				))
			} else {
				toolCalls[call.ID] = index
			}
		}

		if message.Role == RoleTool {
			if message.ToolCallID == "" {
				diagnostics = append(diagnostics, errorDiagnostic("empty_tool_result_id", "Tool result ToolCallID cannot be empty", message.ID, ""))
			} else if previous, exists := toolResults[message.ToolCallID]; exists {
				diagnostics = append(diagnostics, errorDiagnostic(
					"duplicate_tool_result",
					fmt.Sprintf("ToolCallID %q has results at indexes %d and %d", message.ToolCallID, previous, index),
					message.ID,
					"",
				))
			} else {
				toolResults[message.ToolCallID] = index
			}
		} else if message.ToolCallID != "" {
			diagnostics = append(diagnostics, errorDiagnostic("tool_result_wrong_role", "ToolCallID is only valid on tool messages", message.ID, ""))
		}
	}

	for toolCallID, resultIndex := range toolResults {
		callIndex, exists := toolCalls[toolCallID]
		if !exists {
			diagnostics = append(diagnostics, errorDiagnostic(
				"orphan_tool_result",
				fmt.Sprintf("Tool result %q has no matching ToolCall", toolCallID),
				messages[resultIndex].ID,
				"",
			))
			continue
		}
		if resultIndex <= callIndex {
			diagnostics = append(diagnostics, errorDiagnostic(
				"tool_result_before_call",
				fmt.Sprintf("Tool result %q appears before its ToolCall", toolCallID),
				messages[resultIndex].ID,
				"",
			))
		}
	}

	for _, unit := range GroupMessages(messages) {
		if unit.Kind != UnitToolRound {
			continue
		}
		if unit.Status == UnitIncomplete && unit.EndIndex != len(messages)-1 {
			diagnostics = append(diagnostics, errorDiagnostic(
				"incomplete_tool_round_not_last",
				"an incomplete ToolRound must be the final unit",
				"",
				unit.ID,
			))
		}
		seenResults := make(map[string]bool)
		for _, message := range unit.Messages {
			if message.Role == RoleTool {
				seenResults[message.ToolCallID] = true
			}
		}
		for _, callID := range unit.ToolCallIDs {
			if !seenResults[callID] {
				// An incomplete final round is valid; the missing result is the
				// reason it is marked incomplete.
				if unit.Status == UnitComplete {
					diagnostics = append(diagnostics, errorDiagnostic(
						"tool_round_missing_result",
						fmt.Sprintf("ToolCall %q is missing its result", callID),
						"",
						unit.ID,
					))
				}
			}
		}
	}

	return diagnostics
}

func validRole(role Role) bool {
	switch role {
	case RoleSystem, RoleDeveloper, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

func errorDiagnostic(code, message, messageID, unitID string) Diagnostic {
	return Diagnostic{Level: "error", Code: code, Message: message, MessageID: messageID, UnitID: unitID}
}

// HasError reports whether any diagnostic is an error.
func HasError(diagnostics []Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Level == "error" {
			return true
		}
	}
	return false
}
