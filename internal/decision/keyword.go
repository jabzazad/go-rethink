package decision

import (
	"context"
	"fmt"
	"time"

	"poc-gateway/internal/bot"
)

// Keyword is the baseline: a keyword must cover at least MinStrength of the message.
type Keyword struct {
	MinStrength float64
}

func (Keyword) Name() string { return "keyword" }

func (k Keyword) Decide(_ context.Context, text string) Verdict {
	start := time.Now()
	in, score := bot.MatchKeyword(text)
	v := Verdict{Decider: k.Name(), Label: bot.LabelAI, Confidence: score}
	switch {
	case in == nil:
		v.Reason = "no keyword matched"
	case score < k.MinStrength:
		v.Abstain = true
		v.Label = in.Name
		v.Reason = fmt.Sprintf("keyword for %q too weak (%.2f < %.2f)", in.Name, score, k.MinStrength)
	default:
		v.Label = in.Name
		v.Reason = fmt.Sprintf("keyword for %q (%.2f)", in.Name, score)
	}
	v.LatencyUs = time.Since(start).Microseconds()
	return v
}
