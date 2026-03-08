package cursor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kilingzhang/ai-usage/internal/provider"
	_ "github.com/mattn/go-sqlite3"
)

type Config struct {
	Token string
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
		cfg.Token = os.Getenv("CURSOR_TOKEN")
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

func (p *Provider) ID() string { return "cursor" }

func (p *Provider) Name() string {
	return "cursor"
}

func (p *Provider) Probe(ctx context.Context) (provider.Usage, error) {
	usage := provider.Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}

	token := p.config.Token
	if token == "" {
		var err error
		token, err = p.extractTokenFromDB()
		if err != nil {
			usage.Error = err.Error()
			return usage, nil
		}
	}

	userId, err := extractUserIdFromJWT(token)
	if err != nil {
		userId = "unknown"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://cursor.com/api/usage-summary", nil)
	if err != nil {
		usage.Error = err.Error()
		return usage, nil
	}

	cookie := fmt.Sprintf("WorkosCursorSessionToken=%s::%s", userId, token)
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		usage.Error = err.Error()
		return usage, nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		usage.Error = fmt.Sprintf("api status: %d, body: %s", resp.StatusCode, string(body))
		return usage, nil
	}

	var data struct {
		MembershipType    string `json:"membershipType"`
		IsUnlimited       bool   `json:"isUnlimited"`
		BillingCycleStart string `json:"billingCycleStart"`
		BillingCycleEnd   string `json:"billingCycleEnd"`
		IndividualUsage   struct {
			Plan struct {
				Enabled   bool `json:"enabled"`
				Used      int  `json:"used"`
				Limit     int  `json:"limit"`
				Remaining int  `json:"remaining"`
			} `json:"plan"`
			OnDemand struct {
				Enabled   bool `json:"enabled"`
				Used      int  `json:"used"`
				Limit     *int `json:"limit"`
				Remaining *int `json:"remaining"`
			} `json:"onDemand"`
		} `json:"individualUsage"`
	}

	if err := json.Unmarshal(body, &data); err != nil {
		usage.Error = err.Error()
		return usage, nil
	}

	if data.IndividualUsage.Plan.Enabled && data.IndividualUsage.Plan.Limit > 0 {
		used := data.IndividualUsage.Plan.Used
		limit := data.IndividualUsage.Plan.Limit
		percentRemaining := float64(limit-used) / float64(limit) * 100
		resetText := fmt.Sprintf("%d/%d requests", used, limit)

		if data.BillingCycleEnd != "" {
			if rt := formatResetText(data.BillingCycleEnd); rt != "" {
				resetText += " · " + rt
			}
		}

		usage.Quotas = append(usage.Quotas, provider.Quota{
			Type:             "monthly",
			PercentRemaining: percentRemaining,
			Used:             used,
			Limit:            limit,
			ResetText:        resetText,
			ResetTime:        parseResetTime(data.BillingCycleEnd),
		})
	}

	usage.Tier = data.MembershipType

	return usage, nil
}

func (p *Provider) extractTokenFromDB() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	dbPaths := []string{
		filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb"),
	}

	var dbPath string
	for _, p := range dbPaths {
		if _, err := os.Stat(p); err == nil {
			dbPath = p
			break
		}
	}
	if dbPath == "" {
		return "", fmt.Errorf("cursor database not found")
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return "", fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	var token string
	query := `SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'`
	row := db.QueryRow(query)
	if err := row.Scan(&token); err != nil {
		return "", fmt.Errorf("query token: %w", err)
	}

	return token, nil
}

func extractUserIdFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid JWT format")
	}

	payload := parts[1]
	payload = strings.ReplaceAll(payload, "-", "+")
	payload = strings.ReplaceAll(payload, "_", "/")

	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}

	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT: %w", err)
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT: %w", err)
	}

	sub, ok := claims["sub"].(string)
	if !ok {
		return "", fmt.Errorf("sub claim not found")
	}

	return sub, nil
}

func formatResetText(dateStr string) string {
	return provider.FormatResetText(dateStr)
}

func parseResetTime(dateStr string) time.Time {
	return provider.ParseTime(dateStr)
}

func formatDuration(t time.Time) string {
	return provider.FormatDuration(t)
}
