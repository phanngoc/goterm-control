#!/bin/bash
# BomClaw — start all services
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
  go build -o bomclaw ./cmd/bomclaw/
  echo "✅ Built: bomclaw"
}

start_gateway() {
  echo "🌐 Starting gateway (+ Telegram bot) on :${GATEWAY_PORT}..."
  ./bomclaw gateway --config config.yaml --env .env --port "$GATEWAY_PORT" &
  echo "   PID: $!"
}

stop_all() {
  echo "🛑 Stopping..."
  pkill -f "./bomclaw gateway" 2>/dev/null || true
  sleep 1
}

case "$MODE" in
  gateway|all)
    build
    stop_all
    start_gateway
    echo ""
    echo "✅ Gateway running (Telegram bot auto-starts if token configured):"
    echo "   📱 Telegram: @Goterm_bot"
    echo "   🌐 Gateway:  ws://127.0.0.1:${GATEWAY_PORT}/ws"
    echo "   📊 Health:   http://127.0.0.1:${GATEWAY_PORT}/health"
    echo ""
    echo "Commands:"
    echo "   ./bomclaw send \"hello\"              # send via gateway"
    echo "   ./bomclaw status                     # check status"
    echo "   ./bomclaw models                     # list models"
    echo "   ./start.sh stop                       # stop all"
    ;;
  chat)
    build
    echo "💬 Starting interactive chat..."
    ./bomclaw chat --config config.yaml --env .env
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
