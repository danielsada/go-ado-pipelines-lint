package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
)

func known(id string) bool { return strings.HasPrefix(id, "ADO") }

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`
rules:
  ado401: off
  ADO404: error
ignore: ["vendor/**"]
templates:
  enabled: false
  repoRoot: ..
  lintReferenced: true
  ignoreRepositories: [Infra, Unmapped]
repositories:
  Infra: ../infra
tasks:
  known: [1ES.PublishPipelineArtifact@1, yarn-berry-auth@2]
scripts:
  scriptShell: bash
  shellcheck:
    profile: all
    exclude: [SC2034]
`), filepath.Join("cfg", ".adolint.yaml"), known)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rules["ADO401"] != diag.Off || c.Rules["ADO404"] != diag.Error {
		t.Errorf("rules = %v", c.Rules)
	}
	if c.TemplatesEnabled() || !c.ScriptsEnabled() || c.Scripts.ShellCheck.Profile != "all" {
		t.Errorf("unexpected settings: %+v", c)
	}
	if c.RepoRoot() != filepath.Join("cfg", "..") {
		t.Errorf("RepoRoot = %q", c.RepoRoot())
	}
	if c.RepositoryRoots()["infra"] != filepath.Clean(filepath.Join("cfg", "..", "infra")) || !c.Templates.LintReferenced || len(c.Tasks.Known) != 2 {
		t.Errorf("unexpected template/task configuration: %+v", c)
	}
	if strings.Join(c.Templates.IgnoreRepositories, ",") != "Infra,Unmapped" {
		t.Errorf("ignored repository aliases = %v", c.Templates.IgnoreRepositories)
	}
	if _, err := Parse(nil, ".adolint.yaml", known); err != nil {
		t.Errorf("empty config: %v", err)
	}
}

func TestLimitExpansion(t *testing.T) {
	for src, want := range map[string]ExpansionLimit{"": 0, "false": 0, "true": DefaultExpansionLimit, "0": 0, "500000": 500000} {
		c, err := Parse([]byte("templates:\n  limitExpansion: "+src+"\n"), ".adolint.yaml", known)
		if err != nil || c.Templates.LimitExpansion != want {
			t.Errorf("limitExpansion %q = %d, %v; want %d", src, c.Templates.LimitExpansion, err, want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"rules:\n  FOO1: off\n":                              "unknown rule",
		"rules:\n  ADO401: loud\n":                           "invalid severity",
		"rulez: {}\n":                                        "field rulez not found",
		"scripts:\n  scriptShell: zsh\n":                     "scriptShell",
		"scripts:\n  shellcheck:\n    profile: max\n":        "profile",
		"repositories:\n  self: ../other\n":                  "repository alias",
		"repositories:\n  Infra: a\n  infra: b\n":            "repository alias",
		"repositories:\n  infra: ''\n":                       "checkout path",
		"tasks:\n  known: [Custom]\n":                        "tasks.known",
		"tasks:\n  known: [Custom@1.2]\n":                    "tasks.known",
		"templates:\n  ignoreRepositories: ['']\n":           "ignoreRepositories",
		"templates:\n  ignoreRepositories: [self]\n":         "ignoreRepositories",
		"templates:\n  ignoreRepositories: [Infra, infra]\n": "ignoreRepositories",
		"templates:\n  ignoreRepositories: ['repo/path']\n":  "ignoreRepositories",
		"templates:\n  limitExpansion: -1\n":                 "limitExpansion",
		"templates:\n  limitExpansion: lots\n":               "limitExpansion",
	}
	for src, want := range cases {
		if _, err := Parse([]byte(src), ".adolint.yaml", known); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want error containing %q", src, err, want)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"vendor/**", "vendor/a/b.yml", true},
		{"vendor/**", "src/vendor/a.yml", false},
		{"**/generated/*.yml", "a/b/generated/x.yml", true},
		{"**/generated/*.yml", "generated/x.yml", true},
		{"*.yml", "deep/dir/file.yml", true},
		{"templates/*.yml", "templates/sub/x.yml", false},
		{"templates", "templates/x.yml", true},
		{"file?.yml", "file1.yml", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "repo", "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".adolint.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(sub); got != "" {
		t.Errorf("Find should stop at the repository root, got %q", got)
	}
	want := filepath.Join(root, "repo", "a", ".adolint.yml")
	if err := os.WriteFile(want, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Find(sub); got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}
