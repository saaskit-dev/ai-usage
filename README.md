<!-- markdownlint-disable MD001 MD026 -->
<div align="center">

# AI Usage Monitor

Monitor AI service usage (Claude, Copilot, Cursor) with real-time metrics and notifications.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://github.com/saaskit-dev/ai-usage)
[![Release](https://img.shields.io/github/v/release/saaskit-dev/ai-usage)](https://github.com/saaskit-dev/ai-usage/releases)

</div>

## Features

- **Multi-Provider Support**: Claude (API), Copilot, Cursor
- **Real-time Monitoring**: Track quota usage in real-time
- **REST API**: Built-in API server for programmatic access
- **Notifications**: Apprise integration (Discord, Telegram, Slack, WeChat, etc.)
- **Auto-Restart**: Process monitoring with automatic restart on failure
- **Cross-Platform**: macOS & Linux support
- **Auto-Start**: launchd (macOS) / systemd (Linux) integration

## Quick Start

### Installation

#### Homebrew (Recommended)
```bash
brew tap saaskit-dev/tap
brew install ai-usage
```

#### Install Script
```bash
curl -sL https://raw.githubusercontent.com/saaskit-dev/ai-usage/main/install.sh | bash
```

#### Manual
Download from [Releases](https://github.com/saaskit-dev/ai-usage/releases):

```bash
# macOS
curl -sL https://github.com/saaskit-dev/ai-usage/releases/latest/download/ai-usage-darwin-arm64.tar.gz | tar -xz
sudo mv ai-usage /usr/local/bin/

# Linux
curl -sL https://github.com/saaskit-dev/ai-usage/releases/latest/download/ai-usage-linux-amd64.tar.gz | tar -xz
sudo mv ai-usage /usr/local/bin/
```

### Configuration

1. Copy the example config:
```bash
mkdir -p ~/.config/ai-usage
cp /usr/local/etc/ai-usage.example.yaml ~/.config/ai-usage/config.yaml
# Or on Linux: cp ~/.local/share/ai-usage/config.yaml.example ~/.config/ai-usage/config.yaml
```

2. Edit the config:
```bash
vim ~/.config/ai-usage/config.yaml
```

3. Run:
```bash
ai-usage
```

4. Enable auto-start:
```bash
ai-usage daemon install
```

### Configuration Reference

```yaml
server:
  addr: ":8080"          # API server listen address

monitor:
  interval: "60s"        # Probe interval
  data_file: "./data/usage.json"

notify:
  apprise_urls:
    - "discord://webhook_id/webhook_token"
    - "tgram://bot_token/chat_id"
    - "schan://sendkey"
  rules:
    - event: depleted           # Alert when quota is fully depleted
    - event: probe_error        # Alert when API probe fails
    - event: threshold         # Alert when quota drops below threshold
      threshold: 50
    - event: threshold
      threshold: 20
    - event: reset_soon        # Alert before quota resets
      before: "10m"

providers:
  claude:
    enabled: true
    # paths:                   # Additional Claude credential paths
    #   - ~/.claude-work/
  copilot:
    enabled: true
    token: "your-github-token"
  cursor:
    enabled: true
    token: "your-cursor-token"
```

### API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /` | Health check |
| `GET /api/v1/usage` | Current usage for all providers |
| `GET /api/v1/usage/claude` | Claude usage only |
| `GET /api/v1/usage/copilot` | Copilot usage only |
| `GET /api/v1/usage/cursor` | Cursor usage only |
| `GET /api/v1/providers` | Provider status |
| `GET /metrics` | Prometheus metrics |

Example:
```bash
curl http://localhost:8080/api/v1/usage
```

Response:
```json
{
  "claude": {
    "email": "user@example.com",
    "plan": "Pro",
    "usage": 45.2,
    "limit": 100,
    "unit": "USD",
    "reset_at": "2024-01-15T00:00:00Z"
  },
  "copilot": {
    "plan": "Pro",
    "usage": 1245,
    "limit": 5000,
    "unit": "requests"
  },
  "cursor": {
    "usage": 8500,
    "limit": 50000,
    "unit": "requests"
  }
}
```

## Usage

```
AI usage monitoring daemon

Usage:
  ai-usage [flags]
  ai-usage [command]

Available Commands:
  daemon      Manage ai-usage daemon
  help        Help about any command

Flags:
  -a, --addr string           API listen address (default ":8080")
  -c, --config string         Config file path
  -i, --interval duration     Provider probe interval (default 1m0s)
  -n, --apprise stringArray  Notification URLs

Daemon Commands:
  ai-usage daemon install     Install and enable auto-start
  ai-usage daemon uninstall   Uninstall and disable auto-start
  ai-usage daemon start       Start the daemon
  ai-usage daemon stop        Stop the daemon
  ai-usage daemon restart     Restart the daemon
  ai-usage daemon status      Show daemon status
```

## Notifications

Supported notification channels via [Apprise](https://github.com/caronc/apprise):

```yaml
notify:
  apprise_urls:
    # Discord
    - "discord://webhook_id/webhook_token"

    # Telegram
    - "tgram://bot_token/chat_id"

    # Slack
    - "slack://token_a/token_b/token_c"

    # ServerChan (WeChat)
    - "schan://SCTxxxxx"

    # Custom Webhook
    - "json://webhook.example.com/notify"
```

## Development

```bash
# Clone
git clone https://github.com/saaskit-dev/ai-usage.git
cd ai-usage

# Build
go build -o bin/ai-usage ./cmd/ai-usage

# Run
./bin/ai-usage --config config.yaml
```

## License

MIT License - see [LICENSE](LICENSE) for details.
