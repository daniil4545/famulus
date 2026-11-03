package agent

import (
	"fmt"
	"regexp"
	"time"
)

// LimitError means the agent's plan or free tier is used up; the run may succeed later.
type LimitError struct {
	Agent string
	// RetryAfter is the wait the provider asked for, zero when it did not say.
	RetryAfter time.Duration
	Message    string
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%s limit reached: %s", e.Agent, e.Message)
}

// limitText matches what the CLIs print on a used-up plan: OpenCode "Free usage exceeded",
// Claude "usage limit reached", Codex "usage limit", HTTP 429.
var limitText = regexp.MustCompile(`(?i)free usage|usage limit|rate limit|quota|too many requests|\b429\b`)

func isLimit(text string) bool {
	return limitText.MatchString(text)
}
