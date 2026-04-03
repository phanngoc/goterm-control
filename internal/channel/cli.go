package channel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// CLIChannel implements Channel for stdin/stdout interactive use.
type CLIChannel struct{}

func NewCLI() *CLIChannel { return &CLIChannel{} }

func (c *CLIChannel) ID() string { return "cli" }

func (c *CLIChannel) Start(ctx context.Context, handler InboundHandler) error {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("NanoClaw CLI — type a message or /quit to exit")
	fmt.Print("> ")

	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			fmt.Print("> ")
			continue
		}
		if text == "/quit" || text == "/exit" {
			return nil
		}

		msg := InboundMessage{
			ChannelID: "cli",
			ChatID:    "cli_0",
			UserID:    "local",
			Text:      text,
		}

		// Parse commands
		if strings.HasPrefix(text, "/") {
			parts := strings.SplitN(text[1:], " ", 2)
			msg.Command = parts[0]
			if len(parts) > 1 {
				msg.Args = parts[1]
			}
			msg.Text = ""
		}

		reply := handler(ctx, msg)
		if reply != nil && reply.Text != "" {
			fmt.Println(reply.Text)
		}
		fmt.Print("> ")
	}

	return scanner.Err()
}

func (c *CLIChannel) Send(_ context.Context, _ string, msg OutboundMessage) error {
	if msg.Text != "" {
		fmt.Println(msg.Text)
	}
	if msg.ImagePath != "" {
		fmt.Printf("[image: %s]\n", msg.ImagePath)
	}
	return nil
}

func (c *CLIChannel) Stop() error { return nil }
