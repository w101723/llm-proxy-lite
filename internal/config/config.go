package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port               string
	Host               string
	OpenAIAPIKey       string
	ClientAPIKey       string
	APIKeyDirect       bool
	OpenAIAPIBase      string
	LogLevel           string
	ModelMap           map[string]string
	UpstreamRetries    int
	UpstreamRetryDelay int
}

func Load() (Config, error) {
	cfg := Config{
		Port:               env("PORT", "3000"),
		Host:               env("HOST", "0.0.0.0"),
		OpenAIAPIKey:       os.Getenv("OPENAI_API_KEY"),
		ClientAPIKey:       os.Getenv("CLIENT_API_KEY"),
		APIKeyDirect:       strings.EqualFold(env("API_KEY_DIRECTY", "false"), "true"),
		OpenAIAPIBase:      strings.TrimRight(env("OPENAI_API_BASE", "https://api.openai.com/v1"), "/"),
		LogLevel:           env("LOG_LEVEL", "info"),
		ModelMap:           map[string]string{},
		UpstreamRetries:    intEnv("UPSTREAM_RETRIES", 2),
		UpstreamRetryDelay: intEnv("UPSTREAM_RETRY_DELAY_MS", 300),
	}

	if raw := os.Getenv("MODEL_MAP_JSON"); raw != "" {
		parsed := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			return Config{}, fmt.Errorf("无效的 MODEL_MAP_JSON: %w", err)
		}
		for key, value := range parsed {
			cfg.ModelMap[key] = fmt.Sprint(value)
		}
	}

	if !cfg.APIKeyDirect && cfg.OpenAIAPIKey == "" {
		return Config{}, fmt.Errorf("请设置环境变量 OPENAI_API_KEY，或设置 API_KEY_DIRECTY=true 直接透传客户端 API key")
	}
	if !cfg.APIKeyDirect && cfg.ClientAPIKey == "" {
		return Config{}, fmt.Errorf("请设置环境变量 CLIENT_API_KEY（入站鉴权），或设置 API_KEY_DIRECTY=true 直接透传客户端 API key")
	}

	return cfg, nil
}

func env(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func intEnv(name string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}
