package provider

import (
	"context"
	"strings"
	"sync"
	"time"
)

type Registry struct {
	mu        sync.RWMutex
	providers []Provider
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, p)
}

func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Provider, len(r.providers))
	copy(items, r.providers)
	return items
}

// IDs returns the set of registered provider IDs.
func (r *Registry) IDs() map[string]bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make(map[string]bool, len(r.providers))
	for _, p := range r.providers {
		ids[p.ID()] = true
	}
	return ids
}

// ProbeResult pairs an internal ID with probe result.
type ProbeResult struct {
	ID    string
	Usage Usage
}

func (r *Registry) ProbeAll(ctx context.Context) []ProbeResult {
	providers := r.List()
	results := make([]ProbeResult, 0, len(providers))
	for _, p := range providers {
		usage := r.probeWithRetry(ctx, p, 3)
		if usage.UpdatedAt.IsZero() {
			usage.UpdatedAt = time.Now()
		}
		for i := range usage.Quotas {
			usage.Quotas[i].Status = usage.Quotas[i].CalculateStatus()
		}
		results = append(results, ProbeResult{ID: p.ID(), Usage: usage})
	}
	return results
}

func (r *Registry) probeWithRetry(ctx context.Context, p Provider, maxRetries int) Usage {
	var lastErr error
	var lastUsage Usage

	for attempt := 0; attempt < maxRetries; attempt++ {
		usage, err := p.Probe(ctx)
		lastUsage = usage
		if err == nil && len(usage.Quotas) > 0 {
			return usage
		}

		if err != nil {
			lastErr = err
		}

		if usage.Error != "" {
			if strings.Contains(usage.Error, "429") || strings.Contains(usage.Error, "rate") {
				waitTime := time.Duration(attempt+1) * 2 * time.Second
				time.Sleep(waitTime)
				continue
			}
		}

		if err != nil && attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * time.Second)
		}
	}

	// 所有重试均失败，保留最后一次 probe 的错误信息
	if lastUsage.Provider != "" {
		if lastErr != nil && lastUsage.Error == "" {
			lastUsage.Error = lastErr.Error()
		}
		if lastUsage.UpdatedAt.IsZero() {
			lastUsage.UpdatedAt = time.Now()
		}
		return lastUsage
	}

	usage := Usage{
		Provider:  p.Name(),
		UpdatedAt: time.Now(),
	}
	if lastErr != nil {
		usage.Error = lastErr.Error()
	}
	return usage
}
