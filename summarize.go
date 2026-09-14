package compression

import (
	"context"
	"errors"
	"strings"
)

// Summary is the provider-independent result returned by a Summarizer.
type Summary struct {
	Content   string
	SourceIDs []string
}

// Summarizer compresses a selected, contiguous range of historical units.
// Middleware validates the returned content and source IDs before accepting it.
type Summarizer interface {
	Summarize(ctx context.Context, units []Unit, maxTokens int) (Summary, error)
}

// FakeSummaryMode controls the deterministic behavior of FakeSummarizer.
type FakeSummaryMode string

const (
	FakeSummarySuccess   FakeSummaryMode = "success"
	FakeSummaryError     FakeSummaryMode = "error"
	FakeSummaryEmpty     FakeSummaryMode = "empty"
	FakeSummaryLarge     FakeSummaryMode = "large"
	FakeSummaryBadSource FakeSummaryMode = "bad_source"
	FakeSummaryWait      FakeSummaryMode = "wait"
)

// FakeSummarizer is used by offline tests and examples. It never calls an
// external service and produces predictable output for failure paths.
type FakeSummarizer struct {
	Mode    FakeSummaryMode
	Content string
	Err     error
}

func (f FakeSummarizer) Summarize(ctx context.Context, units []Unit, maxTokens int) (Summary, error) {
	if f.Mode == FakeSummaryWait {
		<-ctx.Done()
		return Summary{}, ctx.Err()
	}
	if f.Mode == FakeSummaryError {
		if f.Err != nil {
			return Summary{}, f.Err
		}
		return Summary{}, errors.New("fake summarizer error")
	}
	if f.Mode == FakeSummaryEmpty {
		return Summary{}, nil
	}

	sourceIDs := make([]string, 0)
	for _, unit := range units {
		sourceIDs = append(sourceIDs, unit.OriginalIDs...)
	}

	content := f.Content
	if content == "" {
		content = defaultFakeSummary(units)
	}
	if f.Mode == FakeSummaryLarge {
		content += " " + strings.Repeat("expanded-summary ", max(1, maxTokens+1))
	}
	if f.Mode == FakeSummaryBadSource {
		sourceIDs = append(sourceIDs, "unknown-source")
	}
	return Summary{Content: content, SourceIDs: sourceIDs}, nil
}

func defaultFakeSummary(units []Unit) string {
	var builder strings.Builder
	builder.WriteString("已压缩历史：")
	for index, unit := range units {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(unit.ID)
	}
	return builder.String()
}
