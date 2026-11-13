package pilot

import (
	"strings"
	"testing"
	"time"
)

func TestScriptLanguage(t *testing.T) {
	for task, want := range map[string]string{
		"Work out 12 × 34 in Calculator": "English",
		"用计算器算 12 × 34":                  "Chinese",
		"電卓で 12 × 34 を計算して":              "Japanese",
		"계산기로 12 곱하기 34":                 "Korean",
		"Öffne den Rechner":              "",
		"12 × 34":                        "",
	} {
		if got := scriptLanguage(task); got != want {
			t.Errorf("scriptLanguage(%q) = %q, want %q", task, got, want)
		}
	}
}

// The first message ends with the summary's language, after a screen that
// may be in another one (an English task on a phone set to Chinese).
func TestFirstMessageSummaryLanguage(t *testing.T) {
	msg := firstMessage("Work out 12 × 34 in Calculator", "iPhone", `[1] button "计算器"`, time.Now(), "", "")
	if !strings.HasSuffix(msg, "write finish's summary in English, whatever language the phone's screen is in.") {
		t.Errorf("first message does not end with the summary's language:\n%s", msg)
	}
	msg = firstMessage("Öffne den Rechner", "iPhone", "", time.Now(), "", "")
	if !strings.HasSuffix(msg, "in the language the person wrote the task in, whatever language the phone's screen is in.") {
		t.Errorf("first message does not end with the summary's language:\n%s", msg)
	}
}
