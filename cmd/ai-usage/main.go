package main

import (
	"context"
	"encoding/json"
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

			if cfg.Providers.Copilot.Enabled {
				registry.Register(copilotprovider.NewProvider(copilotprovider.WithToken(cfg.Providers.Copilot.Token)))
			}

			if cfg.Providers.Cursor.Enabled {
				registry.Register(cursorprovider.NewProvider(cursorprovider.WithToken(cfg.Providers.Cursor.Token)))
			}

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

	// 添加子命令
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newUsageCmd())
	cmd.AddCommand(newHealthCmd())
	cmd.AddCommand(newNotifyCmd())

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

// newUsageCmd 创建 usage 命令 - 直接获取用量数据
func newUsageCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Get current usage data",
		RunE: func(cmd *cobra.Command, args []string) error {
			// 尝试从 API 获取
			resp, err := http.Get("http://localhost:18000/usage")
			if err != nil {
				return fmt.Errorf("failed to connect to API: %w (is the service running?)", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				return fmt.Errorf("API returned status %d", resp.StatusCode)
			}

			var data struct {
				Usage       []provider.Usage `json:"usage"`
				LastUpdated time.Time        `json:"last_updated"`
				Ready       bool             `json:"ready"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(data)
			}

			// 格式化输出
			fmt.Printf("Last Updated: %s\n\n", data.LastUpdated.Format("2006-01-02 15:04:05"))

			for _, u := range data.Usage {
				label := u.Provider
				if u.Email != "" {
					label = fmt.Sprintf("%s (%s)", u.Provider, u.Email)
				} else if u.Path != "" {
					label = fmt.Sprintf("%s (%s)", u.Provider, u.Path)
				}

				if u.Error != "" {
					fmt.Printf("❌ %s\n", label)
					fmt.Printf("   Error: %s\n\n", u.Error)
					continue
				}

				fmt.Printf("✅ %s\n", label)
				if u.Tier != "" {
					fmt.Printf("   Plan: %s\n", u.Tier)
				}
				for _, q := range u.Quotas {
					icon := "✅"
					status := q.CalculateStatus()
					if status == provider.StatusWarning {
						icon = "⚠️"
					} else if status == provider.StatusCritical {
						icon = "🔴"
					} else if status == provider.StatusDepleted {
						icon = "🟠"
					}
					line := fmt.Sprintf("   %s %s: %.0f%%", icon, q.Type, q.PercentRemaining)
					if q.Used > 0 || q.Limit > 0 {
						line += fmt.Sprintf(" (%d/%d)", q.Used, q.Limit)
					}
					if q.ResetText != "" {
						line += fmt.Sprintf(" · %s", q.ResetText)
					}
					fmt.Println(line)
				}
				fmt.Println()
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")

	return cmd
}

// newHealthCmd 创建 health 命令 - 健康检查
func newHealthCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check service health",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get("http://localhost:18000/healthz")
			if err != nil {
				return fmt.Errorf("service not responding: %w", err)
			}
			defer resp.Body.Close()

			if asJSON {
				_, err = os.Stdout.ReadFrom(resp.Body)
				return err
			}

			var data struct {
				Status    string `json:"status"`
				Ready     bool   `json:"ready"`
				Providers map[string]struct {
					ConsecutiveFails int    `json:"consecutive_fails"`
					LastError       string `json:"last_error,omitempty"`
					LastSuccess     string `json:"last_success,omitempty"`
				} `json:"providers"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			statusIcon := "✅"
			if data.Status == "degraded" {
				statusIcon = "⚠️"
			} else if data.Status == "error" {
				statusIcon = "🔴"
			}

			fmt.Printf("%s Service Status: %s\n\n", statusIcon, data.Status)
			fmt.Printf("Ready: %v\n\n", data.Ready)

			fmt.Println("Providers:")
			for name, p := range data.Providers {
				if p.ConsecutiveFails > 0 {
					fmt.Printf("  ❌ %s: %d consecutive failures\n", name, p.ConsecutiveFails)
					if p.LastError != "" {
						fmt.Printf("     Error: %s\n", p.LastError)
					}
				} else {
					fmt.Printf("  ✅ %s: healthy\n", name)
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")

	return cmd
}

// newNotifyCmd 创建 notify 命令 - 通知管理
func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Notification commands",
	}

	// notify status 子命令
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show notification status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get("http://localhost:18000/notify")
			if err != nil {
				return fmt.Errorf("service not responding: %w", err)
			}
			defer resp.Body.Close()

			_, err = os.Stdout.ReadFrom(resp.Body)
			return err
		},
	})

	// notify test 子命令
	cmd.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Send test notifications based on current status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Post("http://localhost:18000/notify/test", "application/json", nil)
			if err != nil {
				return fmt.Errorf("service not responding: %w", err)
			}
			defer resp.Body.Close()

			var result struct {
				Sent   []string `json:"sent"`
				Errors []string `json:"errors"`
				Total  int      `json:"total"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			if len(result.Sent) > 0 {
				fmt.Println("Notifications sent:")
				for _, s := range result.Sent {
					fmt.Printf("  ✅ %s\n", s)
				}
			}

			if len(result.Errors) > 0 {
				fmt.Println("\nErrors:")
				for _, e := range result.Errors {
					fmt.Printf("  ❌ %s\n", e)
				}
			}

			fmt.Printf("\nTotal: %d notifications\n", result.Total)

			return nil
		},
	})

	// notify send 子命令 - 发送自定义通知
	var title, body string
	sendCmd := &cobra.Command{
		Use:   "send",
		Short: "Send a custom notification",
		RunE: func(cmd *cobra.Command, args []string) error {
			if title == "" {
				return fmt.Errorf("title is required (use -t)")
			}

			payload := map[string]string{"title": title, "body": body}
			data, _ := json.Marshal(payload)

			resp, err := http.Post("http://localhost:18000/notify", "application/json", strings.NewReader(string(data)))
			if err != nil {
				return fmt.Errorf("service not responding: %w", err)
			}
			defer resp.Body.Close()

			var result struct {
				Status string `json:"status"`
				Error  string `json:"error,omitempty"`
			}

			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return fmt.Errorf("failed to decode response: %w", err)
			}

			if result.Status == "sent" {
				fmt.Println("✅ Notification sent")
				return nil
			}

			return fmt.Errorf("failed to send: %s", result.Error)
		},
	}
	sendCmd.Flags().StringVarP(&title, "title", "t", "", "notification title (required)")
	sendCmd.Flags().StringVarP(&body, "body", "b", "", "notification body")

	cmd.AddCommand(sendCmd)

	return cmd
}
