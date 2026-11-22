package ios

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// shotPhone is a fakePhone that can also show the pad in a screenshot.
type shotPhone struct {
	*fakePhone
	shotErr error // every look at a screenshot fails with it
	shots   int
}

func (s *shotPhone) padInScreenshot(context.Context) (bool, error) {
	s.shots++
	if s.shotErr != nil {
		return false, s.shotErr
	}
	return s.pad, nil
}

func TestUnlockPadInScreenshot(t *testing.T) {
	ctx := context.Background()
	const axDead = 1 << 20 // accessibility never answers
	t.Run("accessibility does not answer", func(t *testing.T) {
		f := lockedPhone("123456")
		f.lookErrs = axDead
		s := &shotPhone{fakePhone: f}
		if err := unlock(ctx, s, "123456"); err != nil {
			t.Fatal(err)
		}
		if f.axLooks != 1 || s.shots == 0 || len(f.keys) != 6 {
			t.Errorf("accessibility looks %d, screenshots %d, keys %d; want 1 look, then screenshots, then the passcode", f.axLooks, s.shots, len(f.keys))
		}
	})
	t.Run("the screenshot has no pad", func(t *testing.T) {
		f := lockedPhone("123456")
		f.lookErrs = axDead
		f.padOnSwipe = false
		s := &shotPhone{fakePhone: f}
		err := unlock(ctx, s, "123456")
		if !errors.Is(err, driver.ErrPINNotEntered) || len(f.keys) != 0 {
			t.Fatalf("err %v, keys %d; want ErrPINNotEntered and nothing typed", err, len(f.keys))
		}
		if f.axLooks != unlockSwipes {
			t.Errorf("%d accessibility looks; want one per swipe (%d)", f.axLooks, unlockSwipes)
		}
	})
	t.Run("no screenshot either", func(t *testing.T) {
		f := lockedPhone("123456")
		f.lookErrs = axDead
		s := &shotPhone{fakePhone: f, shotErr: errors.New("no screenshot")}
		err := unlock(ctx, s, "123456")
		if !errors.Is(err, driver.ErrPINNotEntered) || len(f.keys) != 0 {
			t.Fatalf("err %v, keys %d; want ErrPINNotEntered and nothing typed", err, len(f.keys))
		}
		if !strings.Contains(err.Error(), "no screenshot") {
			t.Errorf("err %q does not say why the screenshot failed", err)
		}
	})
	t.Run("accessibility answers", func(t *testing.T) {
		f := lockedPhone("123456")
		s := &shotPhone{fakePhone: f}
		if err := unlock(ctx, s, "123456"); err != nil {
			t.Fatal(err)
		}
		if s.shots != 0 {
			t.Errorf("%d screenshots; want none while accessibility answers", s.shots)
		}
	})
}

func lines(texts ...string) []ocrLine {
	ls := make([]ocrLine, len(texts))
	for i, t := range texts {
		ls[i] = ocrLine{Text: t}
	}
	return ls
}

func TestPadInText(t *testing.T) {
	// What Vision read on an iPhone 14 Pro's passcode screen (iOS 27.2.1),
	// in order: no 0, and the dots read as punctuation.
	pad := lines("Enter Passcode", "。。", "。。•", "1", "2", "ABC", "3", "DEF", "4", "GHI", "5", "JKL",
		"6", "MNO", "7", "PQRS", "8", "TUV", "9", "WXYZ", "Emergency", "Cancel")
	if !padInText(pad) {
		t.Error("the passcode screen was not taken for the pad")
	}
	for _, c := range []struct {
		name  string
		lines []ocrLine
	}{
		{"nothing read", nil},
		{"a digit missing", lines("1", "2", "3", "4", "5", "6", "7", "8", "Emergency")},
		{"lock screen", lines("Tuesday 17 November", "23:46", "DEVICE HUB", "2 notifications", "9:41")},
		{"digits in longer text", lines("12", "34", "56", "78", "9 ABC", "1 2 3 4 5 6 7 8 9")},
	} {
		if padInText(c.lines) {
			t.Errorf("%s: taken for the pad", c.name)
		}
	}
}
