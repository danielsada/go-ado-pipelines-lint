// Command adolint parses and lints Azure DevOps pipeline YAML.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"

	"github.com/danielsada/go-ado-pipelines-lint/internal/cli"
)

// Set via -ldflags at build time by GoReleaser.
var version = "dev"

// buildVersion returns the ldflags version, or the module version recorded by `go install module@vX`.
func buildVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	app := &cli.App{Version: buildVersion(), Stdout: os.Stdout, Stderr: os.Stderr}
	code := app.Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
