package openai

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/w101723/llm-proxy-lite/internal/auth"
	"github.com/w101723/llm-proxy-lite/internal/config"
	"github.com/w101723/llm-proxy-lite/internal/logging"
)

type Client struct {
	cfg    config.Config
	logger *logging.Logger
	http   *http.Client
}

func NewClient(cfg config.Config, logger *logging.Logger) *Client {
	transport := &http.Transport{DisableKeepAlives: true}
	return &Client{cfg: cfg, logger: logger, http: &http.Client{Transport: transport}}
}

func (c *Client) Do(r *http.Request, path string, body []byte, bodyReader io.Reader, method string, label string) (*http.Response, error) {
	url := c.cfg.OpenAIAPIBase + path
	if method == "" {
		method = r.Method
	}
	if label == "" {
		label = "OpenAI upstream"
	}

	maxRetries := c.cfg.UpstreamRetries
	baseDelay := time.Duration(c.cfg.UpstreamRetryDelay) * time.Millisecond
	attempt := 0
	for {
		var reader io.Reader
		if bodyReader != nil {
			reader = bodyReader
		} else if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, url, reader)
		if err != nil {
			return nil, err
		}
		copyHeaders(req.Header, r.Header)
		req.Header.Set("Authorization", "Bearer "+auth.UpstreamAPIKey(c.cfg, r))

		resp, err := c.http.Do(req)
		if err != nil {
			if bodyReader != nil || attempt >= maxRetries {
				return nil, err
			}
			delay := baseDelay * time.Duration(1<<attempt)
			c.logger.Warn("%s network error (attempt %d/%d), retry in %dms: %s", label, attempt+1, maxRetries+1, delay.Milliseconds(), err.Error())
			time.Sleep(delay)
			attempt++
			continue
		}

		if bodyReader != nil || resp.StatusCode < 500 || attempt >= maxRetries {
			return resp, nil
		}
		errText, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		delay := baseDelay * time.Duration(1<<attempt)
		c.logger.Warn("%s %d (attempt %d/%d), retry in %dms: %s", label, resp.StatusCode, attempt+1, maxRetries+1, delay.Milliseconds(), string(errText))
		time.Sleep(delay)
		attempt++
	}
}

func copyHeaders(dst, src http.Header) {
	for _, name := range []string{"accept", "content-type", "openai-beta", "openai-organization", "openai-project"} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

func PathWithoutV1(original string) string {
	path := strings.TrimPrefix(original, "/v1")
	if path == "" {
		return "/"
	}
	return path
}

func CopyResponseHeaders(dst http.Header, src http.Header) {
	for name, values := range src {
		lower := strings.ToLower(name)
		if lower == "connection" || lower == "transfer-encoding" || lower == "content-encoding" || lower == "content-length" {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func QueryPath(r *http.Request, mappedPath string) string {
	if r.URL.RawQuery == "" {
		return mappedPath
	}
	return fmt.Sprintf("%s?%s", mappedPath, r.URL.RawQuery)
}
