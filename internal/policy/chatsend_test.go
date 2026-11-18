package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

var (
	xiaomei = screen.App{Name: "小美", ID: "com.meituan.android.beam"}
	wechat  = screen.App{Name: "WeChat", ID: "com.tencent.mm"}
	mybank  = screen.App{Name: "Example Bank", ID: "com.example.mybank"}
)

// composer is a chat's text field at the bottom of the screen, holding
// text, with the send button (sendButton) on its row.
func composer(text string) screen.Element {
	return screen.Element{Role: screen.RoleInput, Label: text, Content: text, States: []string{screen.StateEditable},
		Bounds: screen.Rect{X: 40, Y: 2200, W: 800, H: 120}}
}

func sendButton(label string) screen.Element {
	return screen.Element{Ref: 1, Role: screen.RoleButton, Label: label, Bounds: screen.Rect{X: 860, Y: 2210, W: 180, H: 100}}
}

func text(label string, y int) screen.Element {
	return screen.Element{Role: screen.RoleText, Label: label, Bounds: screen.Rect{X: 40, Y: y, W: 900, H: 60}}
}

// TestChatSendIsNoPayment: a chat's send button beside its composer is a
// commit, not a payment, when only the chat's history shows prices
// (GitHub issue #132: "Pay: “发送”?" in Meituan's 小美). The person's rules
// for sending still apply.
func TestChatSendIsNoPayment(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{"小美", tapOn(xiaomei, sendButton("发送"), text("麻辣香锅 ¥39.9 起", 600), text("人均 ¥65 · 距你 1.2km", 700),
			composer("附近有什么不辣的川菜？"))},
		{"WeChat, prices in the history", tapOn(wechat, sendButton("发送"), text("这副耳机 ¥299 值吗", 900),
			text("Black Friday: $49.99", 1000), composer("挺好的"))},
		{"WeChat in English", tapOn(wechat, sendButton("Send"), text("It was $12 at the market", 900), composer("Nice"))},
		{"a search box at the top", tapOn(wechat, sendButton("Send"), text("$12", 900), composer("ok"),
			screen.Element{Role: screen.RoleInput, Label: "Search", States: []string{screen.StateEditable},
				Bounds: screen.Rect{X: 40, Y: 100, W: 1000, H: 100}})},
	}
	for _, c := range cases {
		v := Default().Evaluate(c.in)
		if v.Decision != Allow || !v.Has(Commit) || v.Has(Payment) {
			t.Errorf("%s by default: %v %q (kinds %v), want a commit that is no payment", c.name, v.Decision, v.Rule, v.Kinds)
		}
		if v := commitsEngine(t).Evaluate(c.in); v.Decision != Ask || v.Rule != "Commits" || v.Has(Payment) {
			t.Errorf("%s with preset 1: %v %q (kinds %v), want the commits rule", c.name, v.Decision, v.Rule, v.Kinds)
		}
	}
}

// TestSendStaysPayment: a send button stays a payment whenever it may move
// money: an amount in its label or in a text field, an amount on a screen
// without a composer beside it, a label that names money, a finance app.
func TestSendStaysPayment(t *testing.T) {
	history := text("麻辣香锅 ¥39.9 起", 600)
	amountField := func(content string, y int) screen.Element {
		return screen.Element{Role: screen.RoleInput, Label: "Amount, " + content, Content: content,
			States: []string{screen.StateEditable}, Bounds: screen.Rect{X: 40, Y: y, W: 1000, H: 120}}
	}
	address := screen.Element{Role: screen.RoleInput, Label: "Recipient, 0x3f5C…9a21", Content: "0x3f5C…9a21",
		States: []string{screen.StateEditable}, Bounds: screen.Rect{X: 40, Y: 400, W: 1000, H: 120}}
	wide := screen.Element{Ref: 1, Role: screen.RoleButton, Label: "Send", Bounds: screen.Rect{X: 40, Y: 2200, W: 1000, H: 120}}
	cases := []struct {
		name string
		in   Input
	}{
		{"wallet: the amount typed into a field", tapOn(dex, wide, address, amountField("0.5 ETH", 700), text("≈ $1,500", 850))},
		{"wallet: a number alone in a field on the button's row", tapOn(dex, sendButton("Send"),
			text("Balance 2.1 ETH", 1800), screen.Element{Role: screen.RoleInput, Label: "0.5", Content: "0.5",
				States: []string{screen.StateEditable}, Bounds: screen.Rect{X: 40, Y: 2200, W: 800, H: 120}})},
		{"wallet: confirmation page without a field", tapOn(dex, wide, text("0.5 ETH ≈ $1,500", 600),
			text("To 0x3f5C…9a21", 800), text("Network fee 0.0004 ETH", 900))},
		{"chat: the composer holds an amount", tapOn(wechat, sendButton("发送"), composer("转你 ¥200"))},
		{"chat: the amount in the label", tapOn(wechat, sendButton("Send $20"), history, composer("for lunch"))},
		{"chat: the label names money", tapOn(wechat, sendButton("Send money"), history, composer("lunch"))},
		{"chat: a red packet", tapOn(wechat, sendButton("发送红包"), history, composer("恭喜发财"))},
		{"chat: two fields", tapOn(wechat, sendButton("发送"), history, composer("hi"), amountField("", 1500))},
		{"chat: the field far from the button", tapOn(wechat, sendButton("发送"), history, screen.Element{
			Role: screen.RoleInput, Label: "Note", States: []string{screen.StateEditable}, Bounds: screen.Rect{X: 40, Y: 900, W: 1000, H: 120}})},
		{"chat: no bounds", Input{Op: OpTap, App: wechat, Target: &screen.Element{Ref: 1, Role: screen.RoleButton, Label: "发送"},
			Screen: &screen.Screen{App: wechat, Elements: []screen.Element{history, composer("hi")}}}},
	}
	for _, c := range cases {
		if v := Default().Evaluate(c.in); v.Decision != Ask || v.Rule != "Payments" || !v.Has(Payment) {
			t.Errorf("%s: %v %q (kinds %v), want a payment", c.name, v.Decision, v.Rule, v.Kinds)
		}
	}

	// A finance app's chat, which the person allowed for the task, may
	// move money whatever its send button says.
	in := tapOn(mybank, sendButton("发送"), text("本月账单 ¥1,234.00", 600), composer("能分期吗"))
	in.AllowBlocked = true
	if v := Default().Evaluate(in); v.Decision != Ask || v.Rule != "Payments" || !v.Has(Payment) {
		t.Errorf("finance app chat: %v %q (kinds %v), want a payment", v.Decision, v.Rule, v.Kinds)
	}
}

func TestSendLabels(t *testing.T) {
	for _, l := range []string{"Send", "send", "发送", "發送", "傳送", "送信", "보내기", "전송", "Enviar", "Senden", "Отправить"} {
		if !isSendLabel(l) {
			t.Errorf("%q is no send label", l)
		}
	}
	for _, l := range []string{"Send money", "Send $20", "发送红包", "转账", "Send gift", "Pay", "确认发送", "Senden 5 €", ""} {
		if isSendLabel(l) {
			t.Errorf("%q is a send label", l)
		}
	}
}
