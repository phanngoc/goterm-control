package bot

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// attachment is a single Telegram media file queued for download.
type attachment struct {
	fileID   string
	filename string
}

// hasMedia reports whether the message carries a downloadable attachment.
func hasMedia(msg *tgbotapi.Message) bool {
	return len(msg.Photo) > 0 || msg.Document != nil || msg.Audio != nil ||
		msg.Voice != nil || msg.Video != nil
}

// collectAttachments enumerates the downloadable files on a message.
// A single message can carry at most one of each media type.
func collectAttachments(msg *tgbotapi.Message) []attachment {
	var atts []attachment

	if len(msg.Photo) > 0 {
		// Telegram sends several resolutions; the last entry is the largest.
		largest := msg.Photo[len(msg.Photo)-1]
		atts = append(atts, attachment{
			fileID:   largest.FileID,
			filename: fmt.Sprintf("photo_%d.jpg", msg.MessageID),
		})
	}
	if msg.Document != nil {
		atts = append(atts, attachment{
			fileID:   msg.Document.FileID,
			filename: mediaName(msg.Document.FileName, msg.MessageID, ".bin"),
		})
	}
	if msg.Audio != nil {
		atts = append(atts, attachment{
			fileID:   msg.Audio.FileID,
			filename: mediaName(msg.Audio.FileName, msg.MessageID, ".mp3"),
		})
	}
	if msg.Voice != nil {
		atts = append(atts, attachment{
			fileID:   msg.Voice.FileID,
			filename: fmt.Sprintf("voice_%d.ogg", msg.MessageID),
		})
	}
	if msg.Video != nil {
		atts = append(atts, attachment{
			fileID:   msg.Video.FileID,
			filename: mediaName(msg.Video.FileName, msg.MessageID, ".mp4"),
		})
	}

	return atts
}

// mediaName sanitizes a sender-supplied filename and prefixes it with the
// message ID to avoid collisions. Falls back to file_<msgID><ext> when empty.
func mediaName(name string, msgID int, fallbackExt string) string {
	name = sanitizeFilename(name)
	if name == "" {
		return fmt.Sprintf("file_%d%s", msgID, fallbackExt)
	}
	return fmt.Sprintf("%d_%s", msgID, name)
}

// sanitizeFilename strips directory components and path separators so a
// malicious filename can't escape the upload directory.
func sanitizeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	switch name {
	case ".", "..", "/", "\\":
		return ""
	}
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	return name
}

// handleMedia downloads every attachment on a message into the per-chat upload
// directory inside the Claude workspace, then submits a prompt referencing the
// saved file paths so Claude reads and processes them via its Read tool.
func (h *Handler) handleMedia(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	atts := collectAttachments(msg)
	if len(atts) == 0 {
		return
	}

	uploadDir := filepath.Join(h.cfg.Claude.Workspace, "uploads", strconv.FormatInt(chatID, 10))
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		log.Printf("media: mkdir %s: %v", uploadDir, err)
		h.sendText(chatID, "❌ Không tạo được thư mục lưu tệp.")
		return
	}

	var paths []string
	failed := 0
	for _, a := range atts {
		dest := filepath.Join(uploadDir, a.filename)
		if err := h.downloadTelegramFile(a.fileID, dest); err != nil {
			log.Printf("media: download %s → %s: %v", a.fileID, dest, err)
			failed++
			continue
		}
		paths = append(paths, dest)
	}

	if len(paths) == 0 {
		h.sendText(chatID, "❌ Tải tệp từ Telegram thất bại (lưu ý: bot chỉ tải được tệp ≤ 20MB).")
		return
	}
	if failed > 0 {
		h.sendText(chatID, fmt.Sprintf("⚠️ %d tệp tải thất bại — tiếp tục với %d tệp.", failed, len(paths)))
	}

	prompt := buildMediaPrompt(strings.TrimSpace(msg.Caption), paths)
	h.queue.Submit(chatID, prompt)
}

// downloadTelegramFile resolves the Telegram file URL and streams it to dest.
func (h *Handler) downloadTelegramFile(fileID, dest string) error {
	url, err := h.bot.GetFileDirectURL(fileID)
	if err != nil {
		return fmt.Errorf("get file url: %w", err)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// buildMediaPrompt assembles the text handed to Claude, combining the user's
// caption (if any) with explicit instructions to read the saved file paths.
func buildMediaPrompt(caption string, paths []string) string {
	var sb strings.Builder

	if caption != "" {
		sb.WriteString(caption)
		sb.WriteString("\n\n")
	}

	if len(paths) == 1 {
		sb.WriteString("📎 Người dùng vừa gửi 1 tệp đính kèm. Hãy dùng công cụ Read để mở và xử lý:\n")
	} else {
		sb.WriteString(fmt.Sprintf("📎 Người dùng vừa gửi %d tệp đính kèm. Hãy dùng công cụ Read để mở và xử lý:\n", len(paths)))
	}
	for _, p := range paths {
		sb.WriteString("- " + p + "\n")
	}

	if caption == "" {
		sb.WriteString("\n(Không có chú thích — hãy xem nội dung tệp rồi mô tả hoặc xử lý phù hợp.)")
	}

	return sb.String()
}
