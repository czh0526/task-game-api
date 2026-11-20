// Package avatar normalizes user-uploaded pictures into a fixed 720x720 JPEG.
//
// The server is the authority on the stored format: whatever a client sends
// (any size or aspect ratio, JPEG/PNG/GIF, with or without transparency) is
// centre-cropped to a square, resampled to Size x Size and encoded as JPEG.
// Only the standard library is used, so no extra module is required.
package avatar

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/gif" // register GIF for image.Decode
	_ "image/png" // register PNG for image.Decode
	"math"
)

const (
	// Size is the width and height, in pixels, of every stored avatar.
	Size = 720
	// MaxInputBytes bounds a single upload before any decoding happens.
	MaxInputBytes = 12 << 20
	// MaxDimension rejects decompression bombs and absurdly large photos.
	MaxDimension = 8000
	// Quality is the JPEG encoding quality for the stored avatar.
	Quality = 88
)

// Normalize converts src into a 720x720 JPEG. It returns a descriptive error
// for empty, oversized, unsupported, or corrupt input.
func Normalize(src []byte) ([]byte, error) {
	if len(src) == 0 {
		return nil, errors.New("image is empty")
	}
	if len(src) > MaxInputBytes {
		return nil, fmt.Errorf("image is too large (max %d bytes)", MaxInputBytes)
	}

	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, errors.New("image must be a valid JPEG, PNG, or GIF")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, errors.New("image has no pixels")
	}
	if cfg.Width > MaxDimension || cfg.Height > MaxDimension {
		return nil, fmt.Errorf("image is too large (max %dx%d)", MaxDimension, MaxDimension)
	}

	srcImage, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, errors.New("image could not be decoded")
	}

	bounds := srcImage.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	side := w
	if h < side {
		side = h
	}
	// Centre crop: keep the middle square of the picture.
	left := bounds.Min.X + (w-side)/2
	top := bounds.Min.Y + (h-side)/2

	out := image.NewRGBA(image.Rect(0, 0, Size, Size))
	scale := float64(side) / float64(Size)
	for y := 0; y < Size; y++ {
		fy := (float64(y)+0.5)*scale - 0.5
		y0 := int(math.Floor(fy))
		if y0 < 0 {
			y0 = 0
		}
		if y0 >= side {
			y0 = side - 1
		}
		y1 := y0 + 1
		if y1 >= side {
			y1 = side - 1
		}
		ty := fy - float64(y0)
		if ty < 0 {
			ty = 0
		}
		if ty > 1 {
			ty = 1
		}
		for x := 0; x < Size; x++ {
			fx := (float64(x)+0.5)*scale - 0.5
			x0 := int(math.Floor(fx))
			if x0 < 0 {
				x0 = 0
			}
			if x0 >= side {
				x0 = side - 1
			}
			x1 := x0 + 1
			if x1 >= side {
				x1 = side - 1
			}
			tx := fx - float64(x0)
			if tx < 0 {
				tx = 0
			}
			if tx > 1 {
				tx = 1
			}

			p00 := whitePixel(srcImage, left+x0, top+y0)
			p10 := whitePixel(srcImage, left+x1, top+y0)
			p01 := whitePixel(srcImage, left+x0, top+y1)
			p11 := whitePixel(srcImage, left+x1, top+y1)
			topR := lerp(p00[0], p10[0], tx)
			topG := lerp(p00[1], p10[1], tx)
			topB := lerp(p00[2], p10[2], tx)
			botR := lerp(p01[0], p11[0], tx)
			botG := lerp(p01[1], p11[1], tx)
			botB := lerp(p01[2], p11[2], tx)

			i := out.PixOffset(x, y)
			out.Pix[i] = uint8(lerp(topR, botR, ty) + 0.5)
			out.Pix[i+1] = uint8(lerp(topG, botG, ty) + 0.5)
			out.Pix[i+2] = uint8(lerp(topB, botB, ty) + 0.5)
			out.Pix[i+3] = 255
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: Quality}); err != nil {
		return nil, fmt.Errorf("encode avatar: %w", err)
	}
	return buf.Bytes(), nil
}

// whitePixel returns the RGB of a pixel composited over white, so transparent
// PNG/GIF uploads do not turn black (or leak alpha) in the JPEG result.
// color.RGBA() gives 16-bit alpha-premultiplied channels; adding the missing
// white contribution and shifting back to 8 bits composites in one step.
func whitePixel(img image.Image, x, y int) [3]float64 {
	r, g, b, a := img.At(x, y).RGBA()
	white := 65535 - a
	return [3]float64{
		float64((r + white) >> 8),
		float64((g + white) >> 8),
		float64((b + white) >> 8),
	}
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }
