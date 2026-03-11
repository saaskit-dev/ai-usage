package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/provider"
)

type EventType string

const (
	EventStatusChange EventType = "status_change"
	EventDepleted     EventType = "depleted"
	EventCritical     EventType = "critical"
	EventWarning      EventType = "warning"
	EventThreshold    EventType = "threshold"
	EventProbeError   EventType = "probe_error"
	EventResetSoon    EventType = "reset_soon"
	EventManual       EventType = "manual"
)

type Event struct {
	Type      EventType            `json:"type"`
	Provider  string               `json:"provider"`
	OldStatus provider.QuotaStatus `json:"old_status,omitempty"`
	NewStatus provider.QuotaStatus `json:"new_status,omitempty"`
	Usage     provider.Usage       `json:"usage"`
	Timestamp time.Time            `json:"timestamp"`
	Message   string               `json:"message,omitempty"`
}

// providerLabel returns a human-readable provider identifier from the event's Usage.
func (e Event) providerLabel() string {
	name := e.Usage.Provider
	if len(name) > 0 {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	if e.Usage.Email != "" {
		return fmt.Sprintf("%s (%s)", name, e.Usage.Email)
	}
	if e.Usage.Path != "" {
		return fmt.Sprintf("%s (%s)", name, e.Usage.Path)
	}
	return name
}

// quotasSummary builds a compact summary of all quotas (Markdown format).
func (e Event) quotasSummary() string {
	if len(e.Usage.Quotas) == 0 {
		return "无配额数据"
	}
	var lines []string
	for _, q := range e.Usage.Quotas {
		icon := "✅"
		switch q.CalculateStatus() {
		case provider.StatusWarning:
			icon = "⚠️"
		case provider.StatusCritical:
			icon = "🔴"
		case provider.StatusDepleted:
			icon = "🟠"
		}
		line := fmt.Sprintf("%s **%s**: %.0f%%", icon, q.Type, q.PercentRemaining)
		if q.Used > 0 || q.Limit > 0 {
			line += fmt.Sprintf(" (%d/%d)", q.Used, q.Limit)
		}
		if q.ResetText != "" {
			line += fmt.Sprintf(" · %s", q.ResetText)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n\n")
}

// accountInfo returns account information string for the event.
func (e Event) accountInfo() string {
	var parts []string
	if e.Usage.Email != "" {
		parts = append(parts, fmt.Sprintf("📧 账户: %s", e.Usage.Email))
	}
	if e.Usage.Tier != "" {
		parts = append(parts, fmt.Sprintf("🏷 计划: %s", e.Usage.Tier))
	}
	if e.Usage.Path != "" {
		parts = append(parts, fmt.Sprintf("📁 路径: %s", e.Usage.Path))
	}
	return strings.Join(parts, "\n\n")
}

// FormatMessage returns a plain text notification message.
func (e Event) FormatMessage() (title, body string) {
	label := e.providerLabel()
	timeStr := e.Timestamp.Format("01-02 15:04")
	accountInfo := e.accountInfo()

	switch e.Type {
	case EventThreshold:
		title = fmt.Sprintf("⚠️ %s 低用量告警 (%.0f%%)", label, e.Usage.LowestPercent())
		var sb strings.Builder
		sb.WriteString("## 当前配额状态\n\n")
		sb.WriteString(e.quotasSummary())
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		if e.Message != "" {
			sb.WriteString(fmt.Sprintf("\n\n> 💡 %s", e.Message))
		}
		sb.WriteString(fmt.Sprintf("\n\n⏰ %s", timeStr))
		body = sb.String()

	case EventDepleted:
		title = fmt.Sprintf("🔴 %s 配额耗尽", label)
		var sb strings.Builder
		sb.WriteString("## 🚨 配额已用完\n\n请等待重置或升级计划\n\n")
		sb.WriteString(e.quotasSummary())
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		sb.WriteString(fmt.Sprintf("\n\n⏰ %s", timeStr))
		body = sb.String()

	case EventProbeError:
		title = fmt.Sprintf("❌ %s 探测失败", label)
		var sb strings.Builder
		sb.WriteString("## 错误详情\n\n")
		sb.WriteString(fmt.Sprintf("```\n%s\n```", e.Usage.Error))
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		// 添加当前配额状态（如果有）
		if len(e.Usage.Quotas) > 0 {
			sb.WriteString("\n\n## 当前配额状态\n\n")
			sb.WriteString(e.quotasSummary())
		}
		sb.WriteString("\n\n💡 可能原因：Token 过期、网络问题或 API 限流")
		sb.WriteString(fmt.Sprintf("\n\n⏰ %s", timeStr))
		body = sb.String()
	case EventResetSoon:
		title = fmt.Sprintf("🔄 %s 即将重置", label)
		var sb strings.Builder
		sb.WriteString("## 配额即将自动重置\n\n")
		sb.WriteString(e.quotasSummary())
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		if e.Message != "" {
			sb.WriteString(fmt.Sprintf("\n\n⏰ %s", e.Message))
		}
		sb.WriteString(fmt.Sprintf("\n\n检测时间: %s", timeStr))
		body = sb.String()

	case EventStatusChange, EventCritical, EventWarning:
		statusIcon := "ℹ️"
		statusText := "状态变更"
		if e.NewStatus == provider.StatusCritical {
			statusIcon = "🔴"
			statusText = "配额严重不足"
		} else if e.NewStatus == provider.StatusWarning {
			statusIcon = "⚠️"
			statusText = "配额偏低"
		} else if e.NewStatus == provider.StatusHealthy {
			statusIcon = "✅"
			statusText = "配额正常"
		}
		title = fmt.Sprintf("%s %s：%s → %s", statusIcon, label, e.OldStatus, e.NewStatus)
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("## %s\n\n", statusText))
		sb.WriteString(e.quotasSummary())
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		sb.WriteString(fmt.Sprintf("\n\n⏰ %s", timeStr))
		body = sb.String()

	case EventManual:
		parts := strings.SplitN(e.Message, "\n", 2)
		title = parts[0]
		if len(parts) > 1 {
			body = parts[1]
		} else {
			body = e.Message
		}
	default:
		title = fmt.Sprintf("🔔 %s %s", label, e.Type)
		var sb strings.Builder
		sb.WriteString(e.quotasSummary())
		if accountInfo != "" {
			sb.WriteString("\n\n")
			sb.WriteString(accountInfo)
		}
		sb.WriteString(fmt.Sprintf("\n\n⏰ %s", timeStr))
		body = sb.String()
	}
	return
}

type Notifier interface {
	Name() string
	Send(ctx context.Context, event Event) error
}

type Manager struct {
	notifiers []Notifier
	logger    *slog.Logger
}

func NewManager(logger *slog.Logger) *Manager {
	return &Manager{logger: logger}
}

func (m *Manager) AddNotifier(n Notifier) {
	m.notifiers = append(m.notifiers, n)
}

func (m *Manager) HasNotifiers() bool {
	return len(m.notifiers) > 0
}

// Reload replaces all notifiers with new ones based on the provided URLs
func (m *Manager) Reload(logger *slog.Logger, urls []string) {
	m.logger = logger
	m.notifiers = nil
	if len(urls) > 0 {
		m.notifiers = append(m.notifiers, NewAppriseNotifier("apprise", urls))
	}
}

func (m *Manager) Notify(ctx context.Context, event Event) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(m.notifiers))

	for _, n := range m.notifiers {
		wg.Add(1)
		go func(n Notifier) {
			defer wg.Done()
			if err := m.sendWithRetry(ctx, n, event); err != nil {
				errCh <- err
			}
		}(n)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

const maxRetries = 3

// sendWithRetry attempts to send with exponential backoff (1s, 2s, 4s).
func (m *Manager) sendWithRetry(ctx context.Context, n Notifier, event Event) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			m.logger.Warn("retrying notification",
				"notifier", n.Name(),
				"attempt", attempt+1,
				"backoff", backoff,
				"error", lastErr,
			)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return fmt.Errorf("%s: %w (last send error: %v)", n.Name(), ctx.Err(), lastErr)
			}
		}

		if err := n.Send(ctx, event); err != nil {
			lastErr = err
			continue
		}
		return nil
	}

	m.logger.Error("notify failed after retries",
		"notifier", n.Name(),
		"attempts", maxRetries+1,
		"error", lastErr,
	)
	return fmt.Errorf("%s: all %d attempts failed: %w", n.Name(), maxRetries+1, lastErr)
}

// AppriseNotifier sends notifications via Apprise CLI or direct HTTP.
type AppriseNotifier struct {
	name    string
	urls    []string
	cliPath string
	client  *http.Client
	useCLI  bool
}

func NewAppriseNotifier(name string, urls []string, opts ...func(*AppriseNotifier)) *AppriseNotifier {
	n := &AppriseNotifier{
		name:    name,
		urls:    urls,
		cliPath: "apprise",
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(n)
	}
	if _, err := exec.LookPath(n.cliPath); err == nil {
		n.useCLI = true
	}
	return n
}

func WithCLIPath(path string) func(*AppriseNotifier) {
	return func(n *AppriseNotifier) { n.cliPath = path }
}

func (n *AppriseNotifier) Name() string { return n.name }

func (n *AppriseNotifier) Send(ctx context.Context, event Event) error {
	title, body := event.FormatMessage()

	if n.useCLI {
		return n.sendCLI(ctx, title, body)
	}
	return n.sendHTTP(ctx, title, body)
}

func (n *AppriseNotifier) sendCLI(ctx context.Context, title, body string) error {
	args := []string{"-t", title, "-b", body}
	args = append(args, n.urls...)

	cmd := exec.CommandContext(ctx, n.cliPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apprise cli: %w, output: %s", err, string(output))
	}
	return nil
}

func (n *AppriseNotifier) sendHTTP(ctx context.Context, title, body string) error {
	for _, url := range n.urls {
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			if err := n.sendHTTPDirect(ctx, url, title, body); err != nil {
				return err
			}
			continue
		}

		providerURL := convertAppriseURL(url)
		if providerURL != "" {
			if err := n.sendHTTPDirect(ctx, providerURL, title, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (n *AppriseNotifier) sendHTTPDirect(ctx context.Context, url, title, body string) error {
	// Server酱 使用 "desp" 字段，其他服务使用 "body"
	payload := map[string]string{"title": title}
	if strings.Contains(url, "sctapi.ftqq.com") {
		payload["desp"] = body
	} else {
		payload["body"] = body
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http status: %d", resp.StatusCode)
	}
	return nil
}

// convertAppriseURL converts known Apprise URL schemes to real HTTP endpoints
func convertAppriseURL(url string) string {
	if strings.HasPrefix(url, "schan://") {
		token := strings.TrimPrefix(url, "schan://")
		return fmt.Sprintf("https://sctapi.ftqq.com/%s.send", token)
	}
	if strings.HasPrefix(url, "wecombot://") {
		key := strings.TrimPrefix(url, "wecombot://")
		return fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=%s", key)
	}
	if strings.HasPrefix(url, "discord://") {
		parts := strings.Split(strings.TrimPrefix(url, "discord://"), "/")
		if len(parts) >= 2 {
			return fmt.Sprintf("https://discord.com/api/webhooks/%s/%s", parts[0], parts[1])
		}
	}
	if strings.HasPrefix(url, "json://") {
		return "http://" + strings.TrimPrefix(url, "json://")
	}
	if strings.HasPrefix(url, "jsons://") {
		return "https://" + strings.TrimPrefix(url, "jsons://")
	}
	// telegram, etc. — require apprise CLI for full support
	return ""
}
