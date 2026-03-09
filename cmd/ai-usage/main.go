package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/api"
	"github.com/saaskit-dev/ai-usage/internal/config"
	"github.com/saaskit-dev/ai-usage/internal/daemon"
	"github.com/saaskit-dev/ai-usage/internal/monitor"
	"github.com/saaskit-dev/ai-usage/internal/notify"
	"github.com/saaskit-dev/ai-usage/internal/provider"
	claudeprovider "github.com/saaskit-dev/ai-usage/internal/provider/claude"
	copilotprovider "github.com/saaskit-dev/ai-usage/internal/provider/copilot"
	cursorprovider "github.com/saaskit-dev/ai-usage/internal/provider/cursor"
	"github.com/saaskit-dev/ai-usage/internal/watcher"
	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := newRootCmd(logger).Execute(); err != nil {
		logger.Error("command failed", "error", err)
		os.Exit(1)
	}
}

func newRootCmd(logger *slog.Logger) *cobra.Command {
	var addr string
	var interval time.Duration
	var configPath string
	var appriseURLs []string

	cmd := &cobra.Command{
		Use:   "ai-usage",
		Short: "AI usage monitoring daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Create context with signal handling
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg, err := config.Load(configPath)
			if err != nil {
				logger.Warn("config load failed, using defaults", "error", err)
				cfg = config.Default()
			} else {
				logger.Info("config loaded", "data_file", cfg.Monitor.DataFile)
			}

			if cmd.Flags().Changed("addr") {
				cfg.Server.Addr = addr
			}
			if cmd.Flags().Changed("interval") {
				cfg.Monitor.Interval = interval.String()
			}
			if len(appriseURLs) > 0 {
				cfg.Notify.AppriseURLs = appriseURLs
			}

			registry := provider.NewRegistry()

			// 注册 Claude providers（默认路径 + 配置的额外路径）
			if cfg.Providers.Claude.Enabled {
				// 默认路径 ~/.claude/
				registry.Register(claudeprovider.NewProvider())
				// 额外配置的路径
				for _, path := range cfg.Providers.Claude.Paths {
					registry.Register(claudeprovider.NewProvider(claudeprovider.WithCredentialsPath(path)))
				}
			}

			registry.Register(copilotprovider.NewProvider(copilotprovider.WithToken(cfg.Providers.Copilot.Token)))
			registry.Register(cursorprovider.NewProvider(cursorprovider.WithToken(cfg.Providers.Cursor.Token)))

			notifyMgr := notify.NewManager(logger)
			if len(cfg.Notify.AppriseURLs) > 0 {
				notifyMgr.AddNotifier(notify.NewAppriseNotifier("apprise", cfg.Notify.AppriseURLs))
			}

			probeInterval, _ := time.ParseDuration(cfg.Monitor.Interval)
			if probeInterval <= 0 {
				probeInterval = 60 * time.Second
			}

			mon := monitor.New(logger, registry, probeInterval)
			mon.SetNotifier(notifyMgr)
			mon.SetRules(cfg.Notify.Rules)
			if cfg.Monitor.DataFile != "" {
				mon.SetDataFile(cfg.Monitor.DataFile)
			}

			server := api.NewServer(logger, mon, notifyMgr, cfg.Server.Addr)

			var wg sync.WaitGroup
			errCh := make(chan error, 2)

			wg.Add(1)
			go func() {
				defer wg.Done()
				mon.Run(ctx)
			}()

			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := server.Run(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) {
					errCh <- err
				}
			}()

			// Shared reload logic for both SIGHUP and file watcher
			doReload := func() {
				if err := cfg.Reload(configPath); err != nil {
					logger.Warn("config reload failed", "error", err)
					return
				}
				logger.Info("config reloaded")

				// Reload notifiers
				notifyMgr.Reload(logger, cfg.Notify.AppriseURLs)

				// Reload monitor rules
				mon.SetRules(cfg.Notify.Rules)

				// Update data file
				if cfg.Monitor.DataFile != "" {
					mon.SetDataFile(cfg.Monitor.DataFile)
				}
			}

			// SIGHUP handler for config hot reload
			sighupCh := make(chan os.Signal, 1)
			signal.Notify(sighupCh, syscall.SIGHUP)
			defer signal.Stop(sighupCh)

			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-sighupCh:
						doReload()
					}
				}
			}()

			// File watcher for automatic config reload
			watchPath := configPath
			if watchPath == "" {
				watchPath = config.GetConfigPath()
			}
			if watchPath != "" {
				cw := watcher.New(logger, watchPath, doReload, 500*time.Millisecond)
				wg.Add(1)
				go func() {
					defer wg.Done()
					cw.Run(ctx)
				}()
			} else {
				logger.Warn("no config file found, file watcher disabled")
			}

			logger.Info("daemon started", "addr", cfg.Server.Addr, "interval", probeInterval)

			select {
			case <-ctx.Done():
			case err := <-errCh:
				stop()
				wg.Wait()
				return err
			}

			wg.Wait()
			return nil
		},
	}

	cmd.Flags().StringVarP(&addr, "addr", "a", ":8080", "api listen address")
	cmd.Flags().DurationVarP(&interval, "interval", "i", 60*time.Second, "provider probe interval")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "config file path")
	cmd.Flags().StringArrayVarP(&appriseURLs, "apprise", "n", nil, "apprise notification urls (can be repeated, e.g. schan://key, discord://id/token)")

	// 添加 daemon 子命令
	cmd.AddCommand(newDaemonCmd(logger))

	return cmd
}

// newDaemonCmd 创建 daemon 管理命令
func newDaemonCmd(logger *slog.Logger) *cobra.Command {
	var configPath string
	var binaryPath string

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage ai-usage daemon",
	}

	// install - 安装开机自启
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Install and enable auto-start (launchd/systemd)",
		RunE: func(cmd *cobra.Command, args []string) error {
			platform := daemon.Platform()
			logger.Info("Installing daemon", "platform", platform)

			installer := daemon.NewInstaller(binaryPath)
			if installer == nil {
				return fmt.Errorf("unsupported platform: %s", platform)
			}

			var cfg *config.Config
			if configPath != "" {
				var err error
				cfg, err = config.Load(configPath)
				if err != nil {
					return fmt.Errorf("load config: %w", err)
				}
			} else {
				cfg = config.Default()
			}

			if err := installer.Install(cfg); err != nil {
				return fmt.Errorf("install failed: %w", err)
			}

			logger.Info("Daemon installed successfully", "platform", platform)
			return nil
		},
	}
	installCmd.Flags().StringVarP(&configPath, "config", "c", "", "config file path")
	installCmd.Flags().StringVar(&binaryPath, "binary", "", "path to ai-usage binary (default: current binary)")

	// uninstall - 卸载开机自启
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall and disable auto-start",
		RunE: func(cmd *cobra.Command, args []string) error {
			platform := daemon.Platform()
			logger.Info("Uninstalling daemon", "platform", platform)

			installer := daemon.NewInstaller(binaryPath)
			if installer == nil {
				return fmt.Errorf("unsupported platform: %s", platform)
			}

			if err := installer.Uninstall(); err != nil {
				return fmt.Errorf("uninstall failed: %w", err)
			}

			logger.Info("Daemon uninstalled successfully")
			return nil
		},
	}

	// start - 启动守护进程
	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			installer := daemon.NewInstaller(binaryPath)
			if err := installer.Start(); err != nil {
				return fmt.Errorf("start failed: %w", err)
			}
			logger.Info("Daemon started")
			return nil
		},
	}

	// stop - 停止守护进程
	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			installer := daemon.NewInstaller(binaryPath)
			if err := installer.Stop(); err != nil {
				return fmt.Errorf("stop failed: %w", err)
			}
			logger.Info("Daemon stopped")
			return nil
		},
	}

	// restart - 重启守护进程
	restartCmd := &cobra.Command{
		Use:   "restart",
		Short: "Restart the daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			installer := daemon.NewInstaller(binaryPath)
			if err := installer.Stop(); err != nil {
				return fmt.Errorf("stop failed: %w", err)
			}
			time.Sleep(500 * time.Millisecond)
			if err := installer.Start(); err != nil {
				return fmt.Errorf("start failed: %w", err)
			}
			logger.Info("Daemon restarted")
			return nil
		},
	}

	// status - 查看守护进程状态
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			installer := daemon.NewInstaller(binaryPath)
			status, err := installer.Status()
			if err != nil {
				return fmt.Errorf("status failed: %w", err)
			}

			// 显示路径信息
			fmt.Println("Paths:")
			fmt.Printf("  Config: %s\n", config.GetConfigPath())
			fmt.Printf("  Log:    %s\n", daemon.GetLogPath())
			fmt.Printf("  Data:   %s\n", daemon.GetDataPath())
			fmt.Println()

			logger.Info("Daemon status", "status", status)
			return nil
		},
	}

	cmd.AddCommand(installCmd)
	cmd.AddCommand(uninstallCmd)
	cmd.AddCommand(startCmd)
	cmd.AddCommand(stopCmd)
	cmd.AddCommand(restartCmd)
	cmd.AddCommand(statusCmd)

	return cmd
}
