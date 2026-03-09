<div align="center">

# AI Usage Monitor

Monitor AI service usage (Claude, Copilot, Cursor) with real-time metrics and notifications.

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21+-00ADD8.svg)](https://github.com/saaskit-dev/ai-usage)

</div>

## Features

- **Multi-Provider**: Claude, Copilot, Cursor
- **Real-time Monitoring**: Track quota usage automatically
- **REST API**: Built-in API server on port 18000
- **Notifications**: Apprise integration (Discord, Telegram, ServerChan, etc.)
- **Auto-Start**: Managed by Homebrew Services

## Installation

```bash
brew tap saaskit-dev/tap
brew install ai-usage
```

安装后自动启动服务。

## Commands

```bash
# 查看状态
ai-usage status

# 服务管理
brew services start ai-usage
brew services stop ai-usage
brew services restart ai-usage

# API 调用
curl http://localhost:18000/usage
```

## Configuration

配置文件: `/opt/homebrew/etc/ai-usage.yaml`

默认配置（无需配置文件即可运行）：
- 端口: `18000`
- 间隔: `5min`
- Provider: 只启用 Claude

完整配置示例：

```yaml
server:
  addr: ":18000"

monitor:
  interval: "300s"

notify:
  apprise_urls:
    - "schan://your-sendkey"
  rules:
    - event: depleted       # 配额耗尽
    - event: probe_error    # 探测失败

providers:
  claude:
    enabled: true
    # paths:                # 额外的 Claude 凭证路径
    #   - ~/.claude-work/
  copilot:
    enabled: false
    token: "ghp_xxx"
  cursor:
    enabled: false
    token: "xxx"
```

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /healthz` | 健康检查 |
| `GET /usage` | 所有 Provider 用量 |
| `GET /config` | 当前配置 |
| `GET /notify` | 通知状态 |

```bash
curl http://localhost:18000/healthz
curl http://localhost:18000/usage
```

响应示例：

```json
{
  "usage": [
    {
      "provider": "claude",
      "email": "user@example.com",
      "quotas": [
        {
          "type": "daily",
          "percent_remaining": 75,
          "used": 25,
          "limit": 100,
          "reset_text": "Resets in 6h"
        }
      ]
    }
  ],
  "last_updated": "2024-01-15T10:00:00Z",
  "ready": true
}
```

## Notifications

支持的通知渠道（通过 [Apprise](https://github.com/caronc/apprise)）：

```yaml
notify:
  apprise_urls:
    # ServerChan (微信)
    - "schan://SCTxxxxx"

    # Discord
    - "discord://webhook_id/webhook_token"

    # Telegram
    - "tgram://bot_token/chat_id"

    # 自定义 Webhook
    - "jsons://webhook.example.com/notify"
```

通知规则：

```yaml
rules:
  - event: depleted           # 配额耗尽
  - event: probe_error        # 探测失败
  - event: threshold          # 低于阈值
    threshold: 50
  - event: reset_soon         # 即将重置
    before: "10m"
```

通知示例：

```
⚠️ Claude 低用量告警 (20%)
✅ daily: 20% (40/50) · Resets in 2h
> 配额不足，请注意使用

_03-10 14:30_
```

## Paths

| 文件 | 路径 |
|------|------|
| 配置 | `/opt/homebrew/etc/ai-usage.yaml` |
| 日志 | `/opt/homebrew/var/log/ai-usage.log` |
| 数据 | `/opt/homebrew/var/ai-usage/usage.json` |

## License

MIT License
