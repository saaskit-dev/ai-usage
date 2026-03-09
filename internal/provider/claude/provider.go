package claude

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/provider"
)

type Config struct {
	Token           string
	CredentialsPath string // 自定义凭证目录路径
}

type Provider struct {
	config Config
	id     string // 内部唯一标识，用于 map key（如 "claude", "claude-.claude-max"）
	client *http.Client
	email  string // 缓存从 profile API 获取的 email
}

func NewProvider(opts ...func(*Config)) *Provider {
	cfg := Config{}
	for _, opt := range opts {
		opt(&cfg)
	}


	// 内部 ID 用于 map key，确保唯一
	id := "claude"
	if cfg.CredentialsPath != "" {
		base := filepath.Base(cfg.CredentialsPath)
		id = "claude-" + base
	}

	return &Provider{
		config: cfg,
		id:     id,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func WithToken(token string) func(*Config) {
	return func(c *Config) {
		c.Token = token
	}
}

func WithCredentialsPath(path string) func(*Config) {
	return func(c *Config) {
		c.CredentialsPath = path
	}
}

// ID 返回内部唯一标识，用于 map key
func (p *Provider) ID() string {
	return p.id
}

// Name 返回展示名称，所有 Claude provider 都叫 "claude"
func (p *Provider) Name() string {
	return "claude"
}

func (p *Provider) Probe(ctx context.Context) (provider.Usage, error) {
	// 首先尝试获取 profile 信息（用于显示账号 email）
	p.fetchProfile(ctx)

	// 自定义路径时跳过 CLI（CLI 只读默认 ~/.claude/ 凭证），直接走 API
	if p.config.CredentialsPath == "" {
		usage, err := p.probeCLI(ctx)
		if err == nil && len(usage.Quotas) > 0 {
			p.fillUsageMeta(&usage)
			return usage, nil
		}
	}
	usage := p.probeAPI(ctx)
	p.fillUsageMeta(&usage)
	return usage, nil
}

// fillUsageMeta 填充 usage 的公共元信息
func (p *Provider) fillUsageMeta(usage *provider.Usage) {
	usage.Provider = p.Name()
	usage.Email = p.email
	if p.config.CredentialsPath != "" {
		usage.Path = p.config.CredentialsPath
	}
}

// fetchProfile 调用 /api/oauth/profile 获取用户信息
func (p *Provider) fetchProfile(ctx context.Context) {
	if p.email != "" {
		return // 已缓存
	}

	token := p.config.Token
	if token == "" {
		var err error
		token, err = p.loadTokenFromCredentials()
		if err != nil || token == "" {
			return
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/api/oauth/profile", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return
	}

	var data struct {
		Account struct {
			Email string `json:"email"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return
	}

	if data.Account.Email != "" {
		p.email = data.Account.Email
	}
}

func (p *Provider) probeCLI(ctx context.Context) (provider.Usage, error) {
	usage := provider.Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}

	cmd := exec.CommandContext(ctx, "claude", "/usage", "--allowed-tools", "")
	cmd.Env = os.Environ()
	var newEnv []string
	for _, env := range cmd.Env {
		if !strings.HasPrefix(env, "CLAUDE_CODE_OAUTH_TOKEN=") {
			newEnv = append(newEnv, env)
		}
	}
	cmd.Env = newEnv

	output, err := cmd.CombinedOutput()
	if err != nil {
		usage.Error = fmt.Sprintf("cli error: %v", err)
		return usage, fmt.Errorf("cli failed: %w", err)
	}

	clean := stripANSI(string(output))
	usage.Quotas = p.parseQuotas(clean)
	usage.Tier = p.detectTier(clean)

	return usage, nil
}

func (p *Provider) probeAPI(ctx context.Context) provider.Usage {
	usage := provider.Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}

	token := p.config.Token
	if token == "" {
		var err error
		token, err = p.loadTokenFromCredentials()
		if err != nil {
			usage.Error = fmt.Sprintf("no token found: %v", err)
			return usage
		}
	}

	if token == "" {
		usage.Error = "no token available"
		return usage
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		usage.Error = err.Error()
		return usage
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	resp, err := p.client.Do(req)
	if err != nil {
		usage.Error = err.Error()
		return usage
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		usage.Error = fmt.Sprintf("api status: %d", resp.StatusCode)
		return usage
	}

	var data struct {
		FiveHour *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    *string `json:"resets_at"`
		} `json:"five_hour"`
		SevenDay *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    *string `json:"resets_at"`
		} `json:"seven_day"`
		SevenDaySonnet *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    *string `json:"resets_at"`
		} `json:"seven_day_sonnet"`
		SevenDayOpus *struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    *string `json:"resets_at"`
		} `json:"seven_day_opus"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		usage.Error = err.Error()
		return usage
	}

	if data.FiveHour != nil {
		usage.Quotas = append(usage.Quotas, provider.Quota{
			PercentRemaining: 100 - data.FiveHour.Utilization,
			Type:             "session",
			ResetText:        formatResetText(data.FiveHour.ResetsAt),
			ResetTime:        parseResetTime(data.FiveHour.ResetsAt),
		})
	}
	if data.SevenDay != nil {
		usage.Quotas = append(usage.Quotas, provider.Quota{
			PercentRemaining: 100 - data.SevenDay.Utilization,
			Type:             "weekly",
			ResetText:        formatResetText(data.SevenDay.ResetsAt),
			ResetTime:        parseResetTime(data.SevenDay.ResetsAt),
		})
	}
	if data.SevenDayOpus != nil {
		usage.Quotas = append(usage.Quotas, provider.Quota{
			PercentRemaining: 100 - data.SevenDayOpus.Utilization,
			Type:             "opus",
			ResetText:        formatResetText(data.SevenDayOpus.ResetsAt),
			ResetTime:        parseResetTime(data.SevenDayOpus.ResetsAt),
		})
	}
	if data.SevenDaySonnet != nil {
		usage.Quotas = append(usage.Quotas, provider.Quota{
			PercentRemaining: 100 - data.SevenDaySonnet.Utilization,
			Type:             "sonnet",
			ResetText:        formatResetText(data.SevenDaySonnet.ResetsAt),
			ResetTime:        parseResetTime(data.SevenDaySonnet.ResetsAt),
		})
	}

	usage.Tier = "pro"

	return usage
}

func (p *Provider) loadTokenFromCredentials() (string, error) {
	var credPath string

	// 如果配置了自定义凭证路径
	if p.config.CredentialsPath != "" {
		credPath = filepath.Join(p.config.CredentialsPath, ".credentials.json")
	} else {
		// 默认路径
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		credPath = filepath.Join(home, ".claude", ".credentials.json")
	}

	data, err := os.ReadFile(credPath)
	if err == nil {
		var creds struct {
			ClaudeAiOAuth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(data, &creds) == nil && creds.ClaudeAiOAuth.AccessToken != "" {
			return creds.ClaudeAiOAuth.AccessToken, nil
		}
	}

	// macOS Keychain
	keychainService := p.keychainServiceName()
	cmd := exec.Command("security", "find-generic-password", "-s", keychainService, "-w")
	out, err := cmd.Output()
	if err == nil {
		var creds struct {
			ClaudeAiOAuth struct {
				AccessToken string `json:"accessToken"`
			} `json:"claudeAiOauth"`
		}
		if json.Unmarshal(out, &creds) == nil && creds.ClaudeAiOAuth.AccessToken != "" {
			return creds.ClaudeAiOAuth.AccessToken, nil
		}
	}

	return "", nil
}

// keychainServiceName 返回 macOS Keychain 中的 service name
// 默认路径: "Claude Code-credentials"
// 自定义路径: "Claude Code-credentials-{sha256(abs_path)[:8]}"
func (p *Provider) keychainServiceName() string {
	if p.config.CredentialsPath == "" {
		return "Claude Code-credentials"
	}

	// 展开 ~ 到 home 目录
	path := p.config.CredentialsPath
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	// 移除尾部斜杠
	absPath = strings.TrimRight(absPath, "/")
	hash := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("Claude Code-credentials-%x", hash[:4])
}

func formatResetText(resetsAt *string) string {
	if resetsAt == nil || *resetsAt == "" {
		return ""
	}
	return provider.FormatResetText(*resetsAt)
}

func parseResetTime(resetsAt *string) time.Time {
	if resetsAt == nil || *resetsAt == "" {
		return time.Time{}
	}
	return provider.ParseTime(*resetsAt)
}

func (p *Provider) parseQuotas(text string) []provider.Quota {
	var quotas []provider.Quota

	if pct := p.extractPercent("Current session", text); pct >= 0 {
		resetText := p.extractReset("Current session", text)
		q := provider.Quota{
			PercentRemaining: float64(pct),
			Type:             "session",
			ResetText:        resetText,
			ResetTime:        parseResetFromText(resetText),
		}
		if used, limit := p.extractUsedLimit("Current session", text); limit > 0 {
			q.Used = used
			q.Limit = limit
		}
		quotas = append(quotas, q)
	}
	if pct := p.extractPercent("Current week (all models)", text); pct >= 0 {
		resetText := p.extractReset("Current week", text)
		q := provider.Quota{
			PercentRemaining: float64(pct),
			Type:             "weekly",
			ResetText:        resetText,
			ResetTime:        parseResetFromText(resetText),
		}
		if used, limit := p.extractUsedLimit("Current week (all models)", text); limit > 0 {
			q.Used = used
			q.Limit = limit
		}
		quotas = append(quotas, q)
	}
	if pct := p.extractPercent("Current week (Opus)", text); pct >= 0 {
		resetText := p.extractReset("Current week (Opus)", text)
		q := provider.Quota{
			PercentRemaining: float64(pct),
			Type:             "opus",
			ResetText:        resetText,
			ResetTime:        parseResetFromText(resetText),
		}
		if used, limit := p.extractUsedLimit("Current week (Opus)", text); limit > 0 {
			q.Used = used
			q.Limit = limit
		}
		quotas = append(quotas, q)
	}
	if pct := p.extractPercent("Current week (Sonnet", text); pct >= 0 {
		resetText := p.extractReset("Current week (Sonnet", text)
		q := provider.Quota{
			PercentRemaining: float64(pct),
			Type:             "sonnet",
			ResetText:        resetText,
			ResetTime:        parseResetFromText(resetText),
		}
		if used, limit := p.extractUsedLimit("Current week (Sonnet", text); limit > 0 {
			q.Used = used
			q.Limit = limit
		}
		quotas = append(quotas, q)
	}

	return quotas
}

// extractUsedLimit 从 CLI 输出中提取 "152/300 requests" 格式的使用量
func (p *Provider) extractUsedLimit(label, text string) (used, limit int) {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(label))
	if idx == -1 {
		return 0, 0
	}

	search := text[idx:]
	if len(search) > 300 {
		search = search[:300]
	}

	re := regexp.MustCompile(`(\d+)\s*/\s*(\d+)\s*requests`)
	m := re.FindStringSubmatch(search)
	if len(m) < 3 {
		return 0, 0
	}

	used, _ = strconv.Atoi(m[1])
	limit, _ = strconv.Atoi(m[2])
	return used, limit
}

// parseResetFromText 从 "Resets in 1d 2h" 等文本解析出 reset 时间
func parseResetFromText(resetText string) time.Time {
	if resetText == "" {
		return time.Time{}
	}

	now := time.Now()

	// 匹配 "Resets in Xd Yh" / "Resets in Xh Ym" / "Resets in Xm" 格式
	re := regexp.MustCompile(`(?i)resets\s+in\s+(.+)`)
	m := re.FindStringSubmatch(resetText)
	if len(m) < 2 {
		return time.Time{}
	}

	durationStr := strings.TrimSpace(m[1])
	var totalMinutes float64

	// 解析天
	if dRe := regexp.MustCompile(`(\d+)\s*d`); true {
		if dm := dRe.FindStringSubmatch(durationStr); len(dm) >= 2 {
			d, _ := strconv.Atoi(dm[1])
			totalMinutes += float64(d) * 24 * 60
		}
	}
	// 解析小时
	if hRe := regexp.MustCompile(`(\d+)\s*h`); true {
		if hm := hRe.FindStringSubmatch(durationStr); len(hm) >= 2 {
			h, _ := strconv.Atoi(hm[1])
			totalMinutes += float64(h) * 60
		}
	}
	// 解析分钟
	if mRe := regexp.MustCompile(`(\d+)\s*m`); true {
		if mm := mRe.FindStringSubmatch(durationStr); len(mm) >= 2 {
			mins, _ := strconv.Atoi(mm[1])
			totalMinutes += float64(mins)
		}
	}

	if totalMinutes > 0 {
		return now.Add(time.Duration(math.Round(totalMinutes)) * time.Minute)
	}

	return time.Time{}
}

func (p *Provider) extractPercent(label, text string) int {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(label))
	if idx == -1 {
		return -1
	}

	search := text[idx:]
	if len(search) > 200 {
		search = search[:200]
	}

	re := regexp.MustCompile(`(\d{1,3})\s*%\s*(left|used)`)
	m := re.FindStringSubmatch(search)
	if len(m) < 3 {
		return -1
	}

	pct, _ := strconv.Atoi(m[1])
	if strings.ToLower(m[2]) == "used" {
		return 100 - pct
	}
	return pct
}

func (p *Provider) extractReset(label, text string) string {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(label))
	if idx == -1 {
		return ""
	}

	search := text[idx:]
	if len(search) > 300 {
		search = search[:300]
	}

	re := regexp.MustCompile(`(?i)(Resets[^\n]+)`)
	return strings.TrimSpace(re.FindString(search))
}

func (p *Provider) detectTier(text string) string {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "claude pro") {
		return "pro"
	}
	if strings.Contains(lower, "claude max") {
		return "max"
	}
	if strings.Contains(lower, "api usage billing") {
		return "api"
	}
	return "unknown"
}

func stripANSI(s string) string {
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return re.ReplaceAllString(s, "")
}
