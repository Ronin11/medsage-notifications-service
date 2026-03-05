package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"compumed/notifications-service/email"
)

type Server struct {
	httpServer  *http.Server
	emailClient *email.Client
}

func NewServer(addr string, emailClient *email.Client) *Server {
	s := &Server{
		emailClient: emailClient,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/notifications/email", s.handleSendEmail)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return s
}

func (s *Server) Start() error {
	slog.Info("HTTP server starting", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleSendEmail(w http.ResponseWriter, r *http.Request) {
	var req email.SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if len(req.To) == 0 {
		http.Error(w, `{"error":"'to' is required"}`, http.StatusBadRequest)
		return
	}
	if req.Subject == "" {
		http.Error(w, `{"error":"'subject' is required"}`, http.StatusBadRequest)
		return
	}
	if req.HTML == "" && req.Text == "" {
		http.Error(w, `{"error":"'html' or 'text' is required"}`, http.StatusBadRequest)
		return
	}

	resp, err := s.emailClient.Send(r.Context(), req)
	if err != nil {
		http.Error(w, `{"error":"failed to send email"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}
