package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"poc-gateway/internal/bot"
)

// Jev asks one Choice question (options: the bot intents plus "ai"). By default it
// calls Vercel AI Gateway's Decision endpoint, POST /v1/evaluate
// (https://vercel.com/docs/ai-gateway/modalities/decision). The response parser also
// accepts the TypeSafe shape, so URL can point at the gateway's TypeSafe-compatible
// endpoint (/typesafe/v1/systemone) or at TypeSafe directly.
type Jev struct {
	APIKey  string
	URL     string // e.g. https://ai-gateway.vercel.sh/v1/evaluate
	Model   string // "typesafe-ai/jev" on Vercel, "jev-latest" on TypeSafe direct
	MinConf float64
	// PricePerMInput is USD per 1M input tokens (output tokens are free). Used only
	// when the response doesn't report its cost (Vercel reports the billed cost).
	PricePerMInput float64
	HTTP           *http.Client
	// Limiter, if set, caps calls per minute: live calls over the cap fail fast
	// (so the pipeline falls back), paced calls (eval) wait for a slot.
	Limiter *RateLimiter
}

func (Jev) Name() string { return "jev" }

type jevQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type jevRequest struct {
	State     string                 `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]jevQuestion `json:"questions"`
}

// jevResponse covers both response shapes: Vercel /v1/evaluate (camelCase usage,
// providerMetadata, no confidence on choice answers) and TypeSafe /systemone
// (snake_case usage, provider_metadata, confidence present).
type jevResponse struct {
	Model   string `json:"model"`
	Answers map[string]struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    *float64           `json:"confidence"`
	} `json:"answers"`
	Usage struct {
		InputTokens      int `json:"inputTokens"`
		InputTokensSnake int `json:"input_tokens"`
	} `json:"usage"`
	ProviderMetadata      jevGatewayMeta `json:"providerMetadata"`
	ProviderMetadataSnake jevGatewayMeta `json:"provider_metadata"`
}

type jevGatewayMeta struct {
	Gateway struct {
		Cost string `json:"cost"`
	} `json:"gateway"`
}

func (r *jevResponse) inputTokens() int {
	return max(r.Usage.InputTokens, r.Usage.InputTokensSnake)
}

func (r *jevResponse) gatewayCost() (float64, bool) {
	for _, s := range []string{r.ProviderMetadata.Gateway.Cost, r.ProviderMetadataSnake.Gateway.Cost} {
		if c, err := strconv.ParseFloat(s, 64); err == nil {
			return c, true
		}
	}
	return 0, false
}

// jevInstructions frames the question as the routing decision it is: the state is
// only the customer's raw message, so the question must say what is being decided.
const jevInstructions = "Route this customer chat message (Thai or English) to who should reply: " +
	"the chatbot, if one of its fixed canned answers fully covers it; " +
	"otherwise the AI assistant, which answers open-ended or detailed questions."

func jevCriteria() map[string]string {
	c := make(map[string]string, len(bot.Intents)+1)
	for _, in := range bot.Intents {
		c[in.Name] = "Chatbot: " + in.Description
	}
	c[bot.LabelAI] = "AI assistant: " + bot.AIDescription
	return c
}

func (j Jev) Decide(ctx context.Context, text string) Verdict {
	start := time.Now()
	v := Verdict{Decider: j.Name(), Label: bot.LabelAI}

	if j.Limiter != nil {
		if isPaced(ctx) {
			if err := j.Limiter.Wait(ctx); err != nil {
				v.Error, v.Reason = err.Error(), "gave up waiting for a rate-limit slot"
				return v
			}
			start = time.Now() // latency excludes time spent queued
		} else if !j.Limiter.TryAcquire() {
			v.Error = fmt.Sprintf("local rate limit: %d requests/min used", j.Limiter.RPM())
			v.Reason = "over the per-minute cap, skipped"
			v.LatencyUs = time.Since(start).Microseconds()
			return v
		}
	}

	resp, err := j.call(ctx, text)
	if err != nil {
		v.Error = err.Error()
		v.Reason = "jev call failed"
		v.LatencyUs = time.Since(start).Microseconds()
		return v
	}
	v.InputTokens = resp.inputTokens()
	v.CostUSD = float64(v.InputTokens) / 1e6 * j.PricePerMInput
	if c, ok := resp.gatewayCost(); ok {
		v.CostUSD = c // the gateway's billed cost
	}

	ans, ok := resp.Answers["route"]
	if !ok || ans.Choice == "" {
		v.Error = "jev response missing answer"
		v.LatencyUs = time.Since(start).Microseconds()
		return v
	}
	v.Label = ans.Choice
	// /v1/evaluate returns no confidence for choices; use the chosen option's probability.
	conf := ans.Probabilities[ans.Choice]
	if ans.Confidence != nil {
		conf = *ans.Confidence
	}
	v.Confidence = conf
	switch {
	case ans.Choice == bot.LabelAI:
		v.Reason = fmt.Sprintf("jev chose ai (confidence %.2f)", conf)
	case conf < j.MinConf:
		// Jev itself cannot abstain; we add the abstain on top via a confidence floor.
		v.Abstain = true
		v.Reason = fmt.Sprintf("jev chose %q but confidence %.2f < %.2f", ans.Choice, conf, j.MinConf)
	default:
		v.Reason = fmt.Sprintf("jev chose %q (confidence %.2f)", ans.Choice, conf)
	}
	v.LatencyUs = time.Since(start).Microseconds()
	return v
}

func (j Jev) call(ctx context.Context, text string) (*jevResponse, error) {
	body, _ := json.Marshal(jevRequest{
		State: text, // only the customer's message, nothing else
		Model: j.Model,
		Questions: map[string]jevQuestion{
			"route": {
				Type:         "choice",
				Instructions: jevInstructions,
				Criteria:     jevCriteria(),
			},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+j.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := j.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev status %d: %s", res.StatusCode, truncate(string(raw), 300))
	}
	var out jevResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("jev decode: %w", err)
	}
	return &out, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
