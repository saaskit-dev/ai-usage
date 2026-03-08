# ai-usage

Homebrew tap for ai-usage - AI usage monitoring daemon

## 安装

```bash
# 1. 添加 tap
brew tap saaskit-dev/ai-usage

# 2. 安装
brew install ai-usage
```

## 功能

- 监控 Claude、Copilot、Cursor 使用情况
- API 服务提供实时数据
- 支持 Apprise 通知
- 跨平台支持 (macOS / Linux)
- 开机自启 (launchd / systemd)

## 快速开始

```bash
# 复制配置
cp $(brew --prefix)/etc/ai-usage.example.yaml ~/.config/ai-usage/config.yaml

# 编辑配置
vim ~/.config/ai-usage/config.yaml

# 启动服务
ai-usage

# 开启开机自启
ai-usage daemon install

# 查看状态
ai-usage daemon status
```

## 开发

```bash
# 本地测试
brew install ./homebrew/ai-usage.rb
```
