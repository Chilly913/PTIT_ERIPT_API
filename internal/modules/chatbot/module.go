// Package chatbot is the public gateway to the Python AI service (PTIT_ERIPT_AI):
//
//	POST /api/chatbot   {message, history?} -> {reply, engine, sources}
//
// The AI service is only reachable server-to-server (X-Internal-Token). Which model answers is
// set with CHATBOT_PROVIDER / CHATBOT_MODEL / CHATBOT_API_KEY (provider "mock" works offline).
package chatbot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/app"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/config"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
)

const (
	maxMessageRunes = 2000
	maxHistory      = 10
	perMinute       = 20 // requests per client IP per minute
)

// Module implements app.Module.
type Module struct {
	ai     config.AIConfig
	log    *slog.Logger
	client *http.Client

	mu   sync.Mutex
	hits map[string]*window
}

type window struct {
	start time.Time
	n     int
}

// New builds the gateway from the app configuration.
func New(a *app.App) *Module {
	return &Module{ai: a.Cfg.AI, log: a.Log, client: &http.Client{Timeout: a.Cfg.AI.Timeout}, hits: map[string]*window{}}
}

// Register mounts the route.
func (m *Module) Register(api *gin.RouterGroup) { api.POST("/chatbot", m.chat) }

type turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Message string `json:"message" binding:"required"`
	History []turn `json:"history"`
}

// allow is a small in-memory per-IP limiter (single instance; use Redis if the API is scaled out).
func (m *Module) allow(ip string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	w := m.hits[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		if len(m.hits) > 10000 {
			m.hits = map[string]*window{}
		}
		m.hits[ip] = &window{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= perMinute
}

func (m *Module) chat(c *gin.Context) {
	if !m.allow(c.ClientIP()) {
		httpx.TooManyRequests(c, 60)
		return
	}
	var req request
	if !httpx.BindJSON(c, &req) {
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" || len([]rune(req.Message)) > maxMessageRunes {
		httpx.Fail(c, httpx.NewValidationError().Add("message", "The message must be between 1 and 2000 characters."))
		return
	}
	hist := make([]map[string]string, 0, maxHistory)
	start := 0
	if len(req.History) > maxHistory {
		start = len(req.History) - maxHistory
	}
	for _, t := range req.History[start:] {
		if (t.Role == "user" || t.Role == "assistant") && strings.TrimSpace(t.Content) != "" {
			hist = append(hist, map[string]string{"role": t.Role, "content": t.Content})
		}
	}

	provider := strings.TrimSpace(os.Getenv("CHATBOT_PROVIDER"))
	if provider == "" {
		provider = "mock"
	}
	payload := map[string]any{
		"message": req.Message, "history": hist, "provider": provider, "model": os.Getenv("CHATBOT_MODEL"),
		"api_key": os.Getenv("CHATBOT_API_KEY"), "use_rag": true,
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(c.Request.Context(), m.ai.Timeout)
	defer cancel()
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, m.ai.URL+"/chat", bytes.NewReader(body))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	hr.Header.Set("Content-Type", "application/json")
	if m.ai.SharedSecret != "" {
		hr.Header.Set("X-Internal-Token", m.ai.SharedSecret)
	}
	res, err := m.client.Do(hr)
	if err != nil {
		m.log.WarnContext(c, "chatbot: ai service unreachable", "error", err.Error())
		httpx.Abort(c, http.StatusServiceUnavailable, "The AI service is unavailable.")
		return
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 400 {
		m.log.WarnContext(c, "chatbot: ai service error", "status", res.StatusCode, "body", string(raw[:min(len(raw), 900)]))
		switch res.StatusCode {
		case http.StatusTooManyRequests:
			httpx.Abort(c, http.StatusTooManyRequests, "Chatbot đang quá tải hoặc đã hết hạn mức. Vui lòng thử lại sau.")
		case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			httpx.Abort(c, res.StatusCode, "Chatbot tạm thời chưa phản hồi. Vui lòng thử lại sau.")
		default:
			httpx.Abort(c, http.StatusBadGateway, "The AI service returned an error.")
		}
		return
	}
	var out struct {
		Reply   string `json:"reply"`
		Engine  string `json:"engine"`
		Sources []any  `json:"sources"`
	}
	if json.Unmarshal(raw, &out) != nil || strings.TrimSpace(out.Reply) == "" {
		httpx.Abort(c, http.StatusBadGateway, "The AI service returned an error.")
		return
	}
	if out.Sources == nil {
		out.Sources = []any{}
	}
	c.JSON(http.StatusOK, out)
}
