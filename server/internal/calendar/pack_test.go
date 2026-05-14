package calendar_test

import (
	"image"
	"image/color"
	"testing"

	"calendar-display/internal/calendar"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPack1Bit_FirstByte(t *testing.T) {
	t.Parallel()

	msb := image.NewRGBA(image.Rect(0, 0, 8, 1))
	msb.SetRGBA(0, 0, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	alpha := image.NewNRGBA(image.Rect(0, 0, 8, 1))
	alpha.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 0})

	cases := []struct {
		name string
		img  image.Image
		want byte
	}{
		{"all white", newSolidImage(8, 1, color.White), 0xFF},
		{"all black", newSolidImage(8, 1, color.Black), 0x00},
		{"white at x=0 only sets MSB", msb, 0x80},
		{"transparent white packs as black", alpha, 0x00},
		// RGBA() multiplies 8-bit by 257 → 16-bit; threshold is luma > 32768.
		// R=128 → 128*257=32896 > 32768 → white; R=127 → 127*257=32639 ≤ 32768 → black.
		{"luma just above 32768 packs as white", newSolidImage(8, 1, color.RGBA{R: 128, G: 128, B: 128, A: 255}), 0xFF},
		{"luma just at or below 32768 packs as black", newSolidImage(8, 1, color.RGBA{R: 127, G: 127, B: 127, A: 255}), 0x00},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			packed := calendar.Pack1Bit(tc.img)
			require.NotEmpty(t, packed)
			assert.Equal(t, tc.want, packed[0])
		})
	}
}

func TestPack1Bit_OutputLength(t *testing.T) {
	t.Parallel()
	cases := []struct {
		w, h int
	}{
		{8, 1},
		{16, 4},
		{800, 480}, // 48000 bytes — CLAUDE.md hard invariant
	}
	for _, tc := range cases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			img := newSolidImage(tc.w, tc.h, color.White)
			packed := calendar.Pack1Bit(img)
			assert.Len(t, packed, tc.w*tc.h/8)
		})
	}
}

// newSolidImage returns a solid-colour image of the given dimensions.
func newSolidImage(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill, ok := color.RGBAModel.Convert(c).(color.RGBA)
	if !ok {
		panic("color.RGBAModel.Convert did not return color.RGBA")
	}
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, fill)
		}
	}
	return img
}
