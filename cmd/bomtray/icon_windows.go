//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
)

// The tray icon is drawn and encoded here rather than shipped as an asset, so
// the binary stays self-contained and a state can be added without touching a
// build step.
//
// Two constraints shape this file:
//
//   - systray writes the bytes to a temp file and loads them with
//     LoadImage(LR_LOADFROMFILE). LoadImage understands only the classic
//     DIB-in-ICO form, so a PNG-compressed ICO — legal since Vista, and what
//     image/png would make easy — silently fails to load.
//   - A 32-bit DIB wants straight (non-premultiplied) alpha, whereas Go's
//     image.RGBA is premultiplied. The pixel buffer below is therefore built
//     by hand instead of borrowed from the image package.

// px sizes to embed. Windows picks the closest to the notification area's
// current DPI; 16 covers 100% and 32 covers 200%.
var iconSizes = []int{16, 32}

// State colours.
//
// Written with field names on purpose: rgba stores its channels in the order a
// DIB wants them (b, g, r, a), so a positional literal reads as RGBA and
// silently swaps red and blue. It did — "down" rendered blue until someone
// looked at the icon.
var (
	colIdle    = rgba{r: 0x6B, g: 0x78, b: 0x8A, a: 0xFF} // slate: up, nothing in flight
	colRunning = rgba{r: 0x30, g: 0xA4, b: 0x6C, a: 0xFF} // green: a run is in flight
	colDown    = rgba{r: 0xE5, g: 0x48, b: 0x4D, a: 0xFF} // red: a known gateway went away
	colAwake   = rgba{r: 0xF5, g: 0xA5, b: 0x24, a: 0xFF} // amber pip: sleep is being held
)

var (
	iconMu    sync.Mutex
	iconCache = map[trayState][]byte{}
)

// stateIcon returns .ico bytes for a state, encoding each one at most once.
// systray keys its own icon-handle cache off the file path, which it derives
// from a hash of these bytes, so returning identical bytes for an unchanged
// state also avoids re-creating the Win32 icon handle every poll.
func stateIcon(s trayState) []byte {
	iconMu.Lock()
	defer iconMu.Unlock()
	if b, ok := iconCache[s]; ok {
		return b
	}
	canvases := make([]*canvas, 0, len(iconSizes))
	for _, px := range iconSizes {
		canvases = append(canvases, drawStateIcon(px, s))
	}
	b := encodeICO(canvases)
	iconCache[s] = b
	return b
}

// rgba is one straight-alpha pixel, in the order a DIB stores them.
type rgba struct{ b, g, r, a uint8 }

// canvas is a straight-alpha pixel buffer, top row first.
type canvas struct {
	w, h int
	pix  []rgba
}

func newCanvas(w, h int) *canvas {
	return &canvas{w: w, h: h, pix: make([]rgba, w*h)}
}

func (c *canvas) at(x, y int) rgba     { return c.pix[y*c.w+x] }
func (c *canvas) set(x, y int, p rgba) { c.pix[y*c.w+x] = p }

// over composites src onto the pixel at (x,y) using straight-alpha Porter-Duff.
func (c *canvas) over(x, y int, src rgba) {
	if src.a == 0 {
		return
	}
	if src.a == 0xFF {
		c.set(x, y, src)
		return
	}
	dst := c.at(x, y)
	sa := float64(src.a) / 255
	da := float64(dst.a) / 255
	oa := sa + da*(1-sa)
	if oa == 0 {
		c.set(x, y, rgba{})
		return
	}
	mix := func(s, d uint8) uint8 {
		v := (float64(s)*sa + float64(d)*da*(1-sa)) / oa
		return uint8(math.Round(math.Min(255, math.Max(0, v))))
	}
	c.set(x, y, rgba{
		b: mix(src.b, dst.b),
		g: mix(src.g, dst.g),
		r: mix(src.r, dst.r),
		a: uint8(math.Round(oa * 255)),
	})
}

// disc draws an antialiased filled circle. Coverage is the fraction of the
// pixel inside the edge, approximated over one pixel of falloff — enough that
// a 16px dot does not look stepped.
func (c *canvas) disc(cx, cy, r float64, col rgba) {
	x0, x1 := int(math.Floor(cx-r-1)), int(math.Ceil(cx+r+1))
	y0, y1 := int(math.Floor(cy-r-1)), int(math.Ceil(cy+r+1))
	for y := max(0, y0); y <= min(c.h-1, y1); y++ {
		for x := max(0, x0); x <= min(c.w-1, x1); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			cov := math.Min(1, math.Max(0, r-d+0.5))
			if cov <= 0 {
				continue
			}
			p := col
			p.a = uint8(math.Round(float64(col.a) * cov))
			c.over(x, y, p)
		}
	}
}

// drawStateIcon paints one size. A single filled dot, because at 16px anything
// more detailed is mush: the colour is the signal, the tooltip is the detail.
func drawStateIcon(px int, s trayState) *canvas {
	c := newCanvas(px, px)

	col := colIdle
	switch {
	case s.down:
		col = colDown
	case s.running:
		col = colRunning
	}

	mid := float64(px) / 2
	// Inset by a pixel at 16px so the dot is not clipped by the icon bounds.
	c.disc(mid, mid, mid-float64(px)/16-0.5, col)

	if s.awake {
		// A pip in the bottom-right corner, punched out of the dot with a
		// transparent ring first so it reads as a separate mark rather than a
		// smudge on a similar colour.
		pr := float64(px) / 4
		pcx, pcy := float64(px)-pr, float64(px)-pr
		c.disc(pcx, pcy, pr, rgba{})
		c.disc(pcx, pcy, pr*0.8, colAwake)
	}

	return c
}

// encodeICO packs the canvases into a Windows .ico.
//
// Layout: ICONDIR, then one ICONDIRENTRY per image, then the image bodies. The
// entries' offsets are absolute from the start of the file, so the directory
// size has to be known before any of them can be written.
func encodeICO(imgs []*canvas) []byte {
	var dir, body bytes.Buffer

	binary.Write(&dir, binary.LittleEndian, uint16(0))         // reserved
	binary.Write(&dir, binary.LittleEndian, uint16(1))         // type: icon
	binary.Write(&dir, binary.LittleEndian, uint16(len(imgs))) // count

	offset := 6 + 16*len(imgs)
	for _, img := range imgs {
		dib := encodeDIB(img)
		// A 256px side is stored as 0; nothing here is that big, but the
		// modulo keeps the field honest if a size is ever added.
		dir.WriteByte(byte(img.w % 256))
		dir.WriteByte(byte(img.h % 256))
		dir.WriteByte(0)                                    // palette entries: none, this is true colour
		dir.WriteByte(0)                                    // reserved
		binary.Write(&dir, binary.LittleEndian, uint16(1))  // colour planes
		binary.Write(&dir, binary.LittleEndian, uint16(32)) // bits per pixel
		binary.Write(&dir, binary.LittleEndian, uint32(len(dib)))
		binary.Write(&dir, binary.LittleEndian, uint32(offset+body.Len()))
		body.Write(dib)
	}

	return append(dir.Bytes(), body.Bytes()...)
}

// encodeDIB writes one image as a BITMAPINFOHEADER, the colour rows bottom-up,
// then the AND mask.
//
// biHeight is *twice* the real height: a DIB inside an icon covers the colour
// rows and the mask rows together, and an icon whose header omits the doubling
// renders as garbage or is rejected outright.
func encodeDIB(img *canvas) []byte {
	maskRow := ((img.w + 31) / 32) * 4 // 1bpp rows pad to a 4-byte boundary
	var buf bytes.Buffer

	binary.Write(&buf, binary.LittleEndian, uint32(40))     // biSize
	binary.Write(&buf, binary.LittleEndian, int32(img.w))   // biWidth
	binary.Write(&buf, binary.LittleEndian, int32(img.h*2)) // biHeight
	binary.Write(&buf, binary.LittleEndian, uint16(1))      // biPlanes
	binary.Write(&buf, binary.LittleEndian, uint16(32))     // biBitCount
	binary.Write(&buf, binary.LittleEndian, uint32(0))      // BI_RGB
	binary.Write(&buf, binary.LittleEndian, uint32(img.w*img.h*4+maskRow*img.h))
	binary.Write(&buf, binary.LittleEndian, int32(0))  // biXPelsPerMeter
	binary.Write(&buf, binary.LittleEndian, int32(0))  // biYPelsPerMeter
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // biClrUsed
	binary.Write(&buf, binary.LittleEndian, uint32(0)) // biClrImportant

	// Colour rows, bottom-up.
	for y := img.h - 1; y >= 0; y-- {
		for x := 0; x < img.w; x++ {
			p := img.at(x, y)
			buf.Write([]byte{p.b, p.g, p.r, p.a})
		}
	}

	// The AND mask is left all-zero, i.e. "opaque everywhere". For a 32-bit
	// icon the alpha channel is what actually cuts the shape out; the mask
	// still has to be present and correctly sized or the DIB is malformed.
	buf.Write(make([]byte, maskRow*img.h))

	return buf.Bytes()
}
