package rules_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/rules"
)

// TestValidPipelines checks that realistic pipelines produce no errors or warnings.
func TestValidPipelines(t *testing.T) {
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "valid"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []lint.Input
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yml") {
			inputs = append(inputs, lint.Input{Abs: filepath.Join(abs, e.Name()), Path: e.Name()})
		}
	}
	eng, err := lint.New(lint.Options{Registry: rules.Registry(), Config: config.Default(abs), RepoRoot: abs, NoScripts: true,
		Display: func(p string) string { r, _ := filepath.Rel(abs, p); return filepath.ToSlash(r) }})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := eng.Lint(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Severity >= diag.Warning {
			t.Errorf("%s:%d:%d: %s [%s] %s", d.File, d.Line, d.Column, d.Severity, d.FullID(), d.Message)
		}
	}
}
