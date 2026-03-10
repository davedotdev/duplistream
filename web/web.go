package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"duplistream/output"
)

//go:embed dashboard.html
var dashboardFS embed.FS

type StatusProvider interface {
	IsInputConnected() bool
	Uptime() time.Duration
	Outputs() map[string]*output.Output
	HealthStatus() string
}

type Server struct {
	addr     string
	provider StatusProvider
	version  string
}

func NewServer(addr string, provider StatusProvider, version string) *Server {
	return &Server{
		addr:     addr,
		provider: provider,
		version:  version,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/status", s.handleStatus)

	return http.ListenAndServe(s.addr, mux)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data, err := dashboardFS.ReadFile("dashboard.html")
	if err != nil {
		http.Error(w, "Dashboard not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := s.provider.HealthStatus()

	response := map[string]interface{}{
		"status":          status,
		"input_connected": s.provider.IsInputConnected(),
		"outputs":         make(map[string]map[string]interface{}),
	}

	for name, out := range s.provider.Outputs() {
		st := out.Status()
		response["outputs"].(map[string]map[string]interface{})[name] = map[string]interface{}{
			"running": st.Running,
			"error":   st.Error,
		}
	}

	w.Header().Set("Content-Type", "application/json")

	switch status {
	case "healthy":
		w.WriteHeader(http.StatusOK)
	case "degraded":
		w.WriteHeader(http.StatusOK) // Still 200, but status shows degraded
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	outputs := make(map[string]output.OutputStatus)
	for name, out := range s.provider.Outputs() {
		outputs[name] = out.Status()
	}

	uptime := s.provider.Uptime()
	var uptimeStr string
	if uptime > 0 {
		uptimeStr = formatDuration(uptime)
	}

	response := map[string]interface{}{
		"version":         s.version,
		"input_connected": s.provider.IsInputConnected(),
		"uptime":          uptimeStr,
		"outputs":         outputs,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
