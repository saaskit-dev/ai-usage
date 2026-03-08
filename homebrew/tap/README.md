# homebrew-tap

Homebrew tap for [saaskit-dev](https://github.com/saaskit-dev) projects.

## 安装

```bash
# 添加 tap
brew tap saaskit-dev/tap

# 安装项目
brew install ai-usage
```

## 可用项目

| Formula | 描述 |
|---------|------|
| [ai-usage](Formula/ai-usage.rb) | AI 使用监控 Daemon (Claude, Copilot, Cursor) |

## 添加新 Formula

1. Fork 此仓库
2. 在 `Formula/` 目录添加新的 `.rb` 文件
3. 提交 PR

## 本地测试

```bash
brew install ./Formula/ai-usage.rb
```
