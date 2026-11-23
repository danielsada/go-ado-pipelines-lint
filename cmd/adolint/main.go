// Command adolint parses and lints Azure DevOps pipeline YAML.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/danielsada/go-ado-pipelines-lint/internal/cli"
)

// Set via -ldflags at build time by GoReleaser.
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	app := &cli.App{Version: version, Stdout: os.Stdout, Stderr: os.Stderr}
	code := app.Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
