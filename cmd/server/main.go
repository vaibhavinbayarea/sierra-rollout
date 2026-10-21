// Command server serves the rollout HTTP API.
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"go.temporal.io/sdk/client"

	"sierra-rollout/internal/api"
	"sierra-rollout/internal/registry"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "HTTP listen address")
	temporal := flag.String("temporal", "127.0.0.1:7233", "Temporal frontend address")
	regPath := flag.String("registry", "plugins.yaml", "plugin registry file")
	flag.Parse()

	reg, err := registry.Load(*regPath)
	if err != nil {
		slog.Error("load registry", "error", err)
		os.Exit(1)
	}
	c, err := client.Dial(client.Options{HostPort: *temporal})
	if err != nil {
		slog.Error("connect to temporal", "error", err)
		os.Exit(1)
	}
	defer c.Close()

	slog.Info("rollout API listening", "addr", *listen)
	if err := http.ListenAndServe(*listen, api.NewServer(c, reg, api.TaskQueue)); err != nil {
		slog.Error("serve", "error", err)
		os.Exit(1)
	}
}
