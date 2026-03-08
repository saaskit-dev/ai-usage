package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/kilingzhang/ai-usage/internal/config"
)

// Platform 返回当前操作系统
func Platform() string {
	return runtime.GOOS
}

// Installer 定义系统安装器接口
type Installer interface {
	Install(cfg *config.Config) error
	Uninstall() error
	Start() error
	Stop() error
	Status() (string, error)
	IsInstalled() bool
}

// NewInstaller 根据操作系统创建对应的安装器
func NewInstaller(binaryPath string) Installer {
	switch Platform() {
	case "darwin":
		return NewLaunchdInstaller(binaryPath)
	case "linux":
		return NewSystemdInstaller(binaryPath)
	default:
		return nil
	}
}

// Daemonize 使当前进程守护化
// 注意：实际使用中，建议使用 launchd (macOS) 或 systemd (Linux) 来管理守护进程
// 这个函数提供一个简单的后台运行机制
func Daemonize() error {
	if Platform() == "windows" {
		return fmt.Errorf("daemonize not supported on windows")
	}

	// 检查是否已经是守护进程
	if os.Getppid() == 1 {
		return nil // 已经是守护进程
	}

	// 创建一个管道用于父子进程通信
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}

	// fork
	attr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	process, err := os.StartProcess(os.Args[0], os.Args, attr)
	if err != nil {
		return err
	}

	// 父进程等待子进程
	status, err := process.Wait()
	if err != nil {
		return err
	}

	w.Close()
	r.Close()

	if !status.Exited() {
		return nil
	}

	// 子进程已退出，父进程也退出
	os.Exit(status.ExitCode())
	return nil
}

// WaitForSignal 等待系统信号
func WaitForSignal(ctx context.Context) {
	<-ctx.Done()
}

// ReloadConfig 重新加载配置（通过发送 SIGHUP）
func ReloadConfig(pid int) error {
	return syscall.Kill(pid, syscall.SIGHUP)
}

// GetPIDFilePath 获取 PID 文件路径
func GetPIDFilePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ai-usage.pid")
}

// WritePID 写入 PID 文件
func WritePID(pid int) error {
	return os.WriteFile(GetPIDFilePath(), []byte(fmt.Sprintf("%d", pid)), 0644)
}

// ReadPID 读取 PID 文件
func ReadPID() (int, error) {
	data, err := os.ReadFile(GetPIDFilePath())
	if err != nil {
		return 0, err
	}
	var pid int
	_, err = fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid)
	return pid, err
}

// RemovePID 删除 PID 文件
func RemovePID() error {
	return os.Remove(GetPIDFilePath())
}

// IsRunning 检查进程是否在运行
func IsRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// WaitForRunning 等待进程启动
func WaitForRunning(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if IsRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for process to start")
}

// WaitForStop 等待进程停止
func WaitForStop(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !IsRunning(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for process to stop")
}

// GetBinaryPath 获取当前二进制文件路径
func GetBinaryPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return ""
	}
	return execPath
}

// GetConfigDir 获取配置目录
func GetConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ai-usage")
}

// EnsureConfigDir 确保配置目录存在
func EnsureConfigDir() error {
	dir := GetConfigDir()
	return os.MkdirAll(dir, 0755)
}

// GetLogPath 获取日志路径
func GetLogPath() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs", "ai-usage.log")
	}
	return filepath.Join(home, ".local", "share", "ai-usage", "ai-usage.log")
}

// EnsureLogDir 确保日志目录存在
func EnsureLogDir() error {
	logPath := GetLogPath()
	dir := filepath.Dir(logPath)
	return os.MkdirAll(dir, 0755)
}

// Restart 重启守护进程
func Restart(pid int) error {
	if !IsRunning(pid) {
		return fmt.Errorf("daemon not running")
	}
	// 发送 SIGTERM 让进程优雅退出
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	// 等待进程停止
	if err := WaitForStop(pid, 10*time.Second); err != nil {
		return err
	}
	// 重新启动（需要外部重新启动）
	return nil
}

// Stop 停止守护进程
func Stop(pid int) error {
	if !IsRunning(pid) {
		return nil // 已经停止了
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	return WaitForStop(pid, 10*time.Second)
}

// RunCommand 执行系统命令
func RunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", string(output), err)
	}
	return string(output), nil
}
