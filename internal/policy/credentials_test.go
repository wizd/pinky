package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestCredentialFieldIdentifiers checks how an input's identifier, which
// agents do not see, counts towards the credentials rule (GitHub issue
// #47: Notes' body on iOS 27.2 was taken for a code field). A code word in
// it counts as a whole word, or joined to a word that says what code it is
// ("smscode"); a word that merely contains one does not make a note or a
// message box a code field. A password word counts anywhere.
func TestCredentialFieldIdentifiers(t *testing.T) {
	for _, c := range []struct {
		label, id string
		want      bool
	}{
		// Real fields, as apps name them.
		{"", "com.example.bank:id/et_verify_code", true},
		{"", "com.example:id/otp_input", true},
		{"", "otpInput", true},
		{"", "OTPCodeField", true},
		{"", "smsCodeEditText", true},
		{"", "verificationCode", true},
		{"", "one-time-code", true},
		{"", "twoFactorCode", true},
		{"", "pin_entry", true},
		{"", "PINField", true},
		{"", "passwordField", true},
		{"", "etpassword", true}, // no word breaks: a password word counts anywhere
		{"", "login.passcode", true},
		{"", "codice_sms", true},
		{"", "codigoVerificacion", true},
		{"", "验证码输入框", true},
		// Words joined without breaks, as many Android apps write them.
		{"", "et_smscode", true},
		{"", "verifycode", true},
		{"", "edt_smsverifycode", true},
		{"", "vcode", true},
		{"", "otpinput", true},
		{"", "otp1", true},
		{"", "pin_1", true},
		{"", "pin2", true},
		{"", "code_3", true},
		{"", "2FACode", true},
		{"Code", "", true},
		{"Verification code", "field_1", true},
		// Words that only contain a code word.
		{"Note", "footprintTextView", false},
		{"Note", "rootPaneBody", false},
		{"Note", "EncodedContentView", false},
		{"Message", "unicodeComposer", false},
		{"Note", "barcodeNotes", false},
		{"Note", "spinnerHost", false},
		{"Note", "maintenanceLog", false},
		{"Note", "snapshotPreviewText", false},
		{"Note", "rootpane", false},
		{"Email", "verifiedEmailInput", false},
		{"Address", "geocodeField", false},
		{"", "zipcode", false},
		{"", "promocode", false},
		{"", "qrcode_scan", false},
		// Codes that are not secrets still do not count.
		{"", "promoCodeInput", false},
		{"", "zip_code", false},
		{"", "countryCodePicker", false},
	} {
		e := &screen.Element{Role: screen.RoleInput, Label: c.label, ID: c.id, States: []string{screen.StateEditable}}
		if got := IsCredentialField(e); got != c.want {
			t.Errorf("label %q, id %q: credential %v, want %v", c.label, c.id, got, c.want)
		}
	}
}

// TestCredentialFieldContent checks that a field is judged by its name and
// not by what was typed into it (GitHub issue #67): Pinky refused to type
// more into a message that mentioned "the code review", and into Chrome's
// address bar while it showed a page whose address had "codes" in it. A
// field's name still counts, typed text or not.
func TestCredentialFieldContent(t *testing.T) {
	for _, c := range []struct {
		label, content, id string
		want               bool
	}{
		// Android shows a field's text in place of its hint.
		{"I will bring the code review notes", "I will bring the code review notes", "com.google.android.apps.messaging:id/compose_message_text", false},
		{"github.com/DIYgod/pinky/codes", "github.com/DIYgod/pinky/codes", "com.android.chrome:id/url_bar", false},
		{"Remember the Wi-Fi password", "Remember the Wi-Fi password", "", false},
		{"Pin the PIN to verify", "Pin the PIN to verify", "", false},
		// iOS reads a field as its name, then its value.
		{"Note, Ask Sam for the OTP flow", "Ask Sam for the OTP flow", "", false},
		{"Verification code, 12", "12", "", true},
		{"Password, abc", "abc", "", true},
		// What the person half typed into a code field: the id still says.
		{"12", "12", "com.example:id/et_verify_code", true},
		// Without content, the label is the field's name.
		{"Enter the 6-digit code", "", "otp", true},
		{"Verify code", "", "", true},
	} {
		e := &screen.Element{Role: screen.RoleInput, Label: c.label, Content: c.content, ID: c.id, States: []string{screen.StateEditable}}
		if got := IsCredentialField(e); got != c.want {
			t.Errorf("label %q, content %q, id %q: credential %v, want %v", c.label, c.content, c.id, got, c.want)
		}
	}
}

// TestCredentialFieldIDAsLabel checks fields without a name, which drivers
// label by their identifier (the end of an Android resource id, an iOS
// accessibility identifier): it counts by whole words, like any identifier,
// and not as a label (GitHub issue #47).
func TestCredentialFieldIDAsLabel(t *testing.T) {
	for _, c := range []struct {
		label, id string
		want      bool
	}{
		{"footprint_text", "com.example.notes:id/footprint_text", false},
		{"footprintTextView", "footprintTextView", false},
		{"unicodeComposer", "unicodeComposer", false},
		{"et_verify_code", "com.example.bank:id/et_verify_code", true},
		{"otpInput", "otpInput", true},
		{"passwordField", "passwordField", true},
	} {
		e := &screen.Element{Role: screen.RoleInput, Label: c.label, ID: c.id, States: []string{screen.StateEditable}}
		if got := IsCredentialField(e); got != c.want {
			t.Errorf("label %q, id %q: credential %v, want %v", c.label, c.id, got, c.want)
		}
	}
}

// TestCredentialFieldBarcode: a barcode typed in by hand is no secret.
func TestCredentialFieldBarcode(t *testing.T) {
	for _, label := range []string{"Barcode", "Enter bar code", "Strichcode", "バーコード", "Código de barras"} {
		e := &screen.Element{Role: screen.RoleInput, Label: label, ID: "barcode", States: []string{screen.StateEditable}}
		if IsCredentialField(e) {
			t.Errorf("%q counts as a credential field", label)
		}
	}
	e := &screen.Element{Role: screen.RoleInput, Label: "Security code (CVC)", ID: "cvc", States: []string{screen.StateEditable}}
	if !IsCredentialField(e) {
		t.Error("a card's security code no longer counts")
	}
}
