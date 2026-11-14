package h264enc

import (
	"bytes"
	"testing"
)

func TestAVCCToAnnexB(t *testing.T) {
	avcc := []byte{0, 0, 0, 2, 0x65, 0xAA, 0, 0, 0, 3, 0x41, 0xBB, 0xCC}
	got := avccToAnnexB(avcc, 4)
	want := []byte{0, 0, 0, 1, 0x65, 0xAA, 0, 0, 0, 1, 0x41, 0xBB, 0xCC}
	if !bytes.Equal(got, want) {
		t.Fatalf("annex b % x", got)
	}
	if !hasIDR(got) || hasIDR(want[6:]) {
		t.Fatal("IDR detection")
	}
	if len(avccToAnnexB([]byte{0, 0, 0, 9, 1}, 4)) != 0 {
		t.Fatal("a truncated unit must be dropped")
	}
}
