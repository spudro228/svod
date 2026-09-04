// Команда svod-mcp — мост между клиентами MCP и Сводом.
//
// Запускается не человеком, а клиентом вроде Claude Desktop: тот поднимает
// процесс и общается с ним по stdio. Поэтому в stdout нельзя писать ничего,
// кроме протокола, — вся диагностика уходит в stderr.
//
// Настройка в Claude Desktop (claude_desktop_config.json):
//
//	{
//	  "mcpServers": {
//	    "svod": {
//	      "command": "/Users/имя/.local/bin/svod-mcp",
//	      "env": {
//	        "SVOD_SERVER": "https://свод.example.com",
//	        "SVOD_TOKEN": "токен"
//	      }
//	    }
//	  }
//	}
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spudro228/svod/internal/mcp"
)

func main() {
	var (
		server = flag.String("server", env("SVOD_SERVER", "http://localhost:8080"), "адрес свода")
		token  = flag.String("token", os.Getenv("SVOD_TOKEN"), "токен доступа")
	)
	flag.Parse()

	s := &mcp.Server{
		Server: strings.TrimSuffix(*server, "/"),
		Token:  *token,
		HC:     &http.Client{Timeout: 30 * time.Second},
		Log:    os.Stderr,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "svod-mcp: свод %s\n", s.Server)
	if err := s.Run(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "svod-mcp: %v\n", err)
		os.Exit(1)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
