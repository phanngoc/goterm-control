package bot

import (
	"fmt"
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML_Tables(t *testing.T) {
	md := "## So sánh Thị trường\n\n---\n\n| Chỉ số | Nhật Bản | Việt Nam |\n|--------|----------|----------|\n| GDP | ~$4.2T | ~$430B |\n| GDP/đầu người | ~$33,000 | ~$4,300 |\n\n---\n\n### Nhận xét\n\n- Điểm **quan trọng** thứ nhất\n- Điểm _thứ hai_"

	html := markdownToTelegramHTML(md)
	fmt.Println("=== OUTPUT ===")
	fmt.Println(html)

	// Headers should be bold
	if !strings.Contains(html, "<b>") {
		t.Error("headers should be converted to <b>")
	}
	// Tables should be in <pre>
	if !strings.Contains(html, "<pre>") {
		t.Error("tables should be in <pre> blocks")
	}
	// Horizontal rules should be converted to visual separator
	if !strings.Contains(html, "━━━") {
		t.Error("horizontal rules should be converted to ━━━")
	}
	// Raw ## headers should not appear
	if strings.Contains(html, "## ") {
		t.Error("markdown headers should not appear raw")
	}
	// List items should use bullet
	if !strings.Contains(html, "•") {
		t.Error("list items should use bullet")
	}
}
