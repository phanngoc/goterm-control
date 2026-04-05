#!/bin/bash
# NanoClaw — start all services
# Usage: ./start.sh [telegram|gateway|chat|all]

set -e
cd "$(dirname "$0")"

# Load .env
if [ -f .env ]; then
  set -a; source .env; set +a
fi

MODE="${1:-all}"
GATEWAY_PORT="${GATEWAY_PORT:-19000}"

build() {
  echo "⚙️  Building..."
  go build -o goterm ./cmd/goterm/
  go build -o nanoclaw ./cmd/nanoclaw/
  echo "✅ Built: goterm + nanoclaw"
}

start_telegram() {
  echo "📱 Starting Telegram bot (@Goterm_bot)..."
  ./goterm -config config.yaml -env .env &
  echo "   PID: $!"
}

start_gateway() {
  echo "🌐 Starting gateway on :${GATEWAY_PORT}..."
  ./nanoclaw gateway --config config.yaml --env .env --port "$GATEWAY_PORT" &
  echo "   PID: $!"
}

stop_all() {
  echo "🛑 Stopping..."
  pkill -f "./goterm -config" 2>/dev/null || true
  pkill -f "./nanoclaw gateway" 2>/dev/null || true
  sleep 1
}

case "$MODE" in
  telegram)
    build
    stop_all
    start_telegram
    echo "✅ Telegram bot running. Send messages to @Goterm_bot"
    ;;
  gateway)
    build
    stop_all
    start_gateway
    echo "✅ Gateway running. Use: ./nanoclaw send \"hello\""
    ;;
  chat)
    build
    echo "💬 Starting interactive chat..."
    ./nanoclaw chat --config config.yaml --env .env
    ;;
  all)
    build
    stop_all
    start_telegram
    start_gateway
    echo ""
    echo "✅ All services running:"
    echo "   📱 Telegram: @Goterm_bot"
    echo "   🌐 Gateway:  ws://127.0.0.1:${GATEWAY_PORT}/ws"
    echo "   📊 Health:   http://127.0.0.1:${GATEWAY_PORT}/health"
    echo ""
    echo "Commands:"
    echo "   ./nanoclaw send \"hello\"              # send via gateway"
    echo "   ./nanoclaw status                     # check status"
    echo "   ./nanoclaw models                     # list models"
    echo "   ./start.sh stop                       # stop all"
    ;;
  stop)
    stop_all
    echo "✅ All stopped."
    ;;
  *)
    echo "Usage: ./start.sh [telegram|gateway|chat|all|stop]"
    exit 1
    ;;
esac

wait
