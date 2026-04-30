package httpserver

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/w101723/llm-proxy-lite/internal/anthropic"
	"github.com/w101723/llm-proxy-lite/internal/auth"
	"github.com/w101723/llm-proxy-lite/internal/config"
	"github.com/w101723/llm-proxy-lite/internal/logging"
	"github.com/w101723/llm-proxy-lite/internal/openai"
)

func NewRouter(cfg config.Config, logger *logging.Logger) http.Handler {
	client := openai.NewClient(cfg, logger)
	anthropicHandler := anthropic.NewHandler(cfg, logger, client)
	passthrough := openai.NewPassthrough(client, logger)
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			logger.Warn("404: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "Not found"})
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "uptime": time.Since(startTime).Seconds()})
	})

	authenticated := func(handler http.Handler) http.Handler {
		return auth.Middleware(cfg, logger, handler)
	}
	mux.Handle("/v1/messages", authenticated(http.HandlerFunc(anthropicHandler.Messages)))
	mux.Handle("/messages", authenticated(http.HandlerFunc(anthropicHandler.Messages)))
	mux.Handle("/v1/messages/count_tokens", authenticated(http.HandlerFunc(anthropicHandler.CountTokens)))
	mux.Handle("/messages/count_tokens", authenticated(http.HandlerFunc(anthropicHandler.CountTokens)))
	mux.Handle("/v1/messages/batches", authenticated(anthropic.NotSupported("Anthropic Message Batches are not supported by this OpenAI-backed proxy.")))
	mux.Handle("/v1/messages/batches/", authenticated(anthropic.NotSupported("Anthropic Message Batches are not supported by this OpenAI-backed proxy.")))
	mux.Handle("/v1/complete", authenticated(anthropic.NotSupported("Legacy /v1/complete is not supported. Use /v1/messages.")))
	mux.Handle("/v1/", authenticated(passthrough))
	mux.Handle("/chat/completions", authenticated(passthrough))
	mux.Handle("/models", authenticated(passthrough))
	return mux
}

var startTime = time.Now()
