package daemon

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/kilingzhang/ai-usage/internal/config"
)

const (
	// SystemdUnitName 是 systemd 服务名称
	SystemdUnitName = "ai-usage"
)

// SystemdInstaller Linux systemd 安装器
type SystemdInstaller struct {
	binaryPath string
	unitPath   string
	// 可配置项
	Description      string
	WorkingDirectory string
	RestartSec       int
	StandardOutput   string
	StandardError    string
	User             string
	Group            string
}

func NewSystemdInstaller(binaryPath string) *SystemdInstaller {
	if binaryPath == "" {
		binaryPath = GetBinaryPath()
	}

	home, _ := os.UserHomeDir()

	return &SystemdInstaller{
		binaryPath:       binaryPath,
		Description:      "AI Usage Monitoring Daemon",
		WorkingDirectory: home,
		RestartSec:       5,
		StandardOutput:   "append:" + filepath.Join(home, ".local", "share", "ai-usage", "ai-usage.log"),
		StandardError:    "append:" + filepath.Join(home, ".local", "share", "ai-usage", "ai-usage.error.log"),
		User:             getCurrentUser(),
		Group:            getCurrentGroup(),
	}
}

func getCurrentUser() string {
	user := os.Getenv("USER")
	if user == "" {
		return "root"
	}
	return user
}

func getCurrentGroup() string {
	group := os.Getenv("GROUP")
	if group == "" {
		return "root"
	}
	return group
}

// systemdUnitTemplate systemd unit 文件模板
var systemdUnitTemplate = template.Must(template.New("systemd").Parse(`[Unit]
Description={{.Description}}
After=network.target

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
WorkingDirectory={{.WorkingDirectory}}
ExecStart={{.BinaryPath}} --config {{.ConfigPath}}
Restart=on-failure
RestartSec={{.RestartSec}}
StandardOutput={{.StandardOutput}}
StandardError={{.StandardError}}

# 安全加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths={{.ReadWritePaths}}

[Install]
WantedBy=multi-user.target
`))

func (i *SystemdInstaller) getUnitContent() (string, error) {
	configPath := filepath.Join(GetConfigDir(), "config.yaml")

	// 确保目录存在
	if err := EnsureConfigDir(); err != nil {
		return "", err
	}

	// 准备环境变量
	env := []string{}
	for _, e := range os.Environ() {
		// 过滤掉可能会干扰的变量
		if !strings.HasPrefix(e, "SSH_") &&
			!strings.HasPrefix(e, "TMUX") &&
			!strings.HasPrefix(e, "TERM") {
			env = append(env, e)
		}
	}

	home, _ := os.UserHomeDir()

	var buf bytes.Buffer
	err := systemdUnitTemplate.Execute(&buf, map[string]interface{}{
		"Description":      i.Description,
		"BinaryPath":       i.binaryPath,
		"ConfigPath":       configPath,
		"WorkingDirectory": i.WorkingDirectory,
		"RestartSec":       i.RestartSec,
		"StandardOutput":   i.StandardOutput,
		"StandardError":    i.StandardError,
		"User":             i.User,
		"Group":            i.Group,
		"Environment":      env,
		"ReadWritePaths":   filepath.Join(home, ".local", "share", "ai-usage"),
	})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// unitPath 获取 systemd unit 文件路径
func (i *SystemdInstaller) unitPathFunc() string {
	return filepath.Join("/etc/systemd/system", SystemdUnitName+".service")
}

// userUnitPath 获取用户级 systemd unit 文件路径
func (i *SystemdInstaller) userUnitPathFunc() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", SystemdUnitName+".service")
}

// Install 安装 systemd 服务
func (i *SystemdInstaller) Install(cfg *config.Config) error {
	// 检查是否是 root 用户
	isRoot := os.Geteuid() == 0

	var unitPath string

	if isRoot {
		// 系统级安装
		unitPath = i.unitPathFunc()
		// 确保目录存在
		if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
			return fmt.Errorf("create systemd directory: %w", err)
		}
	} else {
		// 用户级安装
		unitPath = i.userUnitPathFunc()
		// 确保目录存在
		unitDir := filepath.Dir(unitPath)
		if err := os.MkdirAll(unitDir, 0755); err != nil {
			return fmt.Errorf("create systemd user directory: %w", err)
		}
	}

	// 生成 unit 内容
	content, err := i.getUnitContent()
	if err != nil {
		return fmt.Errorf("generate unit: %w", err)
	}

	// 写入 unit 文件
	if err := os.WriteFile(unitPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write unit file: %w", err)
	}

	// 重新加载 systemd（需要 root）
	if isRoot {
		_, _ = RunCommand("systemctl", "daemon-reload")
	}

	// 启用并启动服务
	if isRoot {
		_, _ = RunCommand("systemctl", "enable", SystemdUnitName)
		_, _ = RunCommand("systemctl", "start", SystemdUnitName)
	} else {
		_, _ = RunCommand("systemctl", "--user", "enable", SystemdUnitName)
		_, _ = RunCommand("systemctl", "--user", "start", SystemdUnitName)
	}

	i.unitPath = unitPath
	return nil
}

// Uninstall 卸载 systemd 服务
func (i *SystemdInstaller) Uninstall() error {
	isRoot := os.Geteuid() == 0

	// 停止服务
	_ = i.Stop()

	// 删除 unit 文件
	unitPath := i.unitPath
	if unitPath == "" {
		if isRoot {
			unitPath = i.unitPathFunc()
		} else {
			unitPath = i.userUnitPathFunc()
		}
	}

	if _, err := os.Stat(unitPath); err == nil {
		if err := os.Remove(unitPath); err != nil {
			return fmt.Errorf("remove unit file: %w", err)
		}
	}

	// 重新加载 systemd（需要 root）
	if isRoot {
		_, _ = RunCommand("systemctl", "daemon-reload")
	}

	return nil
}

// Start 启动服务
func (i *SystemdInstaller) Start() error {
	isRoot := os.Geteuid() == 0

	var err error
	if isRoot {
		_, err = RunCommand("systemctl", "start", SystemdUnitName)
	} else {
		_, err = RunCommand("systemctl", "--user", "start", SystemdUnitName)
	}
	return err
}

// Stop 停止服务
func (i *SystemdInstaller) Stop() error {
	isRoot := os.Geteuid() == 0

	var err error
	if isRoot {
		_, err = RunCommand("systemctl", "stop", SystemdUnitName)
	} else {
		_, err = RunCommand("systemctl", "--user", "stop", SystemdUnitName)
	}
	return err
}

// Status 获取服务状态
func (i *SystemdInstaller) Status() (string, error) {
	isRoot := os.Geteuid() == 0

	var output string
	var err error
	if isRoot {
		output, err = RunCommand("systemctl", "status", SystemdUnitName)
	} else {
		output, err = RunCommand("systemctl", "--user", "status", SystemdUnitName)
	}

	if err != nil {
		return "Stopped", nil
	}
	return output, nil
}

// IsInstalled 检查是否已安装
func (i *SystemdInstaller) IsInstalled() bool {
	unitPath := i.unitPath
	if unitPath == "" {
		if os.Geteuid() == 0 {
			unitPath = i.unitPathFunc()
		} else {
			unitPath = i.userUnitPathFunc()
		}
	}
	_, err := os.Stat(unitPath)
	return err == nil
}

// Enable 启用服务（开机自启）
func (i *SystemdInstaller) Enable() error {
	isRoot := os.Geteuid() == 0

	var err error
	if isRoot {
		_, err = RunCommand("systemctl", "enable", SystemdUnitName)
	} else {
		_, err = RunCommand("systemctl", "--user", "enable", SystemdUnitName)
	}
	return err
}

// Disable 禁用服务
func (i *SystemdInstaller) Disable() error {
	isRoot := os.Geteuid() == 0

	var err error
	if isRoot {
		_, err = RunCommand("systemctl", "disable", SystemdUnitName)
	} else {
		_, err = RunCommand("systemctl", "--user", "disable", SystemdUnitName)
	}
	return err
}
