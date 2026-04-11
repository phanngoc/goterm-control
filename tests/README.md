# Integration Tests

End-to-end tests that verify the full flow: Anthropic API → Agent Loop → Tool execution → Telegram delivery.

## Setup

### 1. Update credentials in `.env`

```bash
# In project root
cat > .env << 'EOF'
ANTHROPIC_API_KEY=sk-ant-api03-...  # Get from https://console.anthropic.com
TELEGRAM_TOKEN=123456:ABC...        # Get from @BotFather on Telegram
EOF
```

### 2. Get your Telegram Chat ID

1. Send any message to your bot on Telegram
2. Run:
   ```bash
   source .env
   curl "https://api.telegram.org/bot${TELEGRAM_TOKEN}/getUpdates" | python3 -m json.tool
   ```
3. Find `"chat":{"id":123456789}` in the response
4. Add to `.env`:
   ```
   TELEGRAM_CHAT_ID=123456789
   ```

### 3. Run tests

```bash
# All integration tests
BOMCLAW_LIVE_TEST=1 go test ./tests/ -v -timeout 120s

# Specific test
BOMCLAW_LIVE_TEST=1 go test ./tests/ -v -run TestLive_AnthropicStreaming
BOMCLAW_LIVE_TEST=1 go test ./tests/ -v -run TestLive_FullRoundtrip

# Skip live tests (default — just runs unit tests)
go test ./internal/...
```

## Test Matrix

| Test | What it verifies | Requires |
|---|---|---|
| `TestLive_AnthropicStreaming` | Direct API streaming works | `ANTHROPIC_API_KEY` |
| `TestLive_AgentLoopSimple` | Agent loop returns text (no tools) | `ANTHROPIC_API_KEY` |
| `TestLive_AgentLoopWithTools` | Agent calls tools and uses results | `ANTHROPIC_API_KEY` |
| `TestLive_ContextEngineWithAgent` | Multi-turn context assembly | `ANTHROPIC_API_KEY` |
| `TestLive_ModelResolverAllModels` | Model catalog works with real API | `ANTHROPIC_API_KEY` |
| `TestLive_TelegramSendMessage` | Bot can send + edit messages | `TELEGRAM_TOKEN`, `TELEGRAM_CHAT_ID` |
| `TestLive_FullRoundtrip` | Complete flow: Telegram → Claude → tools → reply | All three |
| `TestLive_MultiTurnConversation` | Agent remembers context across turns | `ANTHROPIC_API_KEY` |

## Cost

Running all tests costs approximately **$0.01–0.03** (uses `claude-haiku-4-5` which is $0.80/$4.00 per 1M tokens).
