//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The icon is hand-encoded binary that Windows loads with
// LoadImage(LR_LOADFROMFILE). A malformed header does not raise an error there
// — systray logs "unable to set icon" and the tray keeps whatever it had, or
// shows the default blank icon. So the structure is checked here field by field.
func TestEncodeICOStructure(t *testing.T) {
	ico := stateIcon(trayState{})

	if len(ico) < 6 {
		t.Fatalf("icon is %d bytes, far too short", len(ico))
	}

	r := bytes.NewReader(ico)
	var reserved, kind, count uint16
	must(t, binary.Read(r, binary.LittleEndian, &reserved))
	must(t, binary.Read(r, binary.LittleEndian, &kind))
	must(t, binary.Read(r, binary.LittleEndian, &count))

	if reserved != 0 {
		t.Errorf("ICONDIR.reserved = %d, want 0", reserved)
	}
	if kind != 1 {
		t.Errorf("ICONDIR.type = %d, want 1 (icon)", kind)
	}
	if int(count) != len(iconSizes) {
		t.Fatalf("ICONDIR.count = %d, want %d", count, len(iconSizes))
	}

	for i := range int(count) {
		var w, h, palette, pad uint8
		var planes, bpp uint16
		var size, offset uint32
		must(t, binary.Read(r, binary.LittleEndian, &w))
		must(t, binary.Read(r, binary.LittleEndian, &h))
		must(t, binary.Read(r, binary.LittleEndian, &palette))
		must(t, binary.Read(r, binary.LittleEndian, &pad))
		must(t, binary.Read(r, binary.LittleEndian, &planes))
		must(t, binary.Read(r, binary.LittleEndian, &bpp))
		must(t, binary.Read(r, binary.LittleEndian, &size))
		must(t, binary.Read(r, binary.LittleEndian, &offset))

		want := iconSizes[i]
		if int(w) != want || int(h) != want {
			t.Errorf("entry %d is %dx%d, want %dx%d", i, w, h, want, want)
		}
		if planes != 1 || bpp != 32 {
			t.Errorf("entry %d: planes=%d bpp=%d, want 1 and 32", i, planes, bpp)
		}
		if palette != 0 {
			t.Errorf("entry %d: palette=%d, want 0 for true colour", i, palette)
		}
		if int(offset)+int(size) > len(ico) {
			t.Fatalf("entry %d points past the end: offset=%d size=%d file=%d",
				i, offset, size, len(ico))
		}

		checkDIB(t, i, ico[offset:offset+size], want)
	}
}

// checkDIB validates the BITMAPINFOHEADER. The doubled height is the field
// most easily got wrong, and the one that makes Windows reject the icon.
func checkDIB(t *testing.T, idx int, dib []byte, px int) {
	t.Helper()

	if len(dib) < 40 {
		t.Fatalf("entry %d: DIB is %d bytes, shorter than its header", idx, len(dib))
	}
	r := bytes.NewReader(dib)
	var biSize uint32
	var biWidth, biHeight int32
	var biPlanes, biBitCount uint16
	var biCompression uint32
	must(t, binary.Read(r, binary.LittleEndian, &biSize))
	must(t, binary.Read(r, binary.LittleEndian, &biWidth))
	must(t, binary.Read(r, binary.LittleEndian, &biHeight))
	must(t, binary.Read(r, binary.LittleEndian, &biPlanes))
	must(t, binary.Read(r, binary.LittleEndian, &biBitCount))
	must(t, binary.Read(r, binary.LittleEndian, &biCompression))

	if biSize != 40 {
		t.Errorf("entry %d: biSize = %d, want 40", idx, biSize)
	}
	if int(biWidth) != px {
		t.Errorf("entry %d: biWidth = %d, want %d", idx, biWidth, px)
	}
	// An icon DIB spans the colour rows AND the mask rows.
	if int(biHeight) != px*2 {
		t.Errorf("entry %d: biHeight = %d, want %d (2x for the AND mask)", idx, biHeight, px*2)
	}
	if biPlanes != 1 || biBitCount != 32 {
		t.Errorf("entry %d: planes=%d bpp=%d, want 1 and 32", idx, biPlanes, biBitCount)
	}
	if biCompression != 0 {
		t.Errorf("entry %d: biCompression = %d, want 0 (BI_RGB) — LoadImage cannot read a compressed icon",
			idx, biCompression)
	}

	maskRow := ((px + 31) / 32) * 4
	wantLen := 40 + px*px*4 + maskRow*px
	if len(dib) != wantLen {
		t.Errorf("entry %d: DIB is %d bytes, want %d (header + pixels + mask)", idx, len(dib), wantLen)
	}
}

// decodeFirstImage returns the colour rows of the icon's first entry, restored
// to top-row-first order.
func decodeFirstImage(t *testing.T, ico []byte) (int, int, []rgba) {
	t.Helper()

	// ICONDIR is 6 bytes; entry 0's size and offset are its last 8.
	size := binary.LittleEndian.Uint32(ico[6+8 : 6+12])
	offset := binary.LittleEndian.Uint32(ico[6+12 : 6+16])
	dib := ico[offset : uint32(offset)+size]

	w := int(int32(binary.LittleEndian.Uint32(dib[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(dib[8:12]))) / 2 // header doubles it

	pix := make([]rgba, w*h)
	p := 40
	for y := h - 1; y >= 0; y-- { // stored bottom-up
		for x := 0; x < w; x++ {
			pix[y*w+x] = rgba{b: dib[p], g: dib[p+1], r: dib[p+2], a: dib[p+3]}
			p += 4
		}
	}
	return w, h, pix
}

// A blank icon is the failure this catches: if the drawing produced nothing,
// every pixel would be fully transparent and the tray would show an empty slot.
// The corners must stay clear, though — the dot is a circle, not a square.
func TestStateIconIsNotBlank(t *testing.T) {
	w, h, pix := decodeFirstImage(t, stateIcon(trayState{}))

	opaque := 0
	for _, p := range pix {
		if p.a == 0xFF {
			opaque++
		}
	}
	// A disc inscribed in the square covers about pi/4 of it; well over a
	// third is a safe floor and still fails a blank or hairline icon.
	if min := w * h / 3; opaque < min {
		t.Errorf("%d of %d pixels are opaque, want at least %d — the icon looks blank",
			opaque, w*h, min)
	}
	if got := pix[0]; got.a != 0 {
		t.Errorf("top-left corner has alpha %d, want 0 — the dot should not fill the square", got.a)
	}
}

// rgba stores channels in DIB order (b, g, r, a), so a positional literal
// reads as RGBA and quietly swaps red and blue — which is exactly what
// happened: "down" shipped rendering blue. Comparing a decoded pixel against
// the same constant cannot catch that, because both sides are wrong together.
// These assertions are about hue, so a channel swap fails them.
func TestStateColoursAreNotChannelSwapped(t *testing.T) {
	cases := []struct {
		name  string
		col   rgba
		check func(rgba) bool
		want  string
	}{
		{"down is red", colDown, func(c rgba) bool { return c.r > c.g && c.r > c.b }, "red dominant"},
		{"running is green", colRunning, func(c rgba) bool { return c.g > c.r && c.g > c.b }, "green dominant"},
		{"idle is a blue-grey", colIdle, func(c rgba) bool { return c.b > c.r }, "blue above red"},
		{"awake is amber", colAwake, func(c rgba) bool { return c.r > c.b && c.g > c.b }, "red and green above blue"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.check(c.col) {
				t.Errorf("%+v is not %s — channels look swapped", c.col, c.want)
			}
			if c.col.a != 0xFF {
				t.Errorf("%+v should be fully opaque", c.col)
			}
		})
	}
}

// And the same again after a full round trip through the encoder, so a swap
// introduced in encodeDIB rather than the constants is caught too.
func TestEncodedPixelsKeepChannelOrder(t *testing.T) {
	w, h, pix := decodeFirstImage(t, stateIcon(trayState{down: true}))
	c := pix[(h/2)*w+w/2]
	if !(c.r > c.g && c.r > c.b) {
		t.Errorf("the down icon's centre pixel is %+v; red should dominate", c)
	}
}

// The colour is the whole signal on Windows, since there is no title to read.
func TestStateIconCentrePixelEncodesTheState(t *testing.T) {
	cases := map[string]struct {
		state trayState
		want  rgba
	}{
		"idle":    {trayState{}, colIdle},
		"running": {trayState{running: true}, colRunning},
		"down":    {trayState{down: true}, colDown},
		// down outranks running: a gateway that vanished mid-run is a problem
		// to surface, not a run to celebrate.
		"down while running": {trayState{down: true, running: true}, colDown},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w, h, pix := decodeFirstImage(t, stateIcon(c.state))
			got := pix[(h/2)*w+w/2]
			if got != c.want {
				t.Errorf("centre pixel = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The awake pip has to be visible as a distinct mark, not blended into the dot.
func TestAwakePipIsDrawn(t *testing.T) {
	w, h, pix := decodeFirstImage(t, stateIcon(trayState{awake: true}))

	found := false
	for _, p := range pix {
		if p == colAwake {
			found = true
			break
		}
	}
	if !found {
		t.Error("no amber pip pixel found in the awake icon")
	}

	// And the centre is still the state colour underneath it.
	if got := pix[(h/2)*w+w/2]; got != colIdle {
		t.Errorf("centre pixel = %+v, want the idle colour — the pip overran the dot", got)
	}
}

// systray caches Win32 icon handles keyed on a hash of these bytes, so two
// states that encoded identically would leave the tray showing a stale colour.
func TestStateIconsDifferPerState(t *testing.T) {
	states := map[string]trayState{
		"idle":          {},
		"running":       {running: true},
		"down":          {down: true},
		"idle awake":    {awake: true},
		"running awake": {running: true, awake: true},
	}

	seen := map[string]string{}
	for name, s := range states {
		key := string(stateIcon(s))
		if other, dup := seen[key]; dup {
			t.Errorf("%q and %q encode to identical icons", name, other)
		}
		seen[key] = name
	}
}

// stateIcon is called on every poll, so it must return the cached bytes rather
// than redrawing and re-hashing a few times a second.
func TestStateIconIsCached(t *testing.T) {
	a := stateIcon(trayState{running: true})
	b := stateIcon(trayState{running: true})
	if &a[0] != &b[0] {
		t.Error("expected the cached slice to be returned, not a fresh encode")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
}
