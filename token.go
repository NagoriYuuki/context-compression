package compression

import "fmt"

const (
	defaultMessageOverhead = 4
	defaultToolOverhead    = 4
)

// TokenCounter estimates the number of input tokens in normalized messages.
// Implementations must count the same representation that the middleware
// eventually returns.
type TokenCounter interface {
	CountMessages(messages []Message) int
}

// RuneBudgetCounter is a deterministic estimator for offline tests and the
// example implementation. It is not a replacement for a provider tokenizer.
type RuneBudgetCounter struct {
	MessageOverhead int
	ToolOverhead    int
}

func (c RuneBudgetCounter) CountMessages(messages []Message) int {
	messageOverhead := c.MessageOverhead
	if messageOverhead == 0 {
		messageOverhead = defaultMessageOverhead
	}
	toolOverhead := c.ToolOverhead
	if toolOverhead == 0 {
		toolOverhead = defaultToolOverhead
	}

	total := 0
	for _, message := range messages {
		total += messageOverhead
		total += len([]rune(message.Content))
		total += len([]rune(message.ToolCallID))
		total += len([]rune(message.ToolName))
		for _, call := range message.ToolCalls {
			total += toolOverhead
			total += len([]rune(call.ID))
			total += len([]rune(call.Name))
			total += len([]rune(call.Arguments))
		}
	}
	return total
}

// FixedTokenCounter is useful for tests that need exact budget boundaries.
// IDs absent from Tokens are counted by Fallback, or as zero when Fallback is
// nil and no fallback counter was configured.
type FixedTokenCounter struct {
	Tokens   map[string]int
	Fallback TokenCounter
}

func (c FixedTokenCounter) CountMessages(messages []Message) int {
	total := 0
	for _, message := range messages {
		if tokens, ok := c.Tokens[message.ID]; ok {
			total += tokens
			continue
		}
		if c.Fallback != nil {
			total += c.Fallback.CountMessages([]Message{message})
		}
	}
	return total
}

// InputBudget returns the portion of the model context available to input
// messages after reserving output space and safety margin.
func InputBudget(request Request) (int, error) {
	if request.ContextLimit <= 0 {
		return 0, fmt.Errorf("context limit must be greater than zero")
	}
	if request.OutputReserve < 0 {
		return 0, fmt.Errorf("output reserve cannot be negative")
	}
	if request.SafetyMargin < 0 {
		return 0, fmt.Errorf("safety margin cannot be negative")
	}

	budget := request.ContextLimit - request.OutputReserve - request.SafetyMargin
	if budget <= 0 {
		return 0, fmt.Errorf("input budget must be greater than zero")
	}
	return budget, nil
}
