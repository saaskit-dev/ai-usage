package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/provider"
)

type Config struct {
	Token    string
	Username string
}

type Provider struct {
	config Config
	client *http.Client
}

func NewProvider(opts ...func(*Config)) *Provider {
	cfg := Config{}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("GITHUB_TOKEN")
	}
	if cfg.Username == "" {
		if username := getGitHubUsername(cfg.Token); username != "" {
			cfg.Username = username
		}
	}

	return &Provider{
		config: cfg,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func WithToken(token string) func(*Config) {
	return func(c *Config) {
		c.Token = token
	}
}

func WithUsername(username string) func(*Config) {
	return func(c *Config) {
		c.Username = username
	}
}

func (p *Provider) ID() string { return "copilot" }

func (p *Provider) Name() string {
	return "copilot"
}

func (p *Provider) Probe(ctx context.Context) (provider.Usage, error) {
	usage, err := p.probeInternalAPI(ctx)
	if err == nil && len(usage.Quotas) > 0 {
		return usage, nil
	}

	return p.probeBillingAPI(ctx)
}

func (p *Provider) probeInternalAPI(ctx context.Context) (provider.Usage, error) {
	usage := provider.Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}

	if p.config.Token == "" {
		return usage, fmt.Errorf("no token")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/copilot_internal/user", nil)
	if err != nil {
		return usage, err
	}
	req.Header.Set("Authorization", "Bearer "+p.config.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return usage, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return usage, fmt.Errorf("internal api status: %d", resp.StatusCode)
	}

	var data struct {
		CopilotPlan       string `json:"copilot_plan"`
		QuotaResetDate    string `json:"quota_reset_date"`
		QuotaResetDateUTC string `json:"quota_reset_date_utc"`
		QuotaSnapshots    struct {
			PremiumInteractions *struct {
				Entitlement      int     `json:"entitlement"`
				Remaining        int     `json:"remaining"`
				PercentRemaining float64 `json:"percent_remaining"`
				Unlimited        bool    `json:"unlimited"`
				OverageCount     int     `json:"overage_count"`
			} `json:"premium_interactions"`
		} `json:"quota_snapshots"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return usage, err
	}

	pi := data.QuotaSnapshots.PremiumInteractions
	if pi == nil {
		usage.Error = "no premium_interactions quota found"
		return usage, nil
	}

	if pi.Unlimited {
		usage.Quotas = append(usage.Quotas, provider.Quota{
			PercentRemaining: 100,
			Type:             "monthly",
			ResetText:        "Unlimited premium requests",
		})
		usage.Tier = data.CopilotPlan
		return usage, nil
	}

	used := pi.Entitlement - pi.Remaining
	if used < 0 {
		used = 0
	}

	resetText := fmt.Sprintf("%d/%d requests", used, pi.Entitlement)

	if data.QuotaResetDateUTC != "" {
		if rt := formatResetText(data.QuotaResetDateUTC); rt != "" {
			resetText += " · " + rt
		}
	} else if data.QuotaResetDate != "" {
		if rt := formatResetDate(data.QuotaResetDate); rt != "" {
			resetText += " · " + rt
		}
	}

	usage.Quotas = append(usage.Quotas, provider.Quota{
		PercentRemaining: pi.PercentRemaining,
		Used:             used,
		Limit:            pi.Entitlement,
		Type:             "monthly",
		ResetText:        resetText,
		ResetTime:        parseResetTime(data.QuotaResetDateUTC, data.QuotaResetDate),
	})
	usage.Tier = data.CopilotPlan

	return usage, nil
}

func (p *Provider) probeBillingAPI(ctx context.Context) (provider.Usage, error) {
	usage := provider.Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}

	if p.config.Token == "" {
		usage.Error = "GITHUB_TOKEN not set"
		return usage, nil
	}

	if p.config.Username == "" {
		usage.Error = "GitHub username not configured"
		return usage, nil
	}

	url := fmt.Sprintf("https://api.github.com/users/%s/settings/billing/premium_request/usage", p.config.Username)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		usage.Error = err.Error()
		return usage, nil
	}
	req.Header.Set("Authorization", "Bearer "+p.config.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := p.client.Do(req)
	if err != nil {
		usage.Error = err.Error()
		return usage, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		usage.Error = fmt.Sprintf("api status: %d", resp.StatusCode)
		return usage, nil
	}

	var data struct {
		TimePeriod struct {
			Year  int `json:"year"`
			Month int `json:"month"`
		} `json:"timePeriod"`
		UsageItems []struct {
			Product       string  `json:"product"`
			GrossQuantity float64 `json:"grossQuantity"`
		} `json:"usageItems"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		usage.Error = err.Error()
		return usage, nil
	}

	var totalUsed int
	for _, u := range data.UsageItems {
		if strings.EqualFold(u.Product, "Copilot") {
			totalUsed += int(u.GrossQuantity)
		}
	}

	limit := 50
	percentRemaining := float64(limit-totalUsed) / float64(limit) * 100
	if percentRemaining < 0 {
		percentRemaining = 0
	}

	usage.Quotas = append(usage.Quotas, provider.Quota{
		PercentRemaining: percentRemaining,
		Used:             totalUsed,
		Limit:            limit,
		Type:             "monthly",
		ResetText:        fmt.Sprintf("%d/%d requests", totalUsed, limit),
	})
	usage.Tier = "pro"

	return usage, nil
}

func formatResetText(isoDate string) string {
	return provider.FormatResetText(isoDate)
}

func formatResetDate(dateStr string) string {
	return provider.FormatResetText(dateStr)
}

func parseResetTime(utcDate, dateStr string) time.Time {
	if utcDate != "" {
		if t := provider.ParseTime(utcDate); !t.IsZero() {
			return t
		}
	}
	if dateStr != "" {
		return provider.ParseTime(dateStr)
	}
	return time.Time{}
}

func formatDuration(t time.Time) string {
	return provider.FormatDuration(t)
}

func getGitHubUsername(token string) string {
	if token == "" {
		return ""
	}
	cmd := exec.Command("gh", "api", "user", "--jq", ".login")
	cmd.Env = append(os.Environ(), "GITHUB_TOKEN="+token)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
