package ui

import (
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"
)

// testLang is the language of the client's words in tests: English, the
// labels the tests look for, whatever the computer's language is, unless a
// test asks for another with inLanguage.
var testLang atomic.Value

func TestMain(m *testing.M) {
	testLang.Store("en")
	i18n.SetLocaleFunc(func() string { return testLang.Load().(string) })
	// MyGo answers these on its main thread, which tests have none of: a
	// computer that prefers English, and picks that apply to the test.
	appPreferred = func() []string { return []string{"en-US"} }
	appLocale = func() string { return testLang.Load().(string) }
	setAppLocale = func(tag string) {
		if tag == "" {
			tag = "en"
		}
		testLang.Store(tag)
	}
	// The helper's words are told by the tests' clock, which is the
	// computer's until a test sets it (clockAt).
	timeNow = testNow
	// The tests' clock stands still, so what arrives shows at once rather
	// than halfway (withMotion turns the motion back on).
	stillMotion = true
	os.Exit(m.Run())
}

// testLanguages are the languages of the client's catalogs, English first:
// tests that check a layout or draw screenshots draw it in each, so a
// longer translation that no longer fits shows up.
func testLanguages() []string { return mygo.App.Languages() }

// langShort names lang in screenshot files: "en", "zh" or "ja".
func langShort(lang string) string {
	if lang == "zh-Hans" {
		return "zh"
	}
	return lang
}

// inLanguage shows the client's words in tag for the rest of the test, and
// MyGo's own in the testers given.
func inLanguage(t testing.TB, tag string, testers ...*mg.Tester) {
	t.Helper()
	testLang.Store(tag)
	for _, tt := range testers {
		tt.SetLocale(tag)
	}
	t.Cleanup(func() { testLang.Store("en") })
}

// testClock is the time a test set with clockAt; nil leaves the computer's
// clock to tell the time. The fake's goroutines read it too, hence atomic.
var testClock atomic.Pointer[time.Time]

// testNow tells the time by the tests' clock: the helper's words read it
// (timeNow), and so does the fake of every test app (newTestAppWith).
func testNow() time.Time {
	if now := testClock.Load(); now != nil {
		return *now
	}
	return time.Now()
}

// clockAt sets the tests' clock to now for the rest of the test: what the
// views say about when an iPhone's helper stops working, and the dates the
// fake gives what it does, then depend on neither the day nor the hour the
// test runs at. Other views tell dates by Context.Now, which a test cannot
// set: MyGo's Tester starts its clock at the computer's time and only
// moves it on.
func clockAt(t testing.TB, now time.Time) {
	t.Helper()
	saved := testClock.Swap(&now)
	t.Cleanup(func() { testClock.Store(saved) })
}

// wallTime is the time at which the clocks where the test runs show these
// numbers. The views write dates as those clocks do, so a date written
// from it reads the same in every time zone, which one written from a
// time.UTC date or from time.Now does not. Keep the hour to the daytime:
// daylight saving time skips an hour of the night in most places, and of
// the late evening in a few (Easter Island at 22:00).
func wallTime(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.Local)
}
