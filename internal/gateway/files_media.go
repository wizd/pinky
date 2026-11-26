package gateway

import (
	"bytes"
	"context"
	"fmt"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/image/bmp"
	"golang.org/x/image/webp"

	"github.com/DIYgod/pinky/internal/driver"
)

const maxClipboardImageBytes = 25 << 20

// filesCaps describes which file/media transfer actions a phone supports.
type filesCaps struct {
	files     bool // driver.Files (Pinky folder)
	media     bool // driver.MediaImporter (Photos library)
	clipboard bool // driver.ImageClipboard (image pasteboard)
}

func isImageFileName(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".heif":
		return true
	}
	return false
}

// normalizeImageBytes validates raw image bytes and returns a MIME type and
// payload accepted by driver.ImageClipboard (JPEG, PNG, GIF or HEIC),
// transcoding WebP and BMP to PNG.
func normalizeImageBytes(name string, raw []byte) (mimeType string, data []byte, err error) {
	switch {
	case isHEICHeader(raw):
		return "image/heic", raw, nil
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		if _, err := jpeg.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return "", nil, fmt.Errorf("%s is not a valid JPEG: %w", name, err)
		}
		return "image/jpeg", raw, nil
	case bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")):
		if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return "", nil, fmt.Errorf("%s is not a valid PNG: %w", name, err)
		}
		return "image/png", raw, nil
	case bytes.HasPrefix(raw, []byte("GIF87a")) || bytes.HasPrefix(raw, []byte("GIF89a")):
		if _, err := gif.DecodeConfig(bytes.NewReader(raw)); err != nil {
			return "", nil, fmt.Errorf("%s is not a valid GIF: %w", name, err)
		}
		return "image/gif", raw, nil
	case len(raw) >= 12 && string(raw[0:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		img, err := webp.Decode(bytes.NewReader(raw))
		if err != nil {
			return "", nil, fmt.Errorf("%s is not a valid WebP: %w", name, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", nil, fmt.Errorf("encode %s as PNG: %w", name, err)
		}
		return "image/png", buf.Bytes(), nil
	case len(raw) >= 2 && raw[0] == 'B' && raw[1] == 'M':
		img, err := bmp.Decode(bytes.NewReader(raw))
		if err != nil {
			return "", nil, fmt.Errorf("%s is not a valid BMP: %w", name, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", nil, fmt.Errorf("encode %s as PNG: %w", name, err)
		}
		return "image/png", buf.Bytes(), nil
	default:
		return "", nil, fmt.Errorf("%s is not a supported image (JPEG, PNG, GIF, WebP, BMP or HEIC): %w", name, driver.ErrUnsupported)
	}
}

func isHEICHeader(b []byte) bool {
	if len(b) < 12 || string(b[4:8]) != "ftyp" {
		return false
	}
	boxLen := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if boxLen < 12 || boxLen > len(b) {
		boxLen = min(len(b), 64)
	}
	isBrand := func(s string) bool {
		switch s {
		case "heic", "heix", "hevc", "hevx", "mif1", "msf1":
			return true
		}
		return false
	}
	if isBrand(string(b[8:12])) {
		return true
	}
	for off := 16; off+4 <= boxLen; off += 4 {
		if isBrand(string(b[off : off+4])) {
			return true
		}
	}
	return false
}

// pushMediaFiles saves images from the phone's local folder into the phone's
// Photos library via driver.MediaImporter (for phones without a Pinky folder,
// such as iPhones).
func (g *Gateway) pushMediaFiles(ctx context.Context, l *lease, req FilesRequest, importer driver.MediaImporter, dir string, paths []string, args string, start time.Time) *Result {
	d := l.dev
	switch g.scopeOf(l.session).Mode {
	case ScopeReadOnly, ScopeWatchOnly:
		return errResult(ErrScopeDenied, "This agent may only list and copy from the phone: the person gave it %s access.", strings.ReplaceAll(string(g.scopeOf(l.session).Mode), "_", "-"))
	}
	if g.policy.ReadOnly(d.id) {
		res := &Result{Status: StatusBlocked, Rule: "Read-only", Message: "Read-only mode is on, so nothing is copied to the phone."}
		g.recordFiles(l, args, "Blocked by “Read-only”: copy to the phone", res, start)
		return res
	}
	local, err := listLocal(dir)
	if err != nil {
		return errResult(ErrInternal, "Reading the local folder failed: %v", err)
	}
	var want []FileView
	var missing []string
	if len(paths) == 0 {
		for _, f := range sortedLocal(local) {
			if isImageFileName(f.Path) {
				want = append(want, f)
			}
		}
		if len(want) == 0 {
			return errResult(ErrNotFound, "No image files in the local folder to save to %s's Photos library. Upload an image first (JPEG, PNG, GIF, WebP, BMP or HEIC) and pass its name in paths.", d.name())
		}
	} else {
		seen := map[string]bool{}
		for _, p := range paths {
			found := false
			for _, f := range sortedLocal(local) {
				if (f.Path == p || strings.HasPrefix(f.Path, p+"/")) && !seen[f.Path] {
					seen[f.Path] = true
					want = append(want, f)
					found = true
				} else if f.Path == p || strings.HasPrefix(f.Path, p+"/") {
					found = true
				}
			}
			if !found {
				missing = append(missing, p)
			}
		}
	}
	if len(missing) > 0 {
		res := errResult(ErrNotFound, "Not in the local folder: %s. Nothing was copied.", strings.Join(missing, ", "))
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	// Pre-validate that every selected file is a readable, supported image
	// before asking the person to approve.
	for _, f := range want {
		if err := validateLocalImage(dir, f.Path); err != nil {
			res := g.filesError(l, err)
			g.recordFiles(l, args, "Failed: copy to the phone", res, start)
			return res
		}
	}
	if res := g.askToPush(ctx, l, req, want, args, "push_media", "into the phone's Photos library"); res != nil {
		return res
	}
	var saved []FileView
	for _, f := range want {
		if err := importMediaOne(ctx, importer, dir, f); err != nil {
			res := g.filesError(l, err)
			if len(saved) > 0 && res.Error != "" {
				res.Message = fmt.Sprintf("Saving %s to %s's Photos library failed after %s: %s", f.Path, d.name(), count(len(saved), "photo"), res.Message)
			}
			g.recordFiles(l, args, "Failed: copy to the phone", res, start)
			return res
		}
		f.Where = FileBoth
		saved = append(saved, f)
	}
	res := &Result{Status: StatusSettled, Files: saved, Folder: dir,
		Message: fmt.Sprintf("Saved %s to %s's Photos library (Recents). Pick it in the app's photo picker (if the app only sees selected photos, tap Add More Photos in the picker).", count(len(saved), "photo"), d.name())}
	g.recordFiles(l, args, fmt.Sprintf("Copied %s to the phone%s", count(len(saved), "file"), names(saved)), res, start)
	return res
}

func validateLocalImage(dir, rel string) error {
	src, err := localPath(dir, rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", rel)
	}
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	// Read enough to check magic/headers (or full WebP/BMP if small); for
	// JPEG/PNG/GIF/HEIC reading the full file is bounded and fast.
	raw, err := io.ReadAll(io.LimitReader(r, 50<<20+1))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s is empty", rel)
	}
	if len(raw) > 50<<20 {
		return fmt.Errorf("%s is larger than 50 MB", rel)
	}
	_, _, err = normalizeImageBytes(rel, raw)
	return err
}

func importMediaOne(ctx context.Context, importer driver.MediaImporter, dir string, f FileView) error {
	src, err := localPath(dir, f.Path)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", f.Path)
	}
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	return importer.ImportMedia(ctx, path.Base(f.Path), io.LimitReader(r, fi.Size()), fi.Size())
}

// copyImageToClipboard places one image file from the phone's local folder onto
// the phone's clipboard via driver.ImageClipboard so an agent can paste or send
// it directly in chat and editor apps without switching apps.
func (g *Gateway) copyImageToClipboard(ctx context.Context, l *lease, req FilesRequest, clip driver.ImageClipboard, dir string, rel, args string, start time.Time) *Result {
	d := l.dev
	switch g.scopeOf(l.session).Mode {
	case ScopeReadOnly, ScopeWatchOnly:
		return errResult(ErrScopeDenied, "This agent may only list and copy from the phone: the person gave it %s access.", strings.ReplaceAll(string(g.scopeOf(l.session).Mode), "_", "-"))
	}
	if g.policy.ReadOnly(d.id) {
		res := &Result{Status: StatusBlocked, Rule: "Read-only", Message: "Read-only mode is on, so nothing is copied to the phone."}
		g.recordFiles(l, args, "Blocked by “Read-only”: copy to the phone", res, start)
		return res
	}
	local, err := listLocal(dir)
	if err != nil {
		return errResult(ErrInternal, "Reading the local folder failed: %v", err)
	}
	f, ok := local[rel]
	if !ok {
		res := errResult(ErrNotFound, "Not in the local folder: %s. Upload or place the image in the folder first.", rel)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	src, err := localPath(dir, f.Path)
	if err != nil {
		res := g.filesError(l, err)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	fi, err := os.Lstat(src)
	if err != nil {
		res := g.filesError(l, err)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	if !fi.Mode().IsRegular() {
		res := errResult(ErrInvalidArgument, "%s is not a regular file.", f.Path)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	if fi.Size() > maxClipboardImageBytes {
		res := errResult(ErrInvalidArgument, "%s is larger than %d MB, too large for the phone's clipboard.", f.Path, maxClipboardImageBytes>>20)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		res := errResult(ErrInternal, "Reading %s failed: %v", f.Path, err)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	if len(raw) == 0 {
		res := errResult(ErrInvalidArgument, "%s is empty.", f.Path)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	mimeType, payload, err := normalizeImageBytes(f.Path, raw)
	if err != nil {
		res := g.filesError(l, err)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	if len(payload) > maxClipboardImageBytes {
		res := errResult(ErrInvalidArgument, "%s exceeds %d MB after PNG conversion, too large for the phone's clipboard.", f.Path, maxClipboardImageBytes>>20)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	want := []FileView{f}
	if res := g.askToPush(ctx, l, req, want, args, "clipboard", "onto the phone's clipboard"); res != nil {
		return res
	}
	if err := clip.SetClipboardImage(ctx, mimeType, payload); err != nil {
		res := g.filesError(l, err)
		g.recordFiles(l, args, "Failed: copy to the phone", res, start)
		return res
	}
	res := &Result{Status: StatusSettled, Files: want, Folder: dir,
		Message: fmt.Sprintf("Copied %s (%s) onto %s's clipboard. Tap the app's text or chat input to focus it first, then tap or long-press the cursor and tap Paste (粘贴) to send or attach it.", f.Path, mimeType, d.name())}
	g.recordFiles(l, args, fmt.Sprintf("Copied %s to the phone%s", count(1, "file"), names(want)), res, start)
	return res
}
