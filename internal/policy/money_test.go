package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// dex is an app outside the finance category, as a wallet or exchange the
// table does not know is.
var dex = screen.App{Name: "Example DEX", ID: "com.example.dex"}

func tapOn(app screen.App, e screen.Element, others ...screen.Element) Input {
	if e.Bounds.Empty() {
		e.Bounds = screen.Rect{W: 200, H: 80}
	}
	return Input{Op: OpTap, App: app, Target: &e, Screen: &screen.Screen{App: app, Elements: append([]screen.Element{e}, others...)}}
}

// TestMoneyMovingButtonsAskByDefault: buttons that move money are payments,
// which the default (payments only) asks about, with or without an amount
// on the screen (field test bug 1).
func TestMoneyMovingButtonsAskByDefault(t *testing.T) {
	e := Default()
	payments := []string{
		"Sell", "Sell all", "Swap", "Confirm swap", "Slide to swap", "Trade", "Repay", "Borrow", "Stake",
		"Unstake", "Bridge", "Withdraw", "Transfer", "Buy", "Pay", "Deposit", "Place order", "Top up",
		"还款", "立即还款", "分期还款", "去分期", "立即分期", "借钱", "买入", "卖出", "赎回", "申购", "闪兑",
		"预约还款", "转账", "确认支付", "发红包", "发送红包", "塞钱进红包", "發紅包", "塞錢進紅包", "賣出", "売却", "返済", "매도", "상환", "Vender", "Verkaufen",
	}
	for _, l := range payments {
		v := e.Evaluate(tapOn(dex, *button(l)))
		if v.Decision != Ask || v.Rule != "Payments" || !v.Has(Payment) {
			t.Errorf("%q: %v %q (kinds %v), want a payment that asks", l, v.Decision, v.Rule, v.Kinds)
		}
	}

	// Converting, exchanging, redeeming and claiming are commits; a payment
	// only next to an amount, coins included.
	for _, l := range []string{"Convert", "Claim", "Redeem", "兑换"} {
		if v := e.Evaluate(tapOn(dex, *button(l))); v.Decision != Allow || !v.Has(Commit) || v.Has(Payment) {
			t.Errorf("%q without an amount: %+v", l, v)
		}
		amount := screen.Element{Role: screen.RoleText, Label: "0.5 ETH ≈ 1,200 USDC"}
		if v := e.Evaluate(tapOn(dex, *button(l), amount)); v.Decision != Ask || v.Rule != "Payments" {
			t.Errorf("%q next to an amount in coins: %+v", l, v)
		}
	}

	// A tab moves between pages: "Trade" or "交易" in a tab bar asks
	// nothing, whatever the preset.
	for _, l := range []string{"Trade", "Swap", "Buy", "Sell"} {
		tab := screen.Element{Role: screen.RoleTab, Label: l}
		for _, e := range []*Engine{Default(), commitsEngine(t)} {
			if v := e.Evaluate(tapOn(dex, tab)); v.Decision != Allow || len(v.Kinds) != 0 {
				t.Errorf("tab %q, preset %d: %+v", l, e.Preset(), v)
			}
		}
	}

	// Places stay plain: history, details and prices.
	for _, l := range []string{"交易记录", "还款记录", "Order history", "Trade details", "Markets"} {
		if v := e.Evaluate(tapOn(dex, *button(l))); v.Decision != Allow {
			t.Errorf("%q: %+v", l, v)
		}
	}
}

func TestCoinAmounts(t *testing.T) {
	for _, s := range []string{"0.5 ETH", "1,200.50 USDC", "BTC 0.01", "≈ 12 USDT", "2.5 SOL", "100 USDe"} {
		if !hasCurrency(s) {
			t.Errorf("%q is not an amount", s)
		}
	}
	for _, s := range []string{"ETH address", "Ethereum", "Version 2.0", "Solana network", "BTC/USDT"} {
		if hasCurrency(s) {
			t.Errorf("%q is an amount", s)
		}
	}
}

// TestDeleteWords: British iPhones label the delete icon "bin"; "Remove"
// and "移除" delete too (field test bug 3). They are commits, which preset 1
// asks about; the default asks before payments only.
func TestDeleteWords(t *testing.T) {
	commits := commitsEngine(t)
	for _, l := range []string{"bin", "Bin", "trash", "Trash", "Remove", "移除", "Move to Bin", "Move to Trash", "Entfernen"} {
		if v := commits.Evaluate(tapOn(dex, *button(l))); v.Decision != Ask || v.Rule != "Commits" {
			t.Errorf("%q with preset 1: %+v", l, v)
		}
		if v := Default().Evaluate(tapOn(dex, *button(l))); v.Decision != Allow || !v.Has(Commit) {
			t.Errorf("%q by default: %+v", l, v)
		}
	}
}

// TestOCRText judges text that only OCR read, as on a web view, by the
// whole line and by its pieces (field test bug 4, policy's half).
func TestOCRText(t *testing.T) {
	ocr := func(text string) screen.Element {
		return screen.Element{Role: screen.RoleText, Label: text, Source: screen.SourceOCR, Bounds: screen.Rect{W: 300, H: 40}}
	}
	cases := []struct {
		text string
		rule string // "" for a plain tap
	}{
		{"立即还款", "Payments"},
		{"还款 ¥1,234.00", "Payments"},
		{"账单 还款 分期还款 去分期", "Payments"},
		{"本月账单 ¥1,234.00", ""},
		{"交易记录", ""},
		{"Sell all", "Payments"},
		{"Cancel Delete", "Commits"},
		{"You can delete your account at any time in Settings", ""},
	}
	for _, c := range cases {
		v := commitsEngine(t).Evaluate(tapOn(dex, ocr(c.text)))
		switch {
		case c.rule == "" && v.Decision != Allow:
			t.Errorf("OCR %q asks: %+v", c.text, v)
		case c.rule != "" && (v.Decision != Ask || v.Rule != c.rule):
			t.Errorf("OCR %q: %v %q, want %q", c.text, v.Decision, v.Rule, c.rule)
		}
	}
	// By default the payments among them ask.
	if v := Default().Evaluate(tapOn(dex, ocr("账单 还款 分期还款 去分期"))); v.Decision != Ask || v.Rule != "Payments" {
		t.Fatalf("OCR repayment by default: %+v", v)
	}
}

// TestRedPacketsReceivedAskNothing: opening or claiming a red packet someone
// sent brings money in, and a page of past red packets moves none.
func TestRedPacketsReceivedAskNothing(t *testing.T) {
	e := Default()
	for _, l := range []string{"领红包", "开红包", "红包记录", "紅包紀錄"} {
		if v := e.Evaluate(tapOn(dex, *button(l))); v.Decision != Allow || v.Has(Payment) {
			t.Errorf("%q: %+v", l, v)
		}
	}
}

// TestHomeScreenLongPressIsNoPayment: a long press on a payment app's icon
// on the home screen opens its menu, and asks nothing (DIYgod/pinky#157);
// the same long press inside an app, and a tap on the icon, are judged as
// before.
func TestHomeScreenLongPressIsNoPayment(t *testing.T) {
	e := Default()
	homes := []screen.App{
		{Name: "Home Screen", ID: "com.apple.springboard"},
		{Name: "Pixel Launcher", ID: "com.google.android.apps.nexuslauncher"},
	}
	for _, home := range homes {
		for _, l := range []string{"PayPal", "Pay", "支付宝", "Apple Pay"} {
			in := tapOn(home, *button(l))
			in.LongPress = true
			if v := e.Evaluate(in); v.Decision != Allow || len(v.Kinds) != 0 {
				t.Errorf("long press on %q in %s: %+v", l, home.ID, v)
			}
		}
	}
	in := tapOn(dex, *button("Pay"))
	in.LongPress = true
	if v := e.Evaluate(in); v.Decision != Ask || !v.Has(Payment) {
		t.Errorf("long press on Pay inside an app: %+v", v)
	}
	if v := e.Evaluate(tapOn(homes[0], *button("Pay"))); !v.Has(Payment) {
		t.Errorf("tap on Pay on the home screen: %+v", v)
	}
}
