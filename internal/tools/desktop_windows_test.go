//go:build windows

package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPSLiteralIsParenthesised(t *testing.T) {
	got := psLiteral("hi")
	if !strings.HasPrefix(got, "(") || !strings.HasSuffix(got, ")") {
		t.Fatalf("psLiteral must be parenthesised to bind as a cmdlet argument, got %s", got)
	}
}

// psLiteral is interpolated straight after a cmdlet parameter name
// (Set-Clipboard -Value, Start-Process -FilePath). PowerShell parses those
// arguments in command mode, where a bare [Type]::Method(...) does not bind —
// it fails with "A positional parameter cannot be found that accepts argument
// 'System.Byte[]'". This ran real PowerShell because that error is a parse-time
// binding behaviour no amount of string assertion would have caught.
func TestPSLiteralBindsToACmdletParameter(t *testing.T) {
	const want = "xin chào — 🦀 \"quoted\" $notAVar `backtick"

	out, err := psRun(context.Background(), "Write-Output -InputObject "+psLiteral(want))
	if err != nil {
		t.Fatalf("psRun: %v\n%s", err, out)
	}
	if out != want {
		t.Errorf("round trip through a cmdlet parameter failed:\n got %q\nwant %q", out, want)
	}
}

// The same value must survive being interpolated into a .NET method argument
// list, which is how captureScreen passes the output path.
func TestPSLiteralBindsInsideAMethodCall(t *testing.T) {
	const want = "C:\\Users\\Some Name\\shot 1.png"

	out, err := psRun(context.Background(), "[System.IO.Path]::GetFileName("+psLiteral(want)+")")
	if err != nil {
		t.Fatalf("psRun: %v\n%s", err, out)
	}
	if out != "shot 1.png" {
		t.Errorf("got %q, want %q", out, "shot 1.png")
	}
}

// Stdout has to arrive as UTF-8; without the encoding line psRun sets, PowerShell
// emits the console's OEM code page and non-ASCII reaches Go as mojibake.
func TestPSRunReturnsUTF8(t *testing.T) {
	out, err := psRun(context.Background(), "Write-Output 'Ẩn — 日本語'")
	if err != nil {
		t.Fatalf("psRun: %v\n%s", err, out)
	}
	if out != "Ẩn — 日本語" {
		t.Errorf("got %q, want %q", out, "Ẩn — 日本語")
	}
}
