// Package bot is the rule-based chatbot: fixed intents with canned answers.
package bot

import "strings"

// LabelAI is the decision label meaning "not a bot intent, send to the AI".
const LabelAI = "ai"

type Intent struct {
	Name string
	// Description is the rubric Jev sees for this option.
	Description string
	// Keywords drive the keyword baseline decider.
	Keywords []string
	Answer   string
}

var Intents = []Intent{
	{
		Name:        "greeting",
		Description: "A short greeting or hello with no actual question",
		Keywords:    []string{"สวัสดี", "หวัดดี", "hello", "hi", "hey"},
		Answer:      "สวัสดีครับ 👋 พิมพ์ \"เมนู\" เพื่อดูสิ่งที่ผมช่วยได้ หรือถามอะไรก็ได้เลยครับ",
	},
	{
		Name:        "menu",
		Description: "Asks what the bot can do, wants the menu or help options",
		Keywords:    []string{"เมนู", "menu", "help", "ช่วยอะไรได้"},
		Answer:      "ผมช่วยได้เรื่อง:\n• เวลาทำการ\n• ติดต่อเจ้าหน้าที่\n• ราคา / ค่าบริการ\n• เช็คสถานะงาน\nหรือพิมพ์คำถามอื่น ๆ แล้ว AI จะช่วยตอบครับ",
	},
	{
		Name:        "hours",
		Description: "Asks about business / opening hours or which days the company is open",
		Keywords:    []string{"เวลาทำการ", "เปิดกี่โมง", "ปิดกี่โมง", "เปิดวันไหน", "opening hours", "hours"},
		Answer:      "เปิดทำการ จันทร์–เสาร์ 08:30–17:30 น. ครับ",
	},
	{
		Name:        "contact",
		Description: "Wants a phone number or to talk to a human staff member",
		Keywords:    []string{"ติดต่อ", "เบอร์โทร", "คุยกับคน", "เจ้าหน้าที่", "contact", "phone", "agent"},
		Answer:      "ติดต่อเจ้าหน้าที่ได้ที่ 02-000-0000 (จ.–ส. 08:30–17:30) ครับ",
	},
	{
		Name:        "price",
		Description: "A short, general question about price or service fee, with no job-specific details",
		Keywords:    []string{"ราคา", "ค่าบริการ", "กี่บาท", "price", "cost"},
		Answer:      "ค่าบริการขึ้นอยู่กับประเภทงานครับ เริ่มต้น 500 บาท ส่งรายละเอียดงานมาได้เลย เดี๋ยวประเมินให้ครับ",
	},
	{
		Name:        "status",
		Description: "Wants to check the status or progress of an existing job",
		Keywords:    []string{"สถานะ", "เช็คงาน", "งานถึงไหน", "status", "track"},
		Answer:      "ส่งเลขที่งาน (เช่น JOB-12345) มาได้เลยครับ เดี๋ยวเช็คสถานะให้",
	},
	{
		Name:        "thanks",
		Description: "Says thank you, with no further question",
		Keywords:    []string{"ขอบคุณ", "ขอบใจ", "thank", "thx"},
		Answer:      "ยินดีครับ 🙏",
	},
}

// AIDescription is the rubric for the "ai" option.
const AIDescription = "Anything else: an open-ended, detailed, or job-specific question or problem that needs a free-form answer"

// Answer returns the canned reply for an intent label.
func Answer(label string) string {
	for _, in := range Intents {
		if in.Name == label {
			return in.Answer
		}
	}
	return "ขออภัยครับ ผมยังไม่เข้าใจ พิมพ์ \"เมนู\" เพื่อดูสิ่งที่ผมช่วยได้ครับ"
}

// Labels returns every decision label: all intents plus "ai".
func Labels() []string {
	out := make([]string, 0, len(Intents)+1)
	for _, in := range Intents {
		out = append(out, in.Name)
	}
	return append(out, LabelAI)
}

// MatchKeyword returns the best keyword intent and match strength (0..1).
// Strength = matched keyword length / message length, so "ราคา" alone scores 1.0
// while "ราคา" buried in a long paragraph scores low.
func MatchKeyword(text string) (*Intent, float64) {
	t := strings.ToLower(strings.TrimSpace(text))
	n := len([]rune(t))
	if n == 0 {
		return nil, 0
	}
	var best *Intent
	var bestScore float64
	for i := range Intents {
		for _, kw := range Intents[i].Keywords {
			if !strings.Contains(t, kw) {
				continue
			}
			score := min(float64(len([]rune(kw)))/float64(n), 1)
			if score > bestScore {
				best, bestScore = &Intents[i], score
			}
		}
	}
	return best, bestScore
}
