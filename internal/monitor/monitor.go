package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/saaskit-dev/ai-usage/internal/config"
	"github.com/saaskit-dev/ai-usage/internal/notify"
	"github.com/saaskit-dev/ai-usage/internal/provider"
)

// ProviderHealth tracks probe health for a single provider.
type ProviderHealth struct {
	LastSuccess      time.Time `json:"last_success"`
	LastAttempt      time.Time `json:"last_attempt"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	LastError        string    `json:"last_error,omitempty"`
}

type Monitor struct {
	logger   *slog.Logger
	registry *provider.Registry
	notifier *notify.Manager
	interval time.Duration
	dataFile string
	rules    []config.NotifyRule

	mu          sync.RWMutex
	latest      map[string]provider.Usage
	lastGood    map[string]provider.Usage // last successful probe per provider (for fallback)
	statuses    map[string]provider.QuotaStatus
	ready       bool
	readyCh     chan struct{}
	lastUpdated time.Time

	// Dedup: track which (provider, rule) combos have already fired,
	// so we don't spam on every probe cycle.
	firedThresholds map[string]map[float64]bool // provider -> threshold -> fired
	firedDepleted   map[string]bool             // provider -> fired
	firedProbeError map[string]bool             // provider -> fired
	firedResetSoon  map[string]bool             // provider -> fired

	// Per-provider health tracking
	health map[string]*ProviderHealth
}

func New(logger *slog.Logger, registry *provider.Registry, interval time.Duration) *Monitor {
	return &Monitor{
		logger:          logger,
		registry:        registry,
		notifier:        notify.NewManager(logger),
		interval:        interval,
		latest:          make(map[string]provider.Usage),
		lastGood:        make(map[string]provider.Usage),
		statuses:        make(map[string]provider.QuotaStatus),
		readyCh:         make(chan struct{}),
		firedThresholds: make(map[string]map[float64]bool),
		firedDepleted:   make(map[string]bool),
		firedProbeError: make(map[string]bool),
		firedResetSoon:  make(map[string]bool),
		health:          make(map[string]*ProviderHealth),
	}
}

func (m *Monitor) SetDataFile(path string) {
	m.dataFile = path
}

func (m *Monitor) SetRules(rules []config.NotifyRule) {
	m.rules = rules
}

func (m *Monitor) Load() {
	if m.dataFile == "" {
		return
	}
	data, err := os.ReadFile(m.dataFile)
	if err != nil {
		return
	}

	var saved struct {
		Latest          map[string]provider.Usage      `json:"latest"`
		Previous        map[string]provider.Usage      `json:"previous,omitempty"`
		LastUpdated     time.Time                      `json:"last_updated"`
		FiredThresholds map[string]map[string]bool     `json:"fired_thresholds,omitempty"`
		FiredDepleted   map[string]bool                `json:"fired_depleted,omitempty"`
		FiredProbeError map[string]bool                `json:"fired_probe_error,omitempty"`
		FiredResetSoon  map[string]bool                `json:"fired_reset_soon,omitempty"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		m.logger.Warn("failed to load saved data", "error", err)
		return
	}

	m.mu.Lock()
	m.latest = saved.Latest
	m.lastUpdated = saved.LastUpdated
	if saved.Previous != nil {
		m.lastGood = saved.Previous
	}
	if saved.FiredThresholds != nil {
		for pid, strMap := range saved.FiredThresholds {
			m.firedThresholds[pid] = make(map[float64]bool, len(strMap))
			for k, v := range strMap {
				if f, err := strconv.ParseFloat(k, 64); err == nil {
					m.firedThresholds[pid][f] = v
				}
			}
		}
	}
	if saved.FiredDepleted != nil {
		m.firedDepleted = saved.FiredDepleted
	}
	if saved.FiredProbeError != nil {
		m.firedProbeError = saved.FiredProbeError
	}
	if saved.FiredResetSoon != nil {
		m.firedResetSoon = saved.FiredResetSoon
	}
	m.ready = true
	m.mu.Unlock()

	m.logger.Info("loaded saved usage data", "providers", len(m.latest))
}

func (m *Monitor) save() {
	if m.dataFile == "" {
		return
	}

	m.mu.RLock()
	// Convert firedThresholds float64 keys to strings for JSON
	serializableThresholds := make(map[string]map[string]bool, len(m.firedThresholds))
	for pid, tMap := range m.firedThresholds {
		strMap := make(map[string]bool, len(tMap))
		for k, v := range tMap {
			strMap[strconv.FormatFloat(k, 'f', -1, 64)] = v
		}
		serializableThresholds[pid] = strMap
	}
	data, err := json.Marshal(map[string]interface{}{
		"latest":            m.latest,
		"previous":          m.lastGood,
		"last_updated":      m.lastUpdated,
		"fired_thresholds":  serializableThresholds,
		"fired_depleted":    m.firedDepleted,
		"fired_probe_error": m.firedProbeError,
		"fired_reset_soon":  m.firedResetSoon,
	})
	m.mu.RUnlock()

	if err != nil {
		m.logger.Warn("failed to marshal save data", "error", err)
		return
	}

	// 确保目录存在
	if err := os.MkdirAll(filepath.Dir(m.dataFile), 0755); err != nil {
		m.logger.Warn("failed to create data directory", "error", err)
		return
	}

	if err := os.WriteFile(m.dataFile, data, 0600); err != nil {
		m.logger.Warn("failed to save data", "error", err)
	}
}

func (m *Monitor) SetNotifier(n *notify.Manager) {
	m.notifier = n
}

func (m *Monitor) Run(ctx context.Context) {
	m.Load()

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	// reset_soon checker runs every 30s for precision
	resetTicker := time.NewTicker(30 * time.Second)
	defer resetTicker.Stop()

	m.probe(ctx)

	for {
		select {
		case <-ctx.Done():
			m.save()
			return
		case <-ticker.C:
			m.probe(ctx)
			m.save()
		case <-resetTicker.C:
			m.checkResetSoon(ctx)
		}
	}
}

func (m *Monitor) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ready
}

// ReadyCh returns a channel that is closed when the first probe completes.
func (m *Monitor) ReadyCh() <-chan struct{} {
	return m.readyCh
}

func (m *Monitor) LastUpdated() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastUpdated
}

func (m *Monitor) LatestWithFallback() []provider.Usage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]provider.Usage, 0, len(m.latest))
	for id, u := range m.latest {
		prev, hasPrev := m.lastGood[id]
		if u.Error != "" && hasPrev && prev.Error == "" {
			result = append(result, prev)
		} else {
			result = append(result, u)
		}
	}
	return result
}

func (m *Monitor) Latest() []provider.Usage {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]provider.Usage, 0, len(m.latest))
	for _, u := range m.latest {
		result = append(result, u)
	}
	return result
}

func (m *Monitor) TriggerProbe(ctx context.Context) {
	go m.probe(ctx)
}

// Health returns a snapshot of per-provider health tracking.
func (m *Monitor) Health() map[string]ProviderHealth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]ProviderHealth, len(m.health))
	for k, v := range m.health {
		result[k] = *v
	}
	return result
}

func (m *Monitor) probe(ctx context.Context) {
	data := m.registry.ProbeAll(ctx)

	m.mu.Lock()
	for _, r := range data {
		id := r.ID
		u := r.Usage
		oldStatus := m.statuses[id]
		newStatus := u.OverallStatus()

		// Only update lastGood when probe succeeds (for fallback on future errors)
		if u.Error == "" {
			m.lastGood[id] = u
		}

		m.latest[id] = u
		m.statuses[id] = newStatus

		// Status change notification (legacy behavior)
		if oldStatus != "" && oldStatus != newStatus {
			m.logger.Info("status changed",
				"provider", id,
				"old", oldStatus,
				"new", newStatus,
			)
		}

		// Update health tracking
		now := time.Now()
		h, ok := m.health[id]
		if !ok {
			h = &ProviderHealth{}
			m.health[id] = h
		}
		h.LastAttempt = now
		if u.Error != "" {
			h.ConsecutiveFails++
			h.LastError = u.Error
		} else {
			h.LastSuccess = now
			h.ConsecutiveFails = 0
			h.LastError = ""
		}
	}

	// 清理不再注册的 provider（防止旧 usage.json 中的幽灵条目残留）
	registered := m.registry.IDs()
	for id := range m.latest {
		if !registered[id] {
			delete(m.latest, id)
			delete(m.lastGood, id)
			delete(m.statuses, id)
			delete(m.health, id)
			delete(m.firedThresholds, id)
			delete(m.firedDepleted, id)
			delete(m.firedProbeError, id)
			delete(m.firedResetSoon, id)
			m.logger.Info("cleaned up stale provider", "provider", id)
		}
	}

	if !m.ready {
		m.ready = true
		close(m.readyCh)
	}

	m.lastUpdated = time.Now()
	m.mu.Unlock()

	// Evaluate rules AFTER releasing the lock
	for _, r := range data {
		m.evaluateRules(ctx, r.ID, r.Usage)
	}

	m.logger.Info("probe completed", "providers", len(data))
	m.save()
}

// evaluateRules checks all configured rules against a single provider's usage.
func (m *Monitor) evaluateRules(ctx context.Context, id string, usage provider.Usage) {
	for _, rule := range m.rules {
		// If rule is scoped to specific providers, check filter (match on display name or ID)
		if len(rule.Providers) > 0 && !containsProvider(rule.Providers, usage.Provider) && !containsProvider(rule.Providers, id) {
			continue
		}

		switch rule.Event {
		case "threshold":
			m.evalThreshold(ctx, id, usage, rule.Threshold)
		case "depleted":
			m.evalDepleted(ctx, id, usage)
		case "probe_error":
			m.evalProbeError(ctx, id, usage)
		case "reset_soon":
			// handled by checkResetSoon ticker, skip here
		case "status_change":
			m.evalStatusChange(ctx, id, usage)
		}
	}
}

func (m *Monitor) evalThreshold(ctx context.Context, id string, usage provider.Usage, threshold float64) {
	if threshold <= 0 {
		return
	}

	lowest := usage.LowestPercent()
	if lowest >= threshold {
		m.mu.Lock()
		if m.firedThresholds[id] != nil {
			delete(m.firedThresholds[id], threshold)
		}
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	if m.firedThresholds[id] == nil {
		m.firedThresholds[id] = make(map[float64]bool)
	}
	if m.firedThresholds[id][threshold] {
		m.mu.Unlock()
		return
	}
	m.firedThresholds[id][threshold] = true
	m.mu.Unlock()

	event := notify.Event{
		Type:      notify.EventThreshold,
		Provider:  usage.Provider,
		Usage:     usage,
		Timestamp: time.Now(),
		Message:   fmt.Sprintf("Quota dropped below %.0f%% (current: %.1f%%)", threshold, lowest),
	}
	m.sendNotification(ctx, event)
}

func (m *Monitor) evalDepleted(ctx context.Context, id string, usage provider.Usage) {
	status := usage.OverallStatus()
	if status != provider.StatusDepleted {
		m.mu.Lock()
		delete(m.firedDepleted, id)
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	if m.firedDepleted[id] {
		m.mu.Unlock()
		return
	}
	m.firedDepleted[id] = true
	m.mu.Unlock()

	event := notify.Event{
		Type:      notify.EventDepleted,
		Provider:  usage.Provider,
		Usage:     usage,
		Timestamp: time.Now(),
	}
	m.sendNotification(ctx, event)
}

func (m *Monitor) evalProbeError(ctx context.Context, id string, usage provider.Usage) {
	if usage.Error == "" {
		m.mu.Lock()
		delete(m.firedProbeError, id)
		m.mu.Unlock()
		return
	}

	m.mu.Lock()
	if m.firedProbeError[id] {
		m.mu.Unlock()
		return
	}
	m.firedProbeError[id] = true
	m.mu.Unlock()

	event := notify.Event{
		Type:      notify.EventProbeError,
		Provider:  usage.Provider,
		Usage:     usage,
		Timestamp: time.Now(),
		Message:   fmt.Sprintf("Probe failed: %s", usage.Error),
	}
	m.sendNotification(ctx, event)
}

func (m *Monitor) evalStatusChange(ctx context.Context, id string, usage provider.Usage) {
	m.mu.RLock()
	oldStatus := m.statuses[id]
	m.mu.RUnlock()

	newStatus := usage.OverallStatus()
	if oldStatus == "" || oldStatus == newStatus {
		return
	}

	event := notify.Event{
		Type:      notify.EventStatusChange,
		Provider:  usage.Provider,
		OldStatus: oldStatus,
		NewStatus: newStatus,
		Usage:     usage,
		Timestamp: time.Now(),
	}

	if newStatus == provider.StatusDepleted {
		event.Type = notify.EventDepleted
	} else if newStatus == provider.StatusCritical {
		event.Type = notify.EventCritical
	} else if newStatus == provider.StatusWarning {
		event.Type = notify.EventWarning
	}

	m.sendNotification(ctx, event)
}

// checkResetSoon runs periodically and checks if any depleted provider
// has a reset time approaching within the configured "before" duration.
func (m *Monitor) checkResetSoon(ctx context.Context) {
	// Find reset_soon rules
	var resetRules []config.NotifyRule
	for _, r := range m.rules {
		if r.Event == "reset_soon" {
			resetRules = append(resetRules, r)
		}
	}
	if len(resetRules) == 0 {
		return
	}

	m.mu.RLock()
	type idUsage struct {
		id    string
		usage provider.Usage
	}
	usages := make([]idUsage, 0, len(m.latest))
	for id, u := range m.latest {
		usages = append(usages, idUsage{id: id, usage: u})
	}
	m.mu.RUnlock()

	now := time.Now()

	for _, iu := range usages {
		id := iu.id
		usage := iu.usage
		// Only alert for depleted providers
		if usage.OverallStatus() != provider.StatusDepleted {
			// Clear fired state if no longer depleted
			m.mu.Lock()
			delete(m.firedResetSoon, id)
			m.mu.Unlock()
			continue
		}

		for _, rule := range resetRules {
			if len(rule.Providers) > 0 && !containsProvider(rule.Providers, usage.Provider) && !containsProvider(rule.Providers, id) {
				continue
			}

			beforeDur, err := time.ParseDuration(rule.Before)
			if err != nil {
				beforeDur = 10 * time.Minute // default
			}

			// Check all quotas for upcoming reset
			for _, q := range usage.Quotas {
				if q.ResetTime.IsZero() {
					continue
				}

				timeUntilReset := q.ResetTime.Sub(now)
				if timeUntilReset <= 0 || timeUntilReset > beforeDur {
					continue
				}

				// Within the "before" window — check dedup
				m.mu.Lock()
				if m.firedResetSoon[id] {
					m.mu.Unlock()
					continue
				}
				m.firedResetSoon[id] = true
				m.mu.Unlock()

				event := notify.Event{
					Type:      notify.EventResetSoon,
					Provider:  usage.Provider,
					Usage:     usage,
					Timestamp: now,
					Message:   fmt.Sprintf("Quota resets in %s (at %s)", formatDuration(timeUntilReset), q.ResetTime.Format(time.RFC3339)),
				}
				m.sendNotification(ctx, event)
				break // one notification per provider per cycle
			}
		}
	}
}

func (m *Monitor) sendNotification(ctx context.Context, event notify.Event) {
	if err := m.notifier.Notify(ctx, event); err != nil {
		m.logger.Error("notify failed", "event", event.Type, "provider", event.Provider, "error", err)
	}
}

func containsProvider(list []string, name string) bool {
	for _, p := range list {
		if p == name {
			return true
		}
	}
	return false
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", hours, minutes)
}
