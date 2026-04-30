package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/w101723/llm-proxy-lite/internal/config"
	"github.com/w101723/llm-proxy-lite/internal/httpserver"
	"github.com/w101723/llm-proxy-lite/internal/logging"
)

func main() {
	cfg, err := config.Load()
	logger := logging.New("info")
	if err != nil {
		logger.Error("%s", err.Error())
		os.Exit(1)
	}
	logger = logging.New(cfg.LogLevel)
	addr := cfg.Host + ":" + cfg.Port
	fmt.Print(`
══════════════════════════════════════════════════════
                 llm-proxy-lite ✅
══════════════════════════════════════════════════════
`)
	if err := http.ListenAndServe(addr, httpserver.NewRouter(cfg, logger)); err != nil {
		logger.Error("server failed: %s", err.Error())
		os.Exit(1)
	}
}
