package compression

// UnitKind identifies the compression unit represented by Unit.
type UnitKind string

const (
	UnitSystem    UnitKind = "system"
	UnitUser      UnitKind = "user"
	UnitAssistant UnitKind = "assistant"
	UnitToolRound UnitKind = "tool_round"
	UnitSummary   UnitKind = "summary"
)

// UnitStatus describes whether a ToolRound has received every result.
type UnitStatus string

const (
	UnitComplete   UnitStatus = "complete"
	UnitIncomplete UnitStatus = "incomplete"
)

// Unit is the internal compression view of one or more messages.
type Unit struct {
	ID          string
	Kind        UnitKind
	Messages    []Message
	OriginalIDs []string
	ToolCallIDs []string
	Status      UnitStatus
	StartIndex  int
	EndIndex    int
}

// GroupMessages builds units while preserving the original message order.
// Validation is intentionally separate; callers should validate before using
// units for compression, but grouping remains deterministic for valid input.
func GroupMessages(messages []Message) []Unit {
	units := make([]Unit, 0, len(messages))
	for index := 0; index < len(messages); {
		message := messages[index]

		if message.Role == RoleSystem || message.Role == RoleDeveloper {
			start := index
			for index < len(messages) && (messages[index].Role == RoleSystem || messages[index].Role == RoleDeveloper) {
				index++
			}
			units = append(units, newUnit(
				"system:"+messages[start].ID,
				UnitSystem,
				messages[start:index],
				start,
				index-1,
				UnitComplete,
			))
			continue
		}

		if message.Role == RoleAssistant && len(message.ToolCalls) > 0 {
			unit, next := groupToolRound(messages, index)
			units = append(units, unit)
			index = next
			continue
		}

		kind := UnitAssistant
		if message.Role == RoleUser {
			kind = UnitUser
		} else if message.Synthetic {
			kind = UnitSummary
		}
		units = append(units, newUnit(message.ID, kind, messages[index:index+1], index, index, UnitComplete))
		index++
	}
	return units
}

func groupToolRound(messages []Message, start int) (Unit, int) {
	assistant := messages[start]
	callIDs := make([]string, 0, len(assistant.ToolCalls))
	remaining := make(map[string]struct{}, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		callIDs = append(callIDs, call.ID)
		remaining[call.ID] = struct{}{}
	}

	end := start + 1
	for end < len(messages) && messages[end].Role == RoleTool {
		if _, ok := remaining[messages[end].ToolCallID]; ok {
			delete(remaining, messages[end].ToolCallID)
		}
		end++
		if len(remaining) == 0 {
			break
		}
	}

	status := UnitComplete
	if len(remaining) > 0 {
		status = UnitIncomplete
	}

	// A normal assistant explanation immediately following a complete round is
	// part of the round. An assistant ToolCall starts a new round instead.
	if status == UnitComplete && end < len(messages) && messages[end].Role == RoleAssistant && len(messages[end].ToolCalls) == 0 {
		end++
	}

	return newToolRoundUnit(messages[start].ID, messages[start:end], start, end-1, status, callIDs), end
}

func newToolRoundUnit(id string, messages []Message, start, end int, status UnitStatus, callIDs []string) Unit {
	unit := newUnit("tool:"+id, UnitToolRound, messages, start, end, status)
	unit.ToolCallIDs = append([]string(nil), callIDs...)
	return unit
}

func newUnit(id string, kind UnitKind, messages []Message, start, end int, status UnitStatus) Unit {
	originalIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		originalIDs = append(originalIDs, message.ID)
	}
	return Unit{
		ID:          id,
		Kind:        kind,
		Messages:    messages,
		OriginalIDs: originalIDs,
		Status:      status,
		StartIndex:  start,
		EndIndex:    end,
	}
}
