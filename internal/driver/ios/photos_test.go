package ios

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/bmp"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func makeSampleImage(t *testing.T) image.Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{R: 0x30, G: 0x80, B: 0xE0, A: 0xFF})
		}
	}
	return img
}

func TestPrepareImportImage(t *testing.T) {
	sample := makeSampleImage(t)

	var jpgBuf bytes.Buffer
	if err := jpeg.Encode(&jpgBuf, sample, nil); err != nil {
		t.Fatal(err)
	}
	got, err := prepareImportImage("cover.jpg", bytes.NewReader(jpgBuf.Bytes()), int64(jpgBuf.Len()))
	if err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	if got.contentType != "image/jpeg" || got.ext != ".jpg" || !bytes.Equal(got.data, jpgBuf.Bytes()) {
		t.Fatalf("jpeg mismatch: type=%q ext=%q len=%d", got.contentType, got.ext, len(got.data))
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, sample); err != nil {
		t.Fatal(err)
	}
	got, err = prepareImportImage("cover.png", bytes.NewReader(pngBuf.Bytes()), int64(pngBuf.Len()))
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	if got.contentType != "image/png" || got.ext != ".png" {
		t.Fatalf("png mismatch: %+v", got)
	}

	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, sample, nil); err != nil {
		t.Fatal(err)
	}
	got, err = prepareImportImage("anim.gif", bytes.NewReader(gifBuf.Bytes()), int64(gifBuf.Len()))
	if err != nil {
		t.Fatalf("gif: %v", err)
	}
	if got.contentType != "image/gif" || got.ext != ".gif" {
		t.Fatalf("gif mismatch: %+v", got)
	}

	// BMP is transcoded to PNG so Safari always handles it as a native photo.
	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, sample); err != nil {
		t.Fatal(err)
	}
	got, err = prepareImportImage("photo.bmp", bytes.NewReader(bmpBuf.Bytes()), int64(bmpBuf.Len()))
	if err != nil {
		t.Fatalf("bmp: %v", err)
	}
	if got.contentType != "image/png" || got.ext != ".png" {
		t.Fatalf("bmp expected PNG, got type=%q ext=%q", got.contentType, got.ext)
	}
	if _, err := png.DecodeConfig(bytes.NewReader(got.data)); err != nil {
		t.Fatalf("transcoded bmp is not valid PNG: %v", err)
	}

	// Minimal ISO BMFF ftyp box with heic brand.
	heic := []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p',
		'h', 'e', 'i', 'c', 0x00, 0x00, 0x00, 0x00,
		'm', 'i', 'f', '1', 'h', 'e', 'i', 'c',
	}
	got, err = prepareImportImage("shot.heic", bytes.NewReader(heic), int64(len(heic)))
	if err != nil {
		t.Fatalf("heic: %v", err)
	}
	if got.contentType != "image/heic" || got.ext != ".heic" {
		t.Fatalf("heic mismatch: %+v", got)
	}

	// MP4 ftyp box must be rejected as unsupported.
	mp4 := []byte{
		0x00, 0x00, 0x00, 0x14, 'f', 't', 'y', 'p',
		'i', 's', 'o', 'm', 0x00, 0x00, 0x02, 0x00,
		'm', 'p', '4', '1',
	}
	if _, err := prepareImportImage("clip.mp4", bytes.NewReader(mp4), int64(len(mp4))); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("mp4 err = %v, want ErrUnsupported", err)
	}

	// Plain text rejected as unsupported.
	if _, err := prepareImportImage("note.txt", strings.NewReader("hello"), 5); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("txt err = %v, want ErrUnsupported", err)
	}

	// Empty file rejected.
	if _, err := prepareImportImage("empty.jpg", bytes.NewReader(nil), 0); err == nil {
		t.Fatal("expected error for empty image")
	}

	// Corrupt JPEG rejected.
	if _, err := prepareImportImage("bad.jpg", bytes.NewReader([]byte{0xFF, 0xD8, 0xFF, 0x00}), 4); err == nil {
		t.Fatal("expected error for corrupt JPEG")
	}
}

func TestIsSaveToPhotosLabel(t *testing.T) {
	wantYes := []string{
		"Save to Photos",
		"Add to Photos",
		"Save Image",
		"存储到“照片”",
		"添加到“照片”",
		"保存到照片",
		"儲存至「照片」",
		"“写真”に保存",
		"写真に追加",
		"사진에 저장",
		"Guardar en Fotos",
		"Enregistrer dans Photos",
		"In „Fotos“ sichern",
	}
	for _, s := range wantYes {
		if !isSaveToPhotosLabel(s) {
			t.Errorf("isSaveToPhotosLabel(%q) = false, want true", s)
		}
	}
	wantNo := []string{
		"",
		"Save to Files",
		"存储到“文件”",
		"Copy",
		"拷贝",
		"Share…",
		"共享…",
		"Open in New Tab",
	}
	for _, s := range wantNo {
		if isSaveToPhotosLabel(s) {
			t.Errorf("isSaveToPhotosLabel(%q) = true, want false", s)
		}
	}
}

func TestSaveSafariImageWith(t *testing.T) {
	si := screenInfo{Width: 1170, Height: 2532, Scale: 3}
	appEl := raxElement{Visible: true, HasFrame: true, Frame: axpRect{X: 0, Y: 0, W: 390, H: 844}}
	imgEl := raxElement{Visible: true, HasFrame: true, Label: "Pinky Photo", Frame: axpRect{X: 45, Y: 180, W: 300, H: 400}}
	menuCopy := raxElement{Visible: true, HasFrame: true, Label: "拷贝", Frame: axpRect{X: 80, Y: 520, W: 230, H: 44}}
	menuSave := raxElement{Visible: true, HasFrame: true, Label: "存储到“照片”", Frame: axpRect{X: 80, Y: 570, W: 230, H: 44}}

	var pressed screen.Point
	var tapped screen.Point
	var closed bool
	walkCount := 0

	ops := safariSaveOps{
		screenInfo: func(context.Context) screenInfo { return si },
		walk: func(context.Context) ([]raxElement, error) {
			walkCount++
			if walkCount == 1 {
				return []raxElement{appEl, imgEl}, nil
			}
			return []raxElement{appEl, imgEl, menuCopy, menuSave}, nil
		},
		longPress: func(_ context.Context, p screen.Point, _ time.Duration) error {
			pressed = p
			return nil
		},
		tap: func(_ context.Context, p screen.Point) error {
			tapped = p
			return nil
		},
		closeTab: func(context.Context) { closed = true },
		sleep:    func(context.Context, time.Duration) error { return nil },
	}

	if err := saveSafariImageWith(context.Background(), ops, 100*time.Millisecond, 10*time.Millisecond, 0); err != nil {
		t.Fatalf("saveSafariImageWith: %v", err)
	}
	if !closed {
		t.Fatal("expected closeTab to be called")
	}
	// imgEl center: (45+150)*3 = 585, (180+200)*3 = 1140.
	if pressed.X != 585 || pressed.Y != 1140 {
		t.Fatalf("longPress point = %+v, want {585, 1140}", pressed)
	}
	// menuSave center: (80+115)*3 = 585, (570+22)*3 = 1776.
	if tapped.X != 585 || tapped.Y != 1776 {
		t.Fatalf("tap point = %+v, want {585, 1776}", tapped)
	}
}

func TestMediaTransferServer(t *testing.T) {
	payload := []byte("fake-jpeg-bytes")
	srv := newMediaTransferServer("tok123", "image/jpeg", payload)

	// Wrong path -> 404.
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/p/wrong", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("wrong token status = %d, want 404", rr.Code)
	}

	// Page path -> 200 HTML and closes pageHit.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/p/tok123", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `src="/i/tok123"`) {
		t.Fatalf("page response: code=%d body=%q", rr.Code, rr.Body.String())
	}
	select {
	case <-srv.pageHit:
	default:
		t.Fatal("expected pageHit to be closed")
	}

	// Image path -> 200 image bytes and closes imgServed.
	rr = httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/i/tok123", nil))
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rr.Body.Bytes(), payload) {
		t.Fatalf("image response mismatch: code=%d ct=%q body=%q", rr.Code, rr.Header().Get("Content-Type"), rr.Body.Bytes())
	}
	select {
	case <-srv.imgServed:
	default:
		t.Fatal("expected imgServed to be closed")
	}
}

func TestMimeToPasteboardUTI(t *testing.T) {
	cases := map[string]string{
		"image/jpeg": "public.jpeg",
		"image/png":  "public.png",
		"image/gif":  "com.compuserve.gif",
		"image/heic": "public.heic",
	}
	for mime, want := range cases {
		got, ok := mimeToPasteboardUTI(mime)
		if !ok || got != want {
			t.Errorf("mimeToPasteboardUTI(%q) = %q, %v; want %q, true", mime, got, ok, want)
		}
	}
	if _, ok := mimeToPasteboardUTI("text/plain"); ok {
		t.Error("expected text/plain to be unsupported")
	}
}
