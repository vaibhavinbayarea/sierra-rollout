// Command worker runs the rollout workflows and plugin activities.
package main

import (
	"flag"
	"log/slog"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"sierra-rollout/internal/registry"
	"sierra-rollout/internal/workflows"
)

func main() {
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

	w, err := workflows.NewWorker(c, reg)
	if err != nil {
		slog.Error("build worker", "error", err)
		os.Exit(1)
	}
	if err := w.Run(worker.InterruptCh()); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}
