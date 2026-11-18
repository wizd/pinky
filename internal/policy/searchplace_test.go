package policy

import (
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// The phone of GitHub issue #156 is 1080x2400 (Xiaomi 12X).
const issueW, issueH = 1080, 2400

func rect(x, y, w, h int) screen.Rect { return screen.Rect{X: x, Y: y, W: w, H: h} }

// pddBox is Pinduoduo's search box as uiautomator reads it without Pinky's
// accessibility service: an EditText whose text is a suggested term in
// place of a hint, and whose resource id says nothing.
func pddBox() screen.Element {
	return screen.Element{Ref: 2, Role: screen.RoleInput, Label: "羽绒服女冬季", ID: "com.xunmeng.pinduoduo:id/pdd",
		Bounds: rect(210, 114, 570, 96), States: []string{screen.StateFocused, screen.StateEditable}}
}

// pddSearch is Pinduoduo's search page: back, the box with a camera icon
// in it, "搜索" beside it, and the history and suggestions below. Options
// drop the button or the headings.
func pddSearch(button, headings bool) *screen.Screen {
	els := []screen.Element{
		{Ref: 1, Role: screen.RoleButton, Label: "返回", Bounds: rect(0, 108, 120, 108)},
		pddBox(),
		{Ref: 3, Role: screen.RoleButton, Bounds: rect(792, 132, 60, 60)}, // the camera, no label
	}
	if button {
		els = append(els, screen.Element{Ref: 4, Role: screen.RoleButton, Label: "搜索", Bounds: rect(912, 126, 144, 72)})
	}
	if headings {
		els = append(els,
			screen.Element{Role: screen.RoleText, Label: "历史搜索", Bounds: rect(36, 270, 200, 60)},
			screen.Element{Ref: 5, Role: screen.RoleButton, Label: "羽绒服", Bounds: rect(36, 350, 160, 70)},
			screen.Element{Ref: 6, Role: screen.RoleButton, Label: "保温杯", Bounds: rect(220, 350, 160, 70)},
			screen.Element{Role: screen.RoleText, Label: "搜索发现", Bounds: rect(36, 470, 200, 60)},
			screen.Element{Ref: 7, Role: screen.RoleButton, Label: "加绒卫衣", Bounds: rect(36, 550, 400, 70)},
		)
	} else {
		els = append(els, screen.Element{Ref: 5, Role: screen.RoleButton, Label: "羽绒服", Bounds: rect(36, 350, 160, 70)})
	}
	return &screen.Screen{App: screen.App{Name: "拼多多", ID: "com.xunmeng.pinduoduo"}, Width: issueW, Height: issueH,
		Keyboard: true, Elements: els}
}

// A session read-only types into Pinduoduo's search box, which names
// itself by neither its label nor its id (GitHub issue #156), and into no
// other field that only sits near a search word.
func TestReadOnlySearchBoxByPlace(t *testing.T) {
	chat := screen.App{Name: "Chatter", ID: "com.example.chatter"}
	shop := screen.App{Name: "Dinely", ID: "com.example.dinely"}
	focused := []string{screen.StateFocused, screen.StateEditable}
	editable := []string{screen.StateEditable}
	scr := func(app screen.App, els ...screen.Element) *screen.Screen {
		return &screen.Screen{App: app, Width: issueW, Height: issueH, Keyboard: true, Elements: els}
	}

	// A chat: a search icon in the top bar, the composer above the keyboard.
	composer := screen.Element{Ref: 3, Role: screen.RoleInput, Label: "发消息", Bounds: rect(120, 1380, 700, 100), States: focused}
	chatScreen := scr(chat,
		screen.Element{Ref: 1, Role: screen.RoleButton, Label: "返回", Bounds: rect(0, 108, 120, 108)},
		screen.Element{Ref: 2, Role: screen.RoleButton, Label: "iv_search", ID: "com.example.chatter:id/iv_search", Bounds: rect(960, 120, 96, 96)},
		screen.Element{Role: screen.RoleText, Label: "帮我搜索一下这个", Bounds: rect(36, 600, 600, 80)},
		composer,
		screen.Element{Ref: 4, Role: screen.RoleButton, Label: "发送", Bounds: rect(840, 1390, 200, 80)},
	)
	// A comment box at the top of a post's page, "发布" beside it, and a
	// search link elsewhere.
	comment := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "说点什么…", Bounds: rect(36, 200, 800, 100), States: focused}
	commentScreen := scr(chat,
		comment,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "发布", Bounds: rect(860, 210, 180, 80)},
		screen.Element{Ref: 4, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 1000, 180, 80)},
		screen.Element{Role: screen.RoleText, Label: "搜索聊天记录", Bounds: rect(36, 1200, 400, 60)},
	)
	// The same box with a "Search chat history" link on its row.
	commentRow := scr(chat,
		comment,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索聊天记录", Bounds: rect(860, 210, 180, 80)},
	)
	// An address form at the top of the page, a search button on its first
	// row, as an address book's own search.
	name := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "收货人", Bounds: rect(36, 200, 800, 100), States: focused}
	addressScreen := scr(shop,
		name,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 210, 180, 80)},
		screen.Element{Ref: 4, Role: screen.RoleInput, Label: "手机号码", Bounds: rect(36, 320, 800, 100), States: editable},
		screen.Element{Ref: 5, Role: screen.RoleInput, Label: "详细地址", Bounds: rect(36, 440, 800, 100), States: editable},
	)
	// A payment's amount field alone at the top, with a search button.
	amount := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "¥ 200", Content: "¥ 200", Bounds: rect(36, 200, 800, 100), States: focused}
	payScreen := scr(shop,
		amount,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 210, 180, 80)},
		screen.Element{Ref: 4, Role: screen.RoleButton, Label: "确认付款", Bounds: rect(36, 1300, 1000, 120)},
	)
	// A sign-in's code field alone at the top, with "历史搜索" on screen.
	code := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "验证码", Bounds: rect(36, 200, 800, 100), States: focused}
	loginScreen := scr(shop,
		code,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 210, 180, 80)},
		screen.Element{Role: screen.RoleText, Label: "历史搜索", Bounds: rect(36, 400, 200, 60)},
	)
	// A sign-in's phone number at the top, the code field below.
	phone := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "请输入手机号", Bounds: rect(36, 200, 800, 100), States: focused}
	phoneScreen := scr(shop,
		phone,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 210, 180, 80)},
		screen.Element{Ref: 4, Role: screen.RoleInput, Label: "验证码", Bounds: rect(36, 320, 800, 100), States: editable},
	)
	// A tall note field at the top, a search button beside it.
	note := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "写点什么", Bounds: rect(36, 150, 800, 400), States: focused}
	noteScreen := scr(shop,
		note,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 210, 180, 80)},
	)
	// A field halfway down, with a search button on its row.
	mid := screen.Element{Ref: 2, Role: screen.RoleInput, Label: "备注", Bounds: rect(36, 1100, 800, 100), States: focused}
	midScreen := scr(shop,
		mid,
		screen.Element{Ref: 3, Role: screen.RoleButton, Label: "搜索", Bounds: rect(860, 1110, 180, 80)},
	)

	box := pddBox()
	e := Default()
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		target screen.Element
		s      *screen.Screen
		want   Decision
	}{
		{"Pinduoduo's search page", box, pddSearch(true, true), Allow},
		{"Pinduoduo, the search button only", box, pddSearch(true, false), Allow},
		{"Pinduoduo, the headings only", box, pddSearch(false, true), Allow},
		{"Pinduoduo, no search button or heading", box, pddSearch(false, false), Deny},
		{"a chat's composer", composer, chatScreen, Deny},
		{"a comment box at the top", comment, commentScreen, Deny},
		{"a comment box beside a longer search link", comment, commentRow, Deny},
		{"an address form", name, addressScreen, Deny},
		{"a payment's amount", amount, payScreen, Deny},
		{"a sign-in's code", code, loginScreen, Deny},
		{"a sign-in's phone number", phone, phoneScreen, Deny},
		{"a tall note field", note, noteScreen, Deny},
		{"a field halfway down", mid, midScreen, Deny},
	} {
		target := tc.target
		in := Input{Op: OpType, App: tc.s.App, Text: "保温杯", Target: &target, Screen: tc.s, ReadOnly: true, ReadOnlyAccess: true}
		if got := IsSearchBox(&target, tc.s); got != (tc.want == Allow) {
			t.Errorf("%s: IsSearchBox %v", tc.name, got)
		}
		v := e.Evaluate(in)
		// A code field is refused before read-only mode is asked.
		if v.Decision != tc.want || (tc.want == Deny && v.Rule != RuleReadOnly && v.Rule != RuleCredentials) {
			t.Errorf("%s: %+v, want %v", tc.name, v, tc.want)
		}
	}
}

// A read-only refusal says where read-only comes from and where it
// changes: the agent's access (Settings › Agents), the agent's own
// request, or Read-only mode (Settings › Safety). Asking the person
// through phone_request_approval lifts none (GitHub issue #156).
func TestReadOnlyReason(t *testing.T) {
	e := Default()
	if err := e.SetPreset(PresetPayments); err != nil {
		t.Fatal(err)
	}
	chat := screen.App{Name: "Chatter", ID: "com.example.chatter"}
	field := screen.Element{Role: screen.RoleInput, Label: "Message", Bounds: rect(36, 1380, 800, 100), States: []string{screen.StateFocused}}
	in := Input{Device: "pixel", Op: OpType, App: chat, Text: "hi", Target: &field,
		Screen: &screen.Screen{App: chat, Width: issueW, Height: issueH, Elements: []screen.Element{field}}}

	access := in
	access.ReadOnly, access.ReadOnlyAccess = true, true
	asked := in
	asked.ReadOnly = true
	for _, tc := range []struct {
		name string
		in   Input
		has  []string
		not  []string
	}{
		{"access", access, []string{"this agent has read-only access", "Settings › Agents", "phone_request_approval"}, []string{"Settings › Safety", "read_only"}},
		{"asked", asked, []string{"this task is read-only", "read_only", "phone_request_approval"}, []string{"Settings › Agents", "Settings › Safety"}},
	} {
		v := e.Evaluate(tc.in)
		if v.Decision != Deny || v.Rule != RuleReadOnly {
			t.Fatalf("%s: %+v", tc.name, v)
		}
		for _, s := range tc.has {
			if !strings.Contains(strings.ToLower(v.Reason), strings.ToLower(s)) {
				t.Errorf("%s: %q lacks %q", tc.name, v.Reason, s)
			}
		}
		for _, s := range tc.not {
			if strings.Contains(v.Reason, s) {
				t.Errorf("%s: %q has %q", tc.name, v.Reason, s)
			}
		}
	}

	if err := e.SetReadOnly("", true); err != nil {
		t.Fatal(err)
	}
	v := e.Evaluate(in)
	if v.Decision != Deny || !strings.Contains(v.Reason, "Read-only mode is on in Pinky's Settings › Safety") ||
		strings.Contains(v.Reason, "Settings › Agents") {
		t.Errorf("global: %+v", v)
	}
	v = e.Evaluate(access)
	if !strings.Contains(v.Reason, "Settings › Agents") || !strings.Contains(v.Reason, "Settings › Safety") {
		t.Errorf("both: %+v", v)
	}
}
