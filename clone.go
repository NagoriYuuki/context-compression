package compression

// CloneMessages returns a deep copy of messages, including every slice nested
// in Message. Compression must operate on this copy so callers retain their
// original conversation unchanged.
func CloneMessages(messages []Message) []Message {
	if messages == nil {
		return nil
	}

	cloned := make([]Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneMessage(message)
	}
	return cloned
}

func cloneMessage(message Message) Message {
	message.ToolCalls = cloneToolCalls(message.ToolCalls)
	message.Tags = cloneStrings(message.Tags)
	message.SourceIDs = cloneStrings(message.SourceIDs)
	return message
}

func cloneToolCalls(toolCalls []ToolCall) []ToolCall {
	if toolCalls == nil {
		return nil
	}

	cloned := make([]ToolCall, len(toolCalls))
	copy(cloned, toolCalls)
	return cloned
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}

	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
