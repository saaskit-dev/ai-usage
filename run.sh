#!/bin/bash
set -e

PROJECT_DIR="/Users/dev/ai-usage"
cd "$PROJECT_DIR"

echo "🔨 Building..."
go build -o bin/ai-usage ./cmd/ai-usage

echo "🚀 Starting daemon..."
ARGS=(--addr :8080 --interval 30s --config ./config.yaml)

# Add apprise URLs from environment if set (comma-separated)
# Example: AI_USAGE_APPRISE_URLS="schan://key,discord://id/token"
if [ -n "${AI_USAGE_APPRISE_URLS:-}" ]; then
  IFS=',' read -ra URLS <<< "$AI_USAGE_APPRISE_URLS"
  for url in "${URLS[@]}"; do
    ARGS+=(--apprise "$url")
  done
fi

./bin/ai-usage "${ARGS[@]}" &

DAEMON_PID=$!
sleep 3

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  AI Usage Monitor Daemon Started"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  API:     http://localhost:8080"
echo "  Health:  http://localhost:8080/healthz"
echo "  Usage:   http://localhost:8080/usage"
echo ""

# Check notification config from the running instance
NOTIFY_STATUS=$(curl -s http://localhost:8080/config 2>/dev/null | jq -r '.notifications_active // empty' 2>/dev/null)
if [ "$NOTIFY_STATUS" = "true" ]; then
  echo "  Notifications: Active"
else
  echo "  Notifications: None configured"
  echo "  (Set apprise_urls in config.yaml or AI_USAGE_APPRISE_URLS env)"
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

echo "📊 Testing API..."
curl -s http://localhost:8080/healthz | jq . 2>/dev/null || echo "(healthz not ready yet)"
echo ""

echo "📈 Current usage:"
curl -s http://localhost:8080/usage | jq . 2>/dev/null || echo "(usage not ready yet)"
echo ""

echo "✅ Daemon is running (PID: $DAEMON_PID). Press Ctrl+C to stop."
echo ""
echo "To test notification, run:"
echo "  curl -X POST http://localhost:8080/notify \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"title\":\"Test\",\"body\":\"Hello from ai-usage\"}'"
echo ""

wait
