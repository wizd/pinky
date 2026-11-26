package android

import "strings"

// hasHardwareHEVCEncoder reports whether dumpsys media.player's encoder table
// lists at least one hardware-accelerated video/hevc encoder. ok is false when
// the output holds no encoder table, so the caller keeps its existing choice.
func hasHardwareHEVCEncoder(text string) (hw, ok bool) {
	_, rest, found := strings.Cut(text, "Encoder infos by media types:")
	if !found {
		return false, false
	}
	inHEVC := false
	curName := ""
	curAttr := false
	curHW := false
	curSW := false
	finish := func() bool {
		if curName == "" {
			return false
		}
		if curAttr {
			return curHW && !curSW
		}
		return !isSoftwareEncoderName(curName)
	}
	for _, raw := range strings.Split(rest, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if after, isMedia := strings.CutPrefix(line, "Media type '"); isMedia {
			if inHEVC && finish() {
				return true, true
			}
			curName, curAttr, curHW, curSW = "", false, false, false
			inHEVC = strings.HasPrefix(after, "video/hevc'")
			continue
		}
		if !inHEVC {
			continue
		}
		// End of the indented "Media type" section (e.g. " No media recorder client").
		if trimmed != "" && !strings.HasPrefix(line, "  ") {
			if finish() {
				return true, true
			}
			return false, true
		}
		if after, isEnc := strings.CutPrefix(line, `  Encoder "`); isEnc {
			if finish() {
				return true, true
			}
			name, _, _ := strings.Cut(after, `"`)
			curName, curAttr, curHW, curSW = name, false, false, false
			continue
		}
		if curName == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "hw-accelerated:"):
			curAttr = true
			curHW = strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, "hw-accelerated:")), "1")
		case strings.HasPrefix(trimmed, "software-only:"):
			curAttr = true
			curSW = strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(trimmed, "software-only:")), "1")
		}
	}
	if inHEVC && finish() {
		return true, true
	}
	return false, true
}

func isSoftwareEncoderName(name string) bool {
	s := strings.ToLower(name)
	return strings.HasPrefix(s, "c2.android.") ||
		strings.HasPrefix(s, "omx.google.") ||
		strings.HasPrefix(s, "omx.ffmpeg.") ||
		strings.Contains(s, ".sw.") ||
		strings.HasSuffix(s, ".sw")
}
