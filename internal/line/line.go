// Package line handles LINE Messaging API webhook verification and replies.
package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	apiBase     = "https://api.line.me/v2/bot"
	apiDataBase = "https://api-data.line.me/v2/bot"
	replyURL    = apiBase + "/message/reply"
)

// WebhookBody is the payload LINE POSTs to the webhook URL.
type WebhookBody struct {
	Destination string  `json:"destination"`
	Events      []Event `json:"events"`
}

type Event struct {
	Type       string    `json:"type"`
	ReplyToken string    `json:"replyToken"`
	Source     Source    `json:"source"`
	Message    *Message  `json:"message,omitempty"`
	Postback   *Postback `json:"postback,omitempty"`
}

type Postback struct {
	Data string `json:"data"`
}

type Source struct {
	Type   string `json:"type"`
	UserID string `json:"userId"`
}

type Message struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

// VerifySignature checks X-Line-Signature: base64(HMAC-SHA256(channelSecret, body)).
func VerifySignature(channelSecret string, body []byte, signature string) bool {
	mac := hmac.New(sha256.New, []byte(channelSecret))
	mac.Write(body)
	got, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	return hmac.Equal(mac.Sum(nil), got)
}

type Client struct {
	accessToken string
	http        *http.Client
}

func NewClient(accessToken string) *Client {
	return &Client{accessToken: accessToken, http: &http.Client{Timeout: 10 * time.Second}}
}

// ReplyText sends one text message using a reply token (valid ~1 minute, single use).
func (c *Client) ReplyText(ctx context.Context, replyToken, text string) error {
	// LINE text messages are capped at 5000 characters.
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:4997]) + "..."
	}
	payload, _ := json.Marshal(map[string]any{
		"replyToken": replyToken,
		"messages":   []map[string]string{{"type": "text", "text": text}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, replyURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("line reply: status %d: %s", resp.StatusCode, b)
	}
	return nil
}
