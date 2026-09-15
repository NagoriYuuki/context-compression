package compression

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFakeSummarizerReturnsContentAndAllSources(t *testing.T) {
	units := []Unit{
		{ID: "u1", OriginalIDs: []string{"m1", "m2"}},
		{ID: "u2", OriginalIDs: []string{"m3"}},
	}

	summary, err := (FakeSummarizer{}).Summarize(context.Background(), units, 20)
	if err != nil {
		t.Fatalf("Summarize() error = %v", err)
	}
	if summary.Content == "" || summary.Content != "已压缩历史：u1,u2" {
		t.Fatalf("unexpected summary content: %#v", summary)
	}
	if got, want := strings.Join(summary.SourceIDs, ","), "m1,m2,m3"; got != want {
		t.Fatalf("SourceIDs = %q, want %q", got, want)
	}
}

func TestFakeSummarizerFailureModes(t *testing.T) {
	units := []Unit{{ID: "u1", OriginalIDs: []string{"m1"}}}
	tests := []struct {
		name string
		mode FakeSummaryMode
		want string
	}{
		{name: "empty", mode: FakeSummaryEmpty, want: ""},
		{name: "bad source", mode: FakeSummaryBadSource, want: "unknown-source"},
		{name: "large", mode: FakeSummaryLarge, want: "expanded-summary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary, err := (FakeSummarizer{Mode: test.mode}).Summarize(context.Background(), units, 2)
			if err != nil {
				t.Fatalf("Summarize() error = %v", err)
			}
			if test.mode == FakeSummaryEmpty {
				if summary.Content != "" || len(summary.SourceIDs) != 0 {
					t.Fatalf("expected an empty summary, got %#v", summary)
				}
				return
			}
			if !strings.Contains(summary.Content+strings.Join(summary.SourceIDs, ","), test.want) {
				t.Fatalf("summary = %#v, missing %q", summary, test.want)
			}
		})
	}
}

func TestFakeSummarizerReturnsConfiguredError(t *testing.T) {
	wantErr := errors.New("timeout")
	_, err := (FakeSummarizer{Mode: FakeSummaryError, Err: wantErr}).Summarize(context.Background(), nil, 1)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestFakeSummarizerStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (FakeSummarizer{Mode: FakeSummaryWait}).Summarize(ctx, nil, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
