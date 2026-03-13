package api

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"strings"

	"medsage/notifications-service/email"
)

type Server struct {
	httpServer     *http.Server
	emailClient    *email.Client
	contactTo      string
	allowedOrigins string
}

func NewServer(addr string, emailClient *email.Client, contactTo, allowedOrigins string) *Server {
	s := &Server{
		emailClient:    emailClient,
		contactTo:      contactTo,
		allowedOrigins: allowedOrigins,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/notifications/email", s.cors(s.handleSendEmail))
	mux.HandleFunc("POST /api/notifications/contact", s.cors(s.handleContact))
	mux.HandleFunc("OPTIONS /api/notifications/", s.cors(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return s
}

func (s *Server) cors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.allowedOrigins)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		next(w, r)
	}
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

type contactRequest struct {
	Name    string `json:"name"`
	Email   string `json:"email"`
	Subject string `json:"subject"`
	Message string `json:"message"`
}

var subjectLabels = map[string]string{
	"general":     "General Inquiry",
	"demo":        "Demo Request",
	"order":       "Ordering / Waitlist",
	"support":     "Technical Support",
	"partnership": "Partnership",
}

func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	var req contactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Email == "" || req.Message == "" {
		http.Error(w, `{"error":"name, email, and message are required"}`, http.StatusBadRequest)
		return
	}

	subjectLabel := subjectLabels[req.Subject]
	if subjectLabel == "" {
		subjectLabel = "General Inquiry"
	}

	emailSubject := fmt.Sprintf("[Medsage Contact] %s from %s", subjectLabel, req.Name)

	escapedName := html.EscapeString(req.Name)
	escapedEmail := html.EscapeString(req.Email)
	escapedMessage := html.EscapeString(req.Message)
	escapedMessage = strings.ReplaceAll(escapedMessage, "\n", "<br>")

	body := fmt.Sprintf(`<h2>New Contact Form Submission</h2>
<p><strong>Name:</strong> %s</p>
<p><strong>Email:</strong> <a href="mailto:%s">%s</a></p>
<p><strong>Subject:</strong> %s</p>
<hr>
<p>%s</p>`, escapedName, escapedEmail, escapedEmail, html.EscapeString(subjectLabel), escapedMessage)

	resp, err := s.emailClient.Send(r.Context(), email.SendRequest{
		To:      []string{s.contactTo},
		Subject: emailSubject,
		HTML:    body,
	})
	if err != nil {
		http.Error(w, `{"error":"failed to send message"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
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
