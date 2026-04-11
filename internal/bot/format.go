package bot

import (
	"fmt"
	"regexp"
	"strings"
)

// --- Markdown to Telegram HTML conversion ---
// Ported from goclaw's format.go, simplified (no table rendering).
// Converts LLM markdown output to Telegram-safe HTML for parse_mode: "HTML".

// htmlTagToMarkdown converts common HTML tags in LLM output to markdown equivalents
// so they survive the escapeHTML step and get re-converted by the markdown pipeline.
var htmlToMdReplacers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`(?i)<br\s*/?>`), "\n"},
	{regexp.MustCompile(`(?i)</?p\s*>`), "\n"},
	{regexp.MustCompile(`(?i)<b>([\s\S]*?)</b>`), "**$1**"},
	{regexp.MustCompile(`(?i)<strong>([\s\S]*?)</strong>`), "**$1**"},
	{regexp.MustCompile(`(?i)<i>([\s\S]*?)</i>`), "_$1_"},
	{regexp.MustCompile(`(?i)<em>([\s\S]*?)</em>`), "_$1_"},
	{regexp.MustCompile(`(?i)<s>([\s\S]*?)</s>`), "~~$1~~"},
	{regexp.MustCompile(`(?i)<strike>([\s\S]*?)</strike>`), "~~$1~~"},
	{regexp.MustCompile(`(?i)<del>([\s\S]*?)</del>`), "~~$1~~"},
	{regexp.MustCompile(`(?i)<code>([\s\S]*?)</code>`), "`$1`"},
	{regexp.MustCompile(`(?i)<a\s+href="([^"]+)"[^>]*>([\s\S]*?)</a>`), "[$2]($1)"},
}

func htmlTagToMarkdown(text string) string {
	for _, r := range htmlToMdReplacers {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return text
}

// markdownToTelegramHTML converts markdown text to Telegram HTML.
func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	// Pre-process: convert any HTML tags in LLM output to markdown equivalents.
	text = htmlTagToMarkdown(text)

	// Extract and protect code blocks
	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	// Extract and protect inline code
	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	// Extract and protect tables (like code blocks — restored after escaping)
	tables := extractTables(text)
	text = tables.text

	// Convert horizontal rules (---, ***, ___) to visual separator
	text = reHorizontalRule.ReplaceAllString(text, "━━━━━━━━━━━━")

	// Convert markdown headers to bold (via markdown bold, handled after escaping)
	text = reHeader.ReplaceAllString(text, "**$1**")

	// Convert blockquotes: > text → <blockquote>text</blockquote>
	text = convertBlockquotes(text)

	// Escape HTML
	text = escapeHTML(text)

	// Convert markdown links
	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	// Bold
	text = reBoldAsterisks.ReplaceAllString(text, "<b>$1</b>")
	text = reBoldUnderscores.ReplaceAllString(text, "<b>$1</b>")

	// Protect @mentions from italic conversion.
	var mentionPlaceholders []string
	text = reMention.ReplaceAllStringFunc(text, func(s string) string {
		match := reMention.FindStringSubmatch(s)
		if len(match) < 3 {
			return s
		}
		idx := len(mentionPlaceholders)
		mentionPlaceholders = append(mentionPlaceholders, match[2])
		return match[1] + fmt.Sprintf("\x00MN%d\x00", idx)
	})

	// Italic
	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})

	// Strikethrough
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	// Restore @mentions as plain text
	for i, mention := range mentionPlaceholders {
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00MN%d\x00", i), mention)
	}

	// List items
	text = reListItem.ReplaceAllString(text, "• ")

	// Restore inline code
	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), "<code>"+escaped+"</code>")
	}

	// Restore code blocks
	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00CB%d\x00", i), "<pre><code>"+escaped+"</code></pre>")
	}

	// Restore tables
	for i, block := range tables.blocks {
		escaped := escapeHTML(block)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00TB%d\x00", i), "<pre>"+escaped+"</pre>")
	}

	return text
}

// --- Compiled regexes ---

var (
	reHeader         = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reHorizontalRule = regexp.MustCompile(`(?m)^[\t ]*[-*_]{3,}[\t ]*$`)
	reLink           = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldAsterisks  = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnderscores = regexp.MustCompile(`__(.+?)__`)
	reMention        = regexp.MustCompile(`(^|\W)(@\w+)`)
	reItalic         = regexp.MustCompile(`_([^_]+)_`)
	reStrike         = regexp.MustCompile(`~~(.+?)~~`)
	reListItem       = regexp.MustCompile(`(?m)^[-*]\s+`)
	reCodeBlock      = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode     = regexp.MustCompile("`([^`]+)`")
	reBlockquoteLine = regexp.MustCompile(`(?m)^>\s?(.*)$`)
	reTableRow       = regexp.MustCompile(`^\|(.+)\|$`)
	reTableSep       = regexp.MustCompile(`^\|[\s:\-|]+\|$`)
	reHTMLTag        = regexp.MustCompile(`<[^>]+>`)
)

// --- Blockquote conversion ---

// convertBlockquotes converts consecutive `> ` prefixed lines into <blockquote> blocks.
func convertBlockquotes(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inQuote := false

	for _, line := range lines {
		match := reBlockquoteLine.FindStringSubmatch(line)
		if match != nil {
			if !inQuote {
				result = append(result, "<blockquote>")
				inQuote = true
			}
			result = append(result, match[1])
		} else {
			if inQuote {
				result = append(result, "</blockquote>")
				inQuote = false
			}
			result = append(result, line)
		}
	}
	if inQuote {
		result = append(result, "</blockquote>")
	}

	return strings.Join(result, "\n")
}

// --- Table extraction ---

type tableMatch struct {
	text   string
	blocks []string
}

// extractTables finds markdown tables and replaces them with placeholders.
// Returns the modified text and the rendered table blocks (plain text, not yet HTML-escaped).
func extractTables(text string) tableMatch {
	lines := strings.Split(text, "\n")
	var result []string
	var tableRows [][]string
	var blocks []string

	flushTable := func() {
		if len(tableRows) == 0 {
			return
		}
		// Calculate column widths
		var colWidths []int
		for _, row := range tableRows {
			for i, cell := range row {
				if i >= len(colWidths) {
					colWidths = append(colWidths, 0)
				}
				if cellLen := len([]rune(cell)); cellLen > colWidths[i] {
					colWidths[i] = cellLen
				}
			}
		}

		// Cap column widths for narrow Telegram display
		for i := range colWidths {
			if colWidths[i] > 24 {
				colWidths[i] = 24
			}
		}

		// Render rows
		var pre strings.Builder
		for ri, row := range tableRows {
			for ci, cell := range row {
				if ci >= len(colWidths) {
					break
				}
				runes := []rune(cell)
				width := colWidths[ci]
				if len(runes) > width {
					runes = runes[:width]
				}
				pre.WriteString(string(runes))
				for j := len(runes); j < width; j++ {
					pre.WriteByte(' ')
				}
				if ci < len(row)-1 {
					pre.WriteString(" | ")
				}
			}
			pre.WriteByte('\n')
			// Separator after header
			if ri == 0 {
				for ci := range colWidths {
					for j := 0; j < colWidths[ci]; j++ {
						pre.WriteByte('-')
					}
					if ci < len(colWidths)-1 {
						pre.WriteString("-+-")
					}
				}
				pre.WriteByte('\n')
			}
		}

		result = append(result, fmt.Sprintf("\x00TB%d\x00", len(blocks)))
		blocks = append(blocks, pre.String())
		tableRows = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip separator rows (|---|---|)
		if reTableSep.MatchString(trimmed) {
			continue
		}

		if reTableRow.MatchString(trimmed) {
			inner := reTableRow.FindStringSubmatch(trimmed)[1]
			cells := strings.Split(inner, "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			tableRows = append(tableRows, cells)
			continue
		}

		// Non-table line — flush any pending table
		flushTable()
		result = append(result, line)
	}
	flushTable()

	return tableMatch{text: strings.Join(result, "\n"), blocks: blocks}
}

// --- Code block extraction ---

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlock.FindAllStringSubmatch(text, -1)
	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlock.ReplaceAllStringFunc(text, func(_ string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	matches := reInlineCode.FindAllStringSubmatch(text, -1)
	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(_ string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

// --- HTML utilities ---

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// stripHTML removes all HTML tags for plain-text fallback.
func stripHTML(text string) string {
	// Decode common entities first
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", `"`)
	// Strip tags
	text = reHTMLTag.ReplaceAllString(text, "")
	return text
}

// --- HTML-safe message chunking ---

// chunkHTML splits HTML text into chunks that fit within maxLen.
// Prefers splitting at paragraph boundaries (\n\n), then line (\n), then word (space).
// Avoids splitting inside HTML tags or entities.
func chunkHTML(text string, maxLen int) []string {
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for len(remaining) > 0 {
		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		cutAt := maxLen

		// 1. Look for preferred boundaries: paragraph, then newline, then space.
		if idx := strings.LastIndex(remaining[:cutAt], "\n\n"); idx > 0 {
			cutAt = idx + 2
		} else if idx := strings.LastIndex(remaining[:cutAt], "\n"); idx > 0 {
			cutAt = idx + 1
		} else if idx := strings.LastIndex(remaining[:cutAt], " "); idx > 0 {
			cutAt = idx + 1
		}

		// 2. Safety: don't cut in the middle of an HTML tag.
		if lastOpen := strings.LastIndex(remaining[:cutAt], "<"); lastOpen != -1 {
			lastClose := strings.LastIndex(remaining[:cutAt], ">")
			if lastOpen > lastClose {
				cutAt = lastOpen
			}
		}

		// 3. Safety: don't cut in the middle of an HTML entity.
		if lastAmp := strings.LastIndex(remaining[:cutAt], "&"); lastAmp != -1 {
			lastSemi := strings.LastIndex(remaining[:cutAt], ";")
			if lastAmp > lastSemi {
				cutAt = lastAmp
			}
		}

		// 4. Fallback: if safety moved cutAt to 0, force progress.
		if cutAt <= 0 {
			cutAt = maxLen
		}

		chunk := strings.TrimRight(remaining[:cutAt], " \n")
		remaining = strings.TrimLeft(remaining[cutAt:], " \n")

		// Markdown-safe chunking: if we cut inside a <pre><code> block,
		// close it at the end of this chunk and reopen in the next chunk
		// so each chunk is valid HTML. (openclaw pattern)
		preOpens := strings.Count(chunk, "<pre>") + strings.Count(chunk, "<pre><code>")
		preCloses := strings.Count(chunk, "</pre>") + strings.Count(chunk, "</code></pre>")
		if preOpens > preCloses {
			codeOpens := strings.Count(chunk, "<code>")
			codeCloses := strings.Count(chunk, "</code>")
			if codeOpens > codeCloses {
				chunk += "</code></pre>"
				remaining = "<pre><code>" + remaining
			} else {
				chunk += "</pre>"
				remaining = "<pre>" + remaining
			}
		}

		chunks = append(chunks, chunk)
	}

	return chunks
}
