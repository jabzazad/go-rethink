// Package richmenu defines the LINE rich menu (one button per bot feature) and renders its image.
//
// Every button sends a fixed text (or a postback) that goes through the decision
// layer like any typed message. Because each button's meaning is known, taps give
// ground truth: the gateway can score whichever decider handled the tap.
package richmenu

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"poc-gateway/internal/line"
)

// PostbackTest asks the gateway for a random held-out test question.
const PostbackTest = "action=test"

type Button struct {
	Title    string // big label on the image
	Subtitle string
	// Text is what the tap sends as the user's message. Phrased differently from the
	// Rethink corpus so taps don't hand Rethink an exact match.
	Text     string
	Expected string // correct decision label for Text
	Postback string // if set, a postback button instead of a message button
	Color    color.RGBA
}

var Buttons = []Button{
	{Title: "เวลาทำการ", Subtitle: "Opening hours", Text: "ขอทราบเวลาทำการหน่อยครับ", Expected: "hours", Color: rgb(0x1f, 0x9d, 0x6b)},
	{Title: "ค่าบริการ", Subtitle: "Price", Text: "ค่าบริการเท่าไหร่ครับ", Expected: "price", Color: rgb(0x25, 0x63, 0xeb)},
	{Title: "เช็คสถานะงาน", Subtitle: "Job status", Text: "อยากเช็คสถานะงานครับ", Expected: "status", Color: rgb(0xd9, 0x77, 0x06)},
	{Title: "ติดต่อเจ้าหน้าที่", Subtitle: "Talk to staff", Text: "ขอติดต่อเจ้าหน้าที่ครับ", Expected: "contact", Color: rgb(0xdb, 0x27, 0x77)},
	{Title: "เมนูช่วยเหลือ", Subtitle: "What can you do?", Text: "บอทช่วยอะไรได้บ้างครับ", Expected: "menu", Color: rgb(0x47, 0x55, 0x69)},
	{Title: "ทดสอบสุ่ม", Subtitle: "Random test question", Postback: PostbackTest, Color: rgb(0x6d, 0x5a, 0xe6)},
}

// Expected returns the known correct label when text is exactly a button's text.
func Expected(text string) (string, bool) {
	for _, b := range Buttons {
		if b.Postback == "" && b.Text == text {
			return b.Expected, true
		}
	}
	return "", false
}

const (
	width, height = 2500, 1686
	cols, rows    = 3, 2
)

// Menu builds the rich menu definition (3 x 2 grid).
func Menu() line.RichMenu {
	m := line.RichMenu{
		Size:        line.Size{Width: width, Height: height},
		Selected:    true,
		Name:        "decision-gateway-poc",
		ChatBarText: "เมนู",
	}
	cw, rh := width/cols, height/rows
	for i, b := range Buttons {
		area := line.Area{Bounds: line.Bounds{X: (i % cols) * cw, Y: (i / cols) * rh, Width: cw, Height: rh}}
		if b.Postback != "" {
			area.Action = line.Action{Type: "postback", Label: b.Subtitle, Data: b.Postback, DisplayText: b.Title}
		} else {
			area.Action = line.Action{Type: "message", Label: b.Subtitle, Text: b.Text}
		}
		m.Areas = append(m.Areas, area)
	}
	return m
}

// Render draws the menu image as PNG. fontPath must be a TTF/OTF with Thai glyphs
// (e.g. C:\Windows\Fonts\LeelawUI.ttf or NotoSansThai-Regular.ttf).
func Render(fontPath string) ([]byte, error) {
	raw, err := os.ReadFile(fontPath)
	if err != nil {
		return nil, fmt.Errorf("read font: %w (set RICHMENU_FONT to a Thai-capable .ttf, or RICHMENU_IMAGE to your own 2500x1686 image)", err)
	}
	f, err := opentype.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}
	big, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 120, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	small, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 64, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{rgb(0xf3, 0xf4, 0xf6)}, image.Point{}, draw.Src)
	cw, rh := width/cols, height/rows
	const pad = 18
	for i, b := range Buttons {
		x0, y0 := (i%cols)*cw, (i/cols)*rh
		tile := image.Rect(x0+pad, y0+pad, x0+cw-pad, y0+rh-pad)
		roundRect(img, tile, 48, b.Color)
		cx := x0 + cw/2
		drawCentered(img, big, b.Title, cx, y0+rh/2+20, color.White)
		drawCentered(img, small, b.Subtitle, cx, y0+rh/2+130, color.NRGBA{255, 255, 255, 215})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func drawCentered(dst draw.Image, face font.Face, s string, cx, baseline int, c color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face}
	w := d.MeasureString(s)
	d.Dot = fixed.Point26_6{X: fixed.I(cx) - w/2, Y: fixed.I(baseline)}
	d.DrawString(s)
}

func roundRect(dst *image.RGBA, r image.Rectangle, rad int, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			dx, dy := 0, 0
			if x < r.Min.X+rad {
				dx = r.Min.X + rad - x
			} else if x >= r.Max.X-rad {
				dx = x - (r.Max.X - rad - 1)
			}
			if y < r.Min.Y+rad {
				dy = r.Min.Y + rad - y
			} else if y >= r.Max.Y-rad {
				dy = y - (r.Max.Y - rad - 1)
			}
			if dx*dx+dy*dy <= rad*rad {
				dst.SetRGBA(x, y, c)
			}
		}
	}
}

func rgb(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }
