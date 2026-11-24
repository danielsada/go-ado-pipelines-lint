// Package config loads .adolint.yaml configuration files.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
)

// FileNames are the config file names searched for, in order.
var FileNames = []string{".adolint.yaml", ".adolint.yml"}

// Tool configures an external script linter.
type Tool struct {
	// Profile selects the base rule set: recommended (default), all or none.
	Profile string   `yaml:"profile"`
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Scripts configures inline script linting.
type Scripts struct {
	Enabled *bool `yaml:"enabled"`
	// ScriptShell controls how plain `script:` steps are treated: auto, bash or skip.
	ScriptShell      string `yaml:"scriptShell"`
	ShellCheck       Tool   `yaml:"shellcheck"`
	PSScriptAnalyzer Tool   `yaml:"psscriptanalyzer"`
}

// Templates configures template expansion.
type Templates struct {
	Enabled            *bool    `yaml:"enabled"`
	RepoRoot           string   `yaml:"repoRoot"`
	LintReferenced     bool     `yaml:"lintReferenced"`
	IgnoreRepositories []string `yaml:"ignoreRepositories"`
}

// Tasks configures task references not covered by the built-in catalog.
type Tasks struct {
	Known []string `yaml:"known"`
}

// Config is a parsed configuration.
type Config struct {
	// Path is the file the config was loaded from ("" for defaults).
	Path string `yaml:"-"`
	// Dir is the directory relative paths in the config are resolved against.
	Dir          string            `yaml:"-"`
	RulesRaw     map[string]string `yaml:"rules"`
	Ignore       []string          `yaml:"ignore"`
	Templates    Templates         `yaml:"templates"`
	Scripts      Scripts           `yaml:"scripts"`
	Repositories map[string]string `yaml:"repositories"`
	Tasks        Tasks             `yaml:"tasks"`
	// Rules holds the parsed per-rule severities.
	Rules map[string]diag.Severity `yaml:"-"`
}

// Default returns the default configuration rooted at dir.
func Default(dir string) *Config {
	return &Config{Dir: dir, Rules: map[string]diag.Severity{}}
}

// Find searches dir and its parents for a config file, stopping after a directory
// that contains .git. It returns "" if none is found.
func Find(dir string) string {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	for {
		for _, name := range FileNames {
			p := filepath.Join(dir, name)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Load reads and validates a config file. knownRule reports whether a rule ID exists.
func Load(path string, knownRule func(string) bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path, knownRule)
}

// Parse parses config file contents.
func Parse(data []byte, path string, knownRule func(string) bool) (*Config, error) {
	c := Default(filepath.Dir(path))
	c.Path = path
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Rules = map[string]diag.Severity{}
	for id, sev := range c.RulesRaw {
		id = strings.ToUpper(strings.TrimSpace(id))
		if knownRule != nil && !knownRule(id) {
			return nil, fmt.Errorf("%s: unknown rule %q", path, id)
		}
		s, err := diag.ParseSeverity(sev)
		if err != nil {
			return nil, fmt.Errorf("%s: rule %s: %w", path, id, err)
		}
		c.Rules[id] = s
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) validate() error {
	aliases := map[string]bool{}
	for alias, path := range c.Repositories {
		key := strings.ToLower(alias)
		if !validRepositoryAlias(alias) || aliases[key] {
			return fmt.Errorf("invalid or duplicate repository alias %q", alias)
		}
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("repository %q must have a local checkout path", alias)
		}
		aliases[key] = true
	}
	ignored := map[string]bool{}
	for _, alias := range c.Templates.IgnoreRepositories {
		key := strings.ToLower(alias)
		if !validRepositoryAlias(alias) || ignored[key] {
			return fmt.Errorf("templates.ignoreRepositories: invalid or duplicate repository alias %q", alias)
		}
		ignored[key] = true
	}
	for _, ref := range c.Tasks.Known {
		if !taskRefRe.MatchString(ref) {
			return fmt.Errorf("tasks.known entry %q must be Name@major or Name@major.minor.patch", ref)
		}
	}
	switch c.Scripts.ScriptShell {
	case "", "auto", "bash", "skip":
	default:
		return fmt.Errorf("scripts.scriptShell must be auto, bash or skip, got %q", c.Scripts.ScriptShell)
	}
	for name, t := range map[string]Tool{"shellcheck": c.Scripts.ShellCheck, "psscriptanalyzer": c.Scripts.PSScriptAnalyzer} {
		switch t.Profile {
		case "", "recommended", "all", "none":
		default:
			return fmt.Errorf("scripts.%s.profile must be recommended, all or none, got %q", name, t.Profile)
		}
	}
	for _, g := range c.Ignore {
		if _, err := globRegexp(g); err != nil {
			return fmt.Errorf("invalid ignore pattern %q: %w", g, err)
		}
	}
	return nil
}

var taskRefRe = regexp.MustCompile(`^[^@\s]+@[0-9]+(?:\.[0-9]+\.[0-9]+)?$`)

func validRepositoryAlias(alias string) bool {
	return strings.TrimSpace(alias) != "" && !strings.ContainsAny(alias, "@/\\ \t\r\n") && !strings.EqualFold(alias, "self")
}

// RepositoryRoots returns case-insensitive aliases resolved against the config directory.
func (c *Config) RepositoryRoots() map[string]string {
	roots := map[string]string{}
	for alias, root := range c.Repositories {
		if !filepath.IsAbs(root) {
			root = filepath.Join(c.Dir, root)
		}
		roots[strings.ToLower(alias)] = filepath.Clean(root)
	}
	return roots
}

// TemplatesEnabled reports whether template expansion is enabled.
func (c *Config) TemplatesEnabled() bool { return c.Templates.Enabled == nil || *c.Templates.Enabled }

// ScriptsEnabled reports whether inline script linting is enabled.
func (c *Config) ScriptsEnabled() bool { return c.Scripts.Enabled == nil || *c.Scripts.Enabled }

// RepoRoot returns the configured repository root, resolved against the config directory.
func (c *Config) RepoRoot() string {
	if c.Templates.RepoRoot == "" {
		return ""
	}
	if filepath.IsAbs(c.Templates.RepoRoot) {
		return c.Templates.RepoRoot
	}
	return filepath.Join(c.Dir, c.Templates.RepoRoot)
}

// Ignored reports whether a path (relative to the config directory, slash-separated) is ignored.
func (c *Config) Ignored(rel string) bool {
	rel = filepath.ToSlash(rel)
	for _, g := range c.Ignore {
		if MatchGlob(g, rel) {
			return true
		}
	}
	return false
}

// MatchGlob matches a slash-separated path against a glob supporting *, ? and **.
// Patterns without a slash match the base name at any depth.
func MatchGlob(pattern, path string) bool {
	re, err := globRegexp(pattern)
	if err != nil {
		return false
	}
	if re.MatchString(path) {
		return true
	}
	if !strings.Contains(strings.TrimSuffix(pattern, "/"), "/") {
		return re.MatchString(filepath.Base(filepath.FromSlash(path)))
	}
	return false
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("(?:/.*)?$")
	return regexp.Compile(b.String())
}
