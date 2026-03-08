package provider

import (
	"context"
	"time"
)

type QuotaStatus string

const (
	StatusHealthy  QuotaStatus = "healthy"
	StatusWarning  QuotaStatus = "warning"
	StatusCritical QuotaStatus = "critical"
	StatusDepleted QuotaStatus = "depleted"
)

type Quota struct {
	PercentRemaining float64     `json:"percent_remaining"`
	Used             int         `json:"used,omitempty"`
	Limit            int         `json:"limit,omitempty"`
	Type             string      `json:"type"`
	Status           QuotaStatus `json:"status"`
	ResetText        string      `json:"reset_text,omitempty"`
	ResetTime        time.Time   `json:"reset_time,omitempty"`
}

func (q Quota) CalculateStatus() QuotaStatus {
	switch {
	case q.PercentRemaining <= 0:
		return StatusDepleted
	case q.PercentRemaining < 20:
		return StatusCritical
	case q.PercentRemaining < 50:
		return StatusWarning
	default:
		return StatusHealthy
	}
}

type Usage struct {
	Provider  string    `json:"provider"`
	Quotas    []Quota   `json:"quotas"`
	Email     string    `json:"email,omitempty"`
	Path      string    `json:"path,omitempty"`
	Tier      string    `json:"tier,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (u Usage) OverallStatus() QuotaStatus {
	if len(u.Quotas) == 0 {
		return StatusHealthy
	}
	worst := StatusHealthy
	for _, q := range u.Quotas {
		s := q.CalculateStatus()
		if s > worst {
			worst = s
		}
	}
	return worst
}

func (u Usage) LowestPercent() float64 {
	if len(u.Quotas) == 0 {
		return 100
	}
	lowest := 100.0
	for _, q := range u.Quotas {
		if q.PercentRemaining < lowest {
			lowest = q.PercentRemaining
		}
	}
	return lowest
}

type Provider interface {
	ID() string   // 内部唯一标识，用于 map key
	Name() string // 展示名称
	Probe(ctx context.Context) (Usage, error)
}
