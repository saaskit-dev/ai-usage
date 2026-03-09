package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/config"
)

const (
	// LaunchdLabel 是 macOS 的服务标签
	LaunchdLabel = "com.ai-usage.daemon"
)

// LaunchdInstaller macOS launchd 安装器
type LaunchdInstaller struct {
	binaryPath string
	plistPath  string
	// 可配置项
	RunAtLoad       bool
	KeepAlive       bool
	StandardOutPath string
	StandardErrPath string
}

func NewLaunchdInstaller(binaryPath string) *LaunchdInstaller {
	if binaryPath == "" {
		binaryPath = GetBinaryPath()
	}

	return &LaunchdInstaller{
		binaryPath:      binaryPath,
		RunAtLoad:       true,
		KeepAlive:       true,
		StandardOutPath: "/tmp/ai-usage.stdout.log",
		StandardErrPath: "/tmp/ai-usage.stderr.log",
	}
}

// plistTemplate launchd plist 模板
var launchdPlistTemplate = template.Must(template.New("launchd").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>--config</string>
        <string>{{.ConfigPath}}</string>
    </array>
    <key>RunAtLoad</key>
    <{{.RunAtLoad}}/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>StandardOutPath</key>
    <string>{{.StandardOutPath}}</string>
    <key>StandardErrPath</key>
    <string>{{.StandardErrPath}}</string>
    <key>ProcessType</key>
    <string>Background</string>
    <key>ThrottleInterval</key>
    <integer>10</integer>
    <key>ExitTimeOut</key>
    <integer>30</integer>
    <key>HangTimeout</key>
    <integer>180</integer>
</dict>
</plist>
`))

func (i *LaunchdInstaller) getPlistContent() (string, error) {
	configPath := filepath.Join(GetConfigDir(), "config.yaml")

	// 确保目录存在
	if err := EnsureConfigDir(); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err := launchdPlistTemplate.Execute(&buf, map[string]interface{}{
		"Label":           LaunchdLabel,
		"BinaryPath":      i.binaryPath,
		"ConfigPath":      configPath,
		"RunAtLoad":       boolToXML(i.RunAtLoad),
		"StandardOutPath": i.StandardOutPath,
		"StandardErrPath": i.StandardErrPath,
	})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func boolToXML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// plistPath 获取 plist 路径
func (i *LaunchdInstaller) plistPathFunc() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

// Install 安装 launchd 服务
func (i *LaunchdInstaller) Install(cfg *config.Config) error {
	// 确保目录存在
	agentsDir := filepath.Dir(i.plistPathFunc())
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}

	// 生成 plist 内容
	content, err := i.getPlistContent()
	if err != nil {
		return fmt.Errorf("generate plist: %w", err)
	}

	// 写入 plist 文件
	if err := os.WriteFile(i.plistPathFunc(), []byte(content), 0644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// 加载服务
	if err := i.load(); err != nil {
		return fmt.Errorf("load service: %w", err)
	}

	return nil
}

// Uninstall 卸载 launchd 服务
func (i *LaunchdInstaller) Uninstall() error {
	// 先停止服务
	_ = i.Stop()

	// 删除 plist 文件
	if _, err := os.Stat(i.plistPathFunc()); err == nil {
		if err := os.Remove(i.plistPathFunc()); err != nil {
			return fmt.Errorf("remove plist: %w", err)
		}
	}

	return nil
}

// load 加载服务
func (i *LaunchdInstaller) load() error {
	_, err := RunCommand("launchctl", "load", i.plistPathFunc())
	if err != nil {
		// 可能已经加载了，尝试重启
		_ = i.Start()
		return nil
	}
	return nil
}

// unload 卸载服务
func (i *LaunchdInstaller) unload() error {
	_, err := RunCommand("launchctl", "unload", i.plistPathFunc())
	return err
}

// Start 启动服务
func (i *LaunchdInstaller) Start() error {
	_, err := RunCommand("launchctl", "start", LaunchdLabel)
	if err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	// 等待服务启动
	time.Sleep(500 * time.Millisecond)
	return nil
}

// Stop 停止服务
func (i *LaunchdInstaller) Stop() error {
	_, err := RunCommand("launchctl", "stop", LaunchdLabel)
	if err != nil {
		// 可能已经停止了
		return nil
	}
	// 等待服务停止
	time.Sleep(500 * time.Millisecond)
	return nil
}

// Status 获取服务状态
func (i *LaunchdInstaller) Status() (string, error) {
	output, err := RunCommand("launchctl", "list", LaunchdLabel)
	if err != nil {
		return "Stopped", nil
	}

	// 解析输出，查找 PID
	// 格式: "PID" = 12345;
	if strings.Contains(output, `"PID" =`) {
		// 提取 PID
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(line, `"PID" =`) {
				// 格式: \t"PID" = 23642;
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, `"PID" = `)
				line = strings.TrimSuffix(line, ";")
				pid := strings.TrimSpace(line)
				if pid != "" && pid != "0" {
					return "Running (PID: " + pid + ")", nil
				}
			}
		}
		return "Running", nil
	}

	return "Stopped", nil
}

// IsInstalled 检查是否已安装
func (i *LaunchdInstaller) IsInstalled() bool {
	_, err := os.Stat(i.plistPathFunc())
	return err == nil
}
