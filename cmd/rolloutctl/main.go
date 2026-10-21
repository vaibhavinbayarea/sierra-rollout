// Command rolloutctl plans, starts and operates agent rollouts.
package main

import (
	"os"

	"sierra-rollout/internal/cli"
)

func main() {
	os.Exit(cli.ExitCode(cli.NewRootCmd().Execute()))
}
