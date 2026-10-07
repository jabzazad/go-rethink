// Package decision decides, for each incoming message, whether the rule-based bot
// can answer it (and with which intent) or whether it must go to the AI.
//
// Three deciders implement the same interface so they can be compared head to head:
//   - jev:     TypeSafe Jev, a calibrated Choice question over the API (primary)
//   - rethink: modelless nearest-neighbour over a labelled corpus, abstains when unsure (fallback)
//   - keyword: plain keyword rules (baseline)
package decision

import (
	"context"

	"poc-gateway/internal/bot"
)

const (
	RouteBot = "bot"
	RouteAI  = "ai"
)

// Verdict is one decider's answer for one message.
type Verdict struct {
	Decider    string  `json:"decider"`
	Label      string  `json:"label"` // an intent name or "ai"
	Confidence float64 `json:"confidence"`
	// Abstain means the decider was not confident enough to pick a bot intent,
	// so the message escalates to the AI.
	Abstain     bool    `json:"abstain,omitempty"`
	Reason      string  `json:"reason"`
	LatencyUs   int64   `json:"latency_us"`
	InputTokens int     `json:"input_tokens,omitempty"`
	CostUSD     float64 `json:"cost_usd"`
	Error       string  `json:"error,omitempty"`
}

// Route maps a verdict to bot or ai.
func (v Verdict) Route() string {
	if v.Error != "" || v.Abstain || v.Label == bot.LabelAI || v.Label == "" {
		return RouteAI
	}
	return RouteBot
}

// EffectiveLabel is the label that actually gets acted on (abstain/error => "ai").
func (v Verdict) EffectiveLabel() string {
	if v.Route() == RouteAI {
		return bot.LabelAI
	}
	return v.Label
}

type Decider interface {
	Name() string
	Decide(ctx context.Context, text string) Verdict
}
