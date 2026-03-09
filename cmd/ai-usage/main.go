package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/api"
	"github.com/saaskit-dev/ai-usage/internal/config"
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
				logger.Error("config load failed", "error", err)
				return err
			}
			logger.Info("config loaded", "path", configPath)

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
				probeInterval = 300 * time.Second
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

	cmd.Flags().StringVarP(&addr, "addr", "a", ":18000", "api listen address")
	cmd.Flags().DurationVarP(&interval, "interval", "i", 300*time.Second, "provider probe interval")
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "config file path")
	cmd.Flags().StringArrayVarP(&appriseURLs, "apprise", "n", nil, "apprise notification urls (can be repeated, e.g. schan://key, discord://id/token)")

	// 添加 status 子命令
	cmd.AddCommand(newStatusCmd())

	return cmd
}

// newStatusCmd 创建 status 命令
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		Run: func(cmd *cobra.Command, args []string) {
			// 显示路径信息
			fmt.Println("Paths:")
			fmt.Printf("  Config: %s\n", config.GetConfigPath())
			fmt.Printf("  Log:    %s\n", config.GetLogPath())
			fmt.Printf("  Data:   %s\n", config.GetDataPath())
			fmt.Println()

			// 检查 brew services 状态
			out, err := exec.Command("brew", "services", "list").Output()
			if err != nil {
				fmt.Println("Service: Unable to check (brew not available)")
			} else {
				// 格式: Name       Status  User  File
				//       ai-usage   started dev   ~/Library/LaunchAgents/...
				lines := strings.Split(string(out), "\n")
				for _, line := range lines {
					if strings.HasPrefix(line, "ai-usage") {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							status := fields[1]
							switch status {
							case "started":
								fmt.Println("Service: Running")
							case "stopped", "none":
								fmt.Println("Service: Stopped")
							case "error":
								fmt.Println("Service: Error")
							default:
								fmt.Println("Service:", status)
							}
						}
						break
					}
				}
			}
			fmt.Println()

			// 显示 API 端点
			fmt.Println("API Endpoints (default port 18000):")
			fmt.Println("  curl http://localhost:18000/healthz  # Health check")
			fmt.Println("  curl http://localhost:18000/usage    # Usage data")
			fmt.Println("  curl http://localhost:18000/config   # Current config")
			fmt.Println("  curl http://localhost:18000/notify   # Notification status")
		},
	}
}
