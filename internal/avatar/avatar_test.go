package avatar

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func decodeJPEG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("avatar is not a valid JPEG: %v", err)
	}
	return img
}

func rgbAt(img image.Image, x, y int) (uint8, uint8, uint8) {
	r, g, b, _ := img.At(x, y).RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
}

func near(t *testing.T, got, want uint8) {
	t.Helper()
	if int(got)-int(want) > 30 || int(want)-int(got) > 30 {
		t.Fatalf("channel %d, want about %d", got, want)
	}
}

// A wide 800x400 picture must be centre-cropped to a square before scaling:
// the left half of the result stays red and the right half stays blue.
func TestNormalizeCentreCropsWideImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 400 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	out, err := Normalize(encodePNG(t, src))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	img := decodeJPEG(t, out)
	if got := img.Bounds(); got.Dx() != Size || got.Dy() != Size {
		t.Fatalf("size = %dx%d, want %dx%d", got.Dx(), got.Dy(), Size, Size)
	}
	r, g, b := rgbAt(img, 120, 360)
	near(t, r, 255)
	near(t, g, 0)
	near(t, b, 0)
	r, g, b = rgbAt(img, 600, 360)
	near(t, r, 0)
	near(t, g, 0)
	near(t, b, 255)
}

func TestNormalizeUpscalesSmallImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 60, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			src.Set(x, y, color.RGBA{G: 200, A: 255})
		}
	}
	img := decodeJPEG(t, mustNormalize(t, encodePNG(t, src)))
	if got := img.Bounds(); got.Dx() != Size || got.Dy() != Size {
		t.Fatalf("size = %dx%d, want %dx%d", got.Dx(), got.Dy(), Size, Size)
	}
	r, g, b := rgbAt(img, Size/2, Size/2)
	near(t, r, 0)
	near(t, g, 200)
	near(t, b, 0)
}

// Transparent pixels must become white, not black, when converted to JPEG.
func TestNormalizeCompositesTransparencyOnWhite(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 120, 120))
	img := decodeJPEG(t, mustNormalize(t, encodePNG(t, src)))
	r, g, b := rgbAt(img, Size/2, Size/2)
	near(t, r, 255)
	near(t, g, 255)
	near(t, b, 255)
}

func TestNormalizeAcceptsJPEGInput(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 300; x++ {
			src.Set(x, y, color.RGBA{R: 40, G: 120, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	img := decodeJPEG(t, mustNormalize(t, buf.Bytes()))
	if got := img.Bounds(); got.Dx() != Size || got.Dy() != Size {
		t.Fatalf("size = %dx%d", got.Dx(), got.Dy())
	}
}

func TestNormalizeRejectsBadInput(t *testing.T) {
	cases := map[string][]byte{
		"empty":       nil,
		"not an image": []byte("hello world"),
		"truncated png": []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		"too large":   bytes.Repeat([]byte{1}, MaxInputBytes+1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(input); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func mustNormalize(t *testing.T, src []byte) []byte {
	t.Helper()
	out, err := Normalize(src)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	return out
}
