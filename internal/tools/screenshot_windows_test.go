//go:build windows

package tools

import (
	"context"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"testing"
)

// A screenshot that is merely "a valid PNG" is not enough: the first version of
// captureScreen produced exactly that while silently cropping to the top-left
// corner on any display scaled above 100%, because it sized the bitmap from
// scaled coordinates and then copied physical pixels into it.
//
// So the capture is measured against the physical desktop, asked for
// independently here rather than through the code under test.
func TestCaptureScreenCoversTheWholePhysicalDesktop(t *testing.T) {
	if testing.Short() {
		t.Skip("needs an interactive desktop session")
	}

	wantW, wantH := physicalVirtualScreen(t)

	path := tempFile("goterm-screenshot-test.png")
	t.Cleanup(func() { _ = os.Remove(path) })

	if err := captureScreen(context.Background(), path); err != nil {
		t.Fatalf("captureScreen: %v", err)
	}

	gotW, gotH := pngSize(t, path)
	if gotW != wantW || gotH != wantH {
		t.Errorf("captured %dx%d, want the full physical desktop %dx%d "+
			"(a smaller capture means the DPI-aware sizing regressed)",
			gotW, gotH, wantW, wantH)
	}
}

// physicalVirtualScreen asks Windows for the virtual desktop in real pixels.
func physicalVirtualScreen(t *testing.T) (int, int) {
	t.Helper()

	script := strings.Join([]string{
		"Add-Type -AssemblyName System.Windows.Forms",
		`Add-Type -MemberDefinition '[DllImport("user32.dll")] public static extern bool SetProcessDPIAware();' ` +
			"-Name Dpi -Namespace Probe > $null",
		"[Probe.Dpi]::SetProcessDPIAware() > $null",
		"$r = [System.Windows.Forms.SystemInformation]::VirtualScreen",
		`"$($r.Width)x$($r.Height)"`,
	}, "; ")

	out, err := psRun(context.Background(), script)
	if err != nil {
		t.Fatalf("probe virtual screen: %v\n%s", err, out)
	}

	w, h, ok := strings.Cut(strings.TrimSpace(out), "x")
	if !ok {
		t.Fatalf("unexpected probe output %q", out)
	}
	wi, err := strconv.Atoi(w)
	if err != nil {
		t.Fatalf("bad width %q: %v", w, err)
	}
	hi, err := strconv.Atoi(h)
	if err != nil {
		t.Fatalf("bad height %q: %v", h, err)
	}
	return wi, hi
}

// pngSize reads the dimensions straight out of the IHDR chunk, so the check
// does not depend on a decoder accepting the whole image.
func pngSize(t *testing.T, path string) (int, int) {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read screenshot: %v", err)
	}
	if len(b) < 24 || string(b[1:4]) != "PNG" {
		t.Fatalf("not a PNG (%d bytes)", len(b))
	}
	// 8-byte signature, 4-byte length, "IHDR", then width and height big-endian.
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24]))
}
