package calendar

import "image"

// pack1Bit converts the rendered image to a packed 1-bit MSB-first buffer.
// Bit = 1 means white pixel, bit = 0 means black pixel. This matches what
// GxEPD2's drawInvertedBitmap(..., GxEPD_BLACK) expects: it paints black
// where the bit is 0.
//
// Layout: 800x480 = 384000 pixels = 48000 bytes (8 pixels per byte).
func pack1Bit(img image.Image) []byte {
	const (
		bitsPerByte    = 8
		whiteThreshold = uint32(32768)
		rgbComponents  = 3
		msbBitIndex    = 7
	)

	b := img.Bounds()
	w := b.Dx()
	h := b.Dy()
	out := make([]byte, w*h/bitsPerByte)

	for y := range h {
		for x := range w {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// RGBA() returns 16-bit components (0..65535).
			// Simple luma + threshold at 50%.
			luma := (r + g + bl) / rgbComponents
			white := luma > whiteThreshold
			byteIdx := (y*w + x) / bitsPerByte
			bitIdx := msbBitIndex - uint(x%bitsPerByte)
			if white {
				out[byteIdx] |= 1 << bitIdx
			}
		}
	}
	return out
}
