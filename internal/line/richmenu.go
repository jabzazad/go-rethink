package line

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Rich menu types, see https://developers.line.biz/en/reference/messaging-api/#rich-menu

type RichMenu struct {
	Size        Size   `json:"size"`
	Selected    bool   `json:"selected"`
	Name        string `json:"name"`
	ChatBarText string `json:"chatBarText"`
	Areas       []Area `json:"areas"`
}

type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Area struct {
	Bounds Bounds `json:"bounds"`
	Action Action `json:"action"`
}

type Bounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Action is a "message" (sends Text as the user) or "postback" (sends Data silently,
// DisplayText is shown in the chat).
type Action struct {
	Type        string `json:"type"`
	Label       string `json:"label,omitempty"`
	Text        string `json:"text,omitempty"`
	Data        string `json:"data,omitempty"`
	DisplayText string `json:"displayText,omitempty"`
}

// CreateRichMenu creates the menu and returns its ID.
func (c *Client) CreateRichMenu(ctx context.Context, m RichMenu) (string, error) {
	body, _ := json.Marshal(m)
	var out struct {
		RichMenuID string `json:"richMenuId"`
	}
	if err := c.do(ctx, http.MethodPost, apiBase+"/richmenu", "application/json", body, &out); err != nil {
		return "", err
	}
	return out.RichMenuID, nil
}

// UploadRichMenuImage uploads a PNG or JPEG (max 1 MB) matching the menu size.
func (c *Client) UploadRichMenuImage(ctx context.Context, id, contentType string, img []byte) error {
	return c.do(ctx, http.MethodPost, apiDataBase+"/richmenu/"+id+"/content", contentType, img, nil)
}

// SetDefaultRichMenu shows the menu to every user who has no per-user menu.
func (c *Client) SetDefaultRichMenu(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, apiBase+"/user/all/richmenu/"+id, "", nil, nil)
}

func (c *Client) ListRichMenus(ctx context.Context) ([]string, error) {
	var out struct {
		RichMenus []struct {
			RichMenuID string `json:"richMenuId"`
			Name       string `json:"name"`
		} `json:"richmenus"`
	}
	if err := c.do(ctx, http.MethodGet, apiBase+"/richmenu/list", "", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.RichMenus))
	for _, m := range out.RichMenus {
		ids = append(ids, m.RichMenuID+" "+m.Name)
	}
	return ids, nil
}

func (c *Client) DeleteRichMenu(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, apiBase+"/richmenu/"+id, "", nil, nil)
}

func (c *Client) do(ctx context.Context, method, url, contentType string, body []byte, out any) error {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: status %d: %s", method, url, resp.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
