package auth

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/w101723/llm-proxy-lite/internal/config"
	"github.com/w101723/llm-proxy-lite/internal/logging"
)

var authPattern = regexp.MustCompile(`(?i)^(Bearer|x-api-key)\s+(.+)$`)

type ContextKey string

const APIKeyContextKey ContextKey = "client_api_key"

func ExtractIncomingAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("x-api-key")); key != "" {
		return key
	}
	match := authPattern.FindStringSubmatch(strings.TrimSpace(r.Header.Get("authorization")))
	if len(match) == 3 {
		return strings.TrimSpace(match[2])
	}
	return ""
}

func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return key[:1] + "***" + key[len(key)-1:]
	}
	return key[:4] + "***" + key[len(key)-4:]
}

func Valid(cfg config.Config, incomingKey string) bool {
	return incomingKey != "" && (cfg.APIKeyDirect || incomingKey == cfg.ClientAPIKey)
}

func WriteAuthError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`))
}

func Middleware(cfg config.Config, logger *logging.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ExtractIncomingAPIKey(r)
		if !Valid(cfg, key) {
			logger.Warn("Invalid API key: %s", MaskKey(key))
			WriteAuthError(w)
			return
		}
		r.Header.Set("X-LLM-Proxy-Client-Key", key)
		next.ServeHTTP(w, r)
	})
}

func UpstreamAPIKey(cfg config.Config, r *http.Request) string {
	if cfg.APIKeyDirect {
		return r.Header.Get("X-LLM-Proxy-Client-Key")
	}
	return cfg.OpenAIAPIKey
}
