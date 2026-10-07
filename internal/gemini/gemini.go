// Package gemini answers open-ended messages with Gemini, keeping a short per-user history.
package gemini

import (
	"context"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

const systemPrompt = `You are a friendly customer-support assistant chatting with users on LINE.
Reply in the same language the user writes in (usually Thai). Keep answers short and
conversational, a few sentences at most, plain text only (LINE does not render Markdown).
If you don't know a company-specific fact (prices, job status, schedules), say so and suggest
contacting staff at 02-000-0000 instead of guessing.`

// maxTurns is the number of past user/model exchanges kept per user.
const maxTurns = 5

// Pricing is USD per 1M tokens. Thinking tokens are billed as output.
type Pricing struct {
	InputPerM, OutputPerM float64
}

// Result is one Gemini call with its usage and cost.
type Result struct {
	Text         string  `json:"-"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"` // includes thinking tokens
	CostUSD      float64 `json:"cost_usd"`
	LatencyMs    int64   `json:"latency_ms"`
}

type turn struct{ user, model string }

type Client struct {
	api            *genai.Client
	model          string
	price          Pricing
	thinkingBudget *int32

	mu      sync.Mutex
	history map[string][]turn
}

// New creates a client. thinkingBudget < 0 leaves the model default.
func New(ctx context.Context, apiKey, model string, price Pricing, thinkingBudget int) (*Client, error) {
	api, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, err
	}
	c := &Client{api: api, model: model, price: price, history: map[string][]turn{}}
	if thinkingBudget >= 0 {
		b := int32(thinkingBudget)
		c.thinkingBudget = &b
	}
	return c, nil
}

func (c *Client) Model() string    { return c.model }
func (c *Client) Pricing() Pricing { return c.price }

func (c *Client) Reply(ctx context.Context, userID, text string) (Result, error) {
	c.mu.Lock()
	past := append([]turn(nil), c.history[userID]...)
	c.mu.Unlock()

	contents := make([]*genai.Content, 0, len(past)*2+1)
	for _, t := range past {
		contents = append(contents,
			genai.NewContentFromText(t.user, genai.RoleUser),
			genai.NewContentFromText(t.model, genai.RoleModel))
	}
	contents = append(contents, genai.NewContentFromText(text, genai.RoleUser))

	cfg := &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText(systemPrompt, genai.RoleUser)}
	if c.thinkingBudget != nil {
		cfg.ThinkingConfig = &genai.ThinkingConfig{ThinkingBudget: c.thinkingBudget}
	}

	start := time.Now()
	resp, err := c.api.Models.GenerateContent(ctx, c.model, contents, cfg)
	res := Result{LatencyMs: time.Since(start).Milliseconds()}
	if err != nil {
		return res, err
	}
	if u := resp.UsageMetadata; u != nil {
		res.InputTokens = int(u.PromptTokenCount)
		res.OutputTokens = int(u.CandidatesTokenCount + u.ThoughtsTokenCount)
	}
	res.CostUSD = c.Cost(res.InputTokens, res.OutputTokens)
	res.Text = strings.TrimSpace(resp.Text())
	if res.Text == "" {
		res.Text = "ขออภัยครับ ตอนนี้ยังตอบไม่ได้ รบกวนติดต่อเจ้าหน้าที่ที่ 02-000-0000 ครับ"
		return res, nil
	}

	c.mu.Lock()
	h := append(c.history[userID], turn{user: text, model: res.Text})
	if len(h) > maxTurns {
		h = h[len(h)-maxTurns:]
	}
	c.history[userID] = h
	c.mu.Unlock()
	return res, nil
}

func (c *Client) Cost(inputTokens, outputTokens int) float64 {
	return float64(inputTokens)/1e6*c.price.InputPerM + float64(outputTokens)/1e6*c.price.OutputPerM
}
