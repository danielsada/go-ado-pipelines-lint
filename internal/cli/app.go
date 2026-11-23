// Package cli implements the adolint command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/rules"
	"github.com/danielsada/go-ado-pipelines-lint/internal/scripts"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitFindings = 1
	ExitError    = 2
)

// App holds the CLI's dependencies so they can be replaced in tests.
type App struct {
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
	// Dir is the working directory ("" means the process working directory).
	Dir string
	// Runner runs external script linters (nil means the real tools on PATH).
	Runner   scripts.Runner
	ReadFile func(string) ([]byte, error)
	Create   func(string) (io.WriteCloser, error)
}

// exitError carries an exit code through cobra.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

// Run executes the CLI with the given arguments and returns the process exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	a.defaults()
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "Error: %v\n", ee.err)
		}
		return ee.code
	}
	_, _ = fmt.Fprintf(a.Stderr, "Error: %v\n", err)
	return ExitError
}

func (a *App) defaults() {
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.ReadFile == nil {
		a.ReadFile = os.ReadFile
	}
	if a.Create == nil {
		a.Create = func(name string) (io.WriteCloser, error) { return os.Create(name) }
	}
	if a.Version == "" {
		a.Version = "dev"
	}
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "adolint",
		Short: "Parse and lint Azure DevOps (Azure Pipelines) YAML",
		Long: `adolint parses Azure Pipelines YAML, expands local templates and compile-time
expressions, validates the result against the official schema and reports lint findings,
including ShellCheck and PSScriptAnalyzer findings for inline scripts.`,
		Version:       a.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(a.lintCmd(), a.parseCmd(), a.extractCmd(), a.rulesCmd(), a.explainCmd(), a.versionCmd())
	return root
}

func (a *App) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), a.Version)
			return err
		},
	}
}

func (a *App) workdir() (string, error) {
	if a.Dir != "" {
		return filepath.Abs(a.Dir)
	}
	return os.Getwd()
}

// repoRoot returns the nearest ancestor of dir containing .git, or dir itself.
func repoRoot(dir string) string {
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return dir
		}
		d = parent
	}
}

// display returns a path relative to dir (slash-separated) when it is inside dir.
func display(dir, abs string) string {
	rel, err := filepath.Rel(dir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

// common holds flags shared by commands that load pipelines.
type common struct {
	configPath  string
	repoRoot    string
	noTemplates bool
}

func (c *common) register(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&c.configPath, "config", "c", "", "config file (default: .adolint.yaml found from the working directory upward)")
	cmd.Flags().StringVar(&c.repoRoot, "repo-root", "", "repository root for template paths starting with / (default: nearest directory containing .git)")
	cmd.Flags().BoolVar(&c.noTemplates, "no-templates", false, "do not expand template files")
}

type session struct {
	dir      string
	root     string
	cfg      *config.Config
	registry *lint.Registry
}

func (a *App) session(c *common) (*session, error) {
	dir, err := a.workdir()
	if err != nil {
		return nil, err
	}
	reg := rules.Registry()
	path := c.configPath
	if path == "" {
		path = config.Find(dir)
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	cfg := config.Default(dir)
	if path != "" {
		if cfg, err = config.Load(path, reg.Has); err != nil {
			return nil, err
		}
	}
	root := c.repoRoot
	switch {
	case root != "" && !filepath.IsAbs(root):
		root = filepath.Join(dir, root)
	case root == "" && cfg.RepoRoot() != "":
		root = cfg.RepoRoot()
	case root == "":
		root = repoRoot(dir)
	}
	return &session{dir: dir, root: root, cfg: cfg, registry: reg}, nil
}

func (a *App) engine(s *session, c *common, extra func(*lint.Options)) (*lint.Engine, error) {
	opts := lint.Options{
		Registry:    s.registry,
		Config:      s.cfg,
		RepoRoot:    s.root,
		NoTemplates: c.noTemplates || !s.cfg.TemplatesEnabled(),
		Runner:      a.Runner,
		ReadFile:    a.ReadFile,
		Display:     func(p string) string { return display(s.dir, p) },
	}
	if opts.Runner == nil {
		opts.Runner = scripts.ExecRunner{Dir: s.dir}
	}
	if extra != nil {
		extra(&opts)
	}
	return lint.New(opts)
}

func (a *App) registryFor() *lint.Registry { return rules.Registry() }

func usageErr(format string, args ...any) error {
	return &exitError{code: ExitError, err: fmt.Errorf(format, args...)}
}
