package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/kilingzhang/ai-usage/internal/monitor"
	"github.com/kilingzhang/ai-usage/internal/notify"
	"github.com/kilingzhang/ai-usage/internal/provider"
)

type Server struct {
	logger   *slog.Logger
	monitor  *monitor.Monitor
	notifier *notify.Manager
	addr     string
	http     *http.Server
}

type UsageResponse struct {
	Usage       []provider.Usage `json:"usage"`
	LastUpdated time.Time        `json:"last_updated"`
	Ready       bool             `json:"ready"`
}

func NewServer(logger *slog.Logger, mon *monitor.Monitor, notifier *notify.Manager, addr string) *Server {
	s := &Server{logger: logger, monitor: mon, notifier: notifier, addr: addr}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/usage", s.handleUsage)
	mux.HandleFunc("/config", s.handleConfig)
	mux.HandleFunc("/notify", s.handleNotify)
	s.http = &http.Server{Addr: addr, Handler: mux}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("api server starting", "addr", s.addr)
		errCh <- s.http.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	health := s.monitor.Health()
	providerHealth := make(map[string]interface{}, len(health))
	for name, h := range health {
		entry := map[string]interface{}{
			"consecutive_fails": h.ConsecutiveFails,
		}
		if !h.LastSuccess.IsZero() {
			entry["last_success"] = h.LastSuccess
		}
		if !h.LastAttempt.IsZero() {
			entry["last_attempt"] = h.LastAttempt
		}
		if h.LastError != "" {
			entry["last_error"] = h.LastError
		}
		providerHealth[name] = entry
	}

	status := "ok"
	for _, h := range health {
		if h.ConsecutiveFails > 0 {
			status = "degraded"
			break
		}
	}

	result := map[string]interface{}{
		"status":    status,
		"ready":     s.monitor.Ready(),
		"providers": providerHealth,
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.URL.Query().Get("force") == "true" {
		s.monitor.TriggerProbe(r.Context())
		select {
		case <-s.monitor.ReadyCh():
		case <-time.After(15 * time.Second):
		case <-r.Context().Done():
		}
	} else if !s.monitor.Ready() {
		select {
		case <-s.monitor.ReadyCh():
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}

	usage := s.monitor.LatestWithFallback()

	response := UsageResponse{
		Usage:       usage,
		LastUpdated: s.monitor.LastUpdated(),
		Ready:       s.monitor.Ready(),
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	configInfo := map[string]interface{}{
		"providers": map[string]bool{
			"claude":  true,
			"copilot": true,
			"cursor":  true,
		},
		"notifications_active": s.notifier != nil && s.notifier.HasNotifiers(),
	}

	_ = json.NewEncoder(w).Encode(configInfo)
}


func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "read body failed"})
		return
	}
	defer r.Body.Close()

	var req struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	if req.Title == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "title is required"})
		return
	}

	event := notify.Event{
		Type:      notify.EventManual,
		Provider:  "manual",
		Timestamp: time.Now(),
		Message:   fmt.Sprintf("%s\n%s", req.Title, req.Body),
	}

	if s.notifier == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "notifier not configured"})
		return
	}

	if err := s.notifier.Notify(r.Context(), event); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("notify failed: %v", err)})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "sent"})
}