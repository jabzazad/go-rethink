package decision

// Example is one labelled message.
type Example struct {
	Text  string `json:"text"`
	Label string `json:"label"`
}

// Corpus is what the Rethink engine searches. Add real chat logs here to make it smarter.
// Note the "ai" rows: long or job-specific messages that happen to contain an intent
// keyword ("ราคา", "สถานะ") must still go to the AI.
var Corpus = []Example{
	// greeting
	{"สวัสดีครับ", "greeting"}, {"สวัสดีค่ะ", "greeting"}, {"หวัดดี", "greeting"},
	{"hello", "greeting"}, {"hi there", "greeting"}, {"ดีครับ", "greeting"},
	{"สวัสดีตอนเช้าครับ", "greeting"}, {"hey", "greeting"},
	// menu
	{"เมนู", "menu"}, {"ขอดูเมนู", "menu"}, {"ช่วยอะไรได้บ้าง", "menu"},
	{"menu", "menu"}, {"help", "menu"}, {"ทำอะไรได้บ้าง", "menu"}, {"มีบริการอะไรบ้าง", "menu"},
	// hours
	{"เปิดกี่โมง", "hours"}, {"ปิดกี่โมง", "hours"}, {"เวลาทำการ", "hours"},
	{"เปิดวันไหนบ้าง", "hours"}, {"วันอาทิตย์เปิดไหม", "hours"}, {"opening hours", "hours"},
	{"เปิดทำการกี่โมงถึงกี่โมง", "hours"},
	// contact
	{"ขอเบอร์โทร", "contact"}, {"ติดต่อเจ้าหน้าที่", "contact"}, {"อยากคุยกับคน", "contact"},
	{"ขอคุยกับแอดมิน", "contact"}, {"contact", "contact"}, {"เบอร์ติดต่อ", "contact"},
	{"ขอสายเจ้าหน้าที่หน่อย", "contact"},
	// price
	{"ราคาเท่าไหร่", "price"}, {"ค่าบริการเท่าไร", "price"}, {"กี่บาท", "price"},
	{"price", "price"}, {"ราคาเริ่มต้นเท่าไหร่", "price"}, {"คิดค่าบริการยังไง", "price"},
	{"ขอราคาหน่อย", "price"},
	// status
	{"เช็คสถานะงาน", "status"}, {"งานถึงไหนแล้ว", "status"}, {"สถานะงาน", "status"},
	{"ช่างจะมากี่โมง", "status"}, {"track my job", "status"}, {"ติดตามงาน", "status"},
	{"งานของผมเป็นยังไงบ้าง", "status"},
	// thanks
	{"ขอบคุณครับ", "thanks"}, {"ขอบคุณค่ะ", "thanks"}, {"ขอบใจ", "thanks"},
	{"thank you", "thanks"}, {"thx", "thanks"}, {"ขอบคุณมากครับ", "thanks"},
	// ai: open-ended / detailed / job-specific
	{"ห้องน้ำรั่วซึมจากเพดานชั้นสอง ควรเริ่มตรวจจากตรงไหนก่อนดี", "ai"},
	{"แอร์มีน้ำหยดตลอดเวลาเกิดจากอะไรได้บ้าง", "ai"},
	{"อยากรู้ว่าราคาติดตั้งแอร์ 2 เครื่องพร้อมเดินท่อใหม่ทั้งหมดประมาณเท่าไหร่", "ai"},
	{"บ้านไฟตกบ่อยมากตอนเปิดแอร์พร้อมกันหลายเครื่อง ต้องแก้ยังไง", "ai"},
	{"ผนังมีรอยร้าวแนวทแยงข้างหน้าต่าง อันตรายไหม", "ai"},
	{"ควรเลือกกระเบื้องแบบไหนสำหรับห้องน้ำที่ลื่นง่าย", "ai"},
	{"ช่างมาซ่อมไปแล้วแต่ปั๊มน้ำยังดังอยู่เลย ทำยังไงดี", "ai"},
	{"เปรียบเทียบสีทาภายนอกแบบอะคริลิกกับแบบน้ำมันให้หน่อย", "ai"},
	{"how do I fix a leaking kitchen faucet", "ai"},
	{"what's the difference between inverter and non-inverter air conditioners", "ai"},
	{"หลังคาโรงรถเป็นสนิม ควรทาสีทับหรือเปลี่ยนใหม่", "ai"},
	{"ราคาเปลี่ยนหลังคาเมทัลชีท 80 ตารางเมตร พร้อมรื้อของเก่า รวมค่าแรงไหม", "ai"},
	{"สถานะงานผมขึ้นว่าเสร็จแล้วแต่ช่างยังไม่ได้มาเลย เกิดอะไรขึ้น", "ai"},
	{"ติดตั้งโซลาร์เซลล์ 5kW คุ้มไหมถ้าค่าไฟเดือนละ 3000", "ai"},
	{"น้ำประปาไหลแรงไม่พอชั้นบน ต้องใช้ปั๊มแบบไหน", "ai"},
	{"มดขึ้นบ้านเยอะมาก มีวิธีกำจัดถาวรไหม", "ai"},
}

// EvalSet is held out from the corpus (different phrasings) and is used to
// measure every decider's accuracy on the dashboard.
var EvalSet = []Example{
	{"สวัสดีครับแอดมิน", "greeting"},
	{"หวัดดีค่ะ", "greeting"},
	{"good morning", "greeting"},
	{"มีเมนูอะไรบ้าง", "menu"},
	{"บอทช่วยอะไรได้", "menu"},
	{"เปิดกี่โมงครับ", "hours"},
	{"วันเสาร์เปิดไหมคะ", "hours"},
	{"ร้านปิดกี่โมง", "hours"},
	{"ขอเบอร์ติดต่อหน่อยครับ", "contact"},
	{"ขอคุยกับเจ้าหน้าที่", "contact"},
	{"ค่าบริการเริ่มต้นกี่บาท", "price"},
	{"ราคาประมาณเท่าไหร่ครับ", "price"},
	{"งานผมถึงไหนแล้วครับ", "status"},
	{"เช็คสถานะงานให้หน่อย", "status"},
	{"ขอบคุณมากค่ะ", "thanks"},
	{"thanks a lot", "thanks"},
	{"ท่อใต้ซิงค์ล้างจานรั่ว ต้องเปลี่ยนอะไหล่ตัวไหน", "ai"},
	{"แอร์เปิดแล้วมีกลิ่นอับ ล้างเองได้ไหม", "ai"},
	{"ราคาทาสีบ้านสองชั้นพื้นที่ 200 ตารางเมตรทั้งภายในและภายนอกประมาณกี่บาท", "ai"},
	{"เบรกเกอร์ตัดบ่อยตอนใช้เครื่องทำน้ำอุ่น เกิดจากอะไร", "ai"},
	{"ปูกระเบื้องทับกระเบื้องเดิมได้ไหม มีข้อเสียอะไร", "ai"},
	{"สถานะขึ้นว่ายกเลิกงาน แต่ผมไม่ได้กดยกเลิกเลย", "ai"},
	{"should I use epoxy or polyurethane for a garage floor", "ai"},
	{"ฝ้าเพดานบวมเป็นคราบน้ำ ต้องรื้อทั้งแผ่นไหม", "ai"},
}
