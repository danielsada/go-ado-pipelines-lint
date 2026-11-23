package rules_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/config"
	"github.com/danielsada/go-ado-pipelines-lint/internal/diag"
	"github.com/danielsada/go-ado-pipelines-lint/internal/lint"
	"github.com/danielsada/go-ado-pipelines-lint/internal/rules"
)

var wantRe = regexp.MustCompile(`#\s*want:\s*((?:ADO\d{3}[ ,]*)+)`)

// TestRuleFixtures runs every testdata/rules/<ID> directory. Each YAML file marks expected
// diagnostics for the rule with a trailing "# want: <ID>" comment on the reported line;
// files without want comments must produce no diagnostics for the rule.
func TestRuleFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "rules")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	reg := rules.Registry()
	covered := map[string]bool{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		id := d.Name()
		covered[id] = true
		t.Run(id, func(t *testing.T) {
			r, ok := reg.Get(id)
			if !ok {
				t.Fatalf("fixture directory for unknown rule %s", id)
			}
			runFixture(t, reg, filepath.Join(root, id), r.Meta())
		})
	}
	for _, r := range reg.All() {
		id := r.Meta().ID
		if !covered[id] && !strings.HasPrefix(id, "ADO70") {
			t.Errorf("rule %s has no fixtures in testdata/rules", id)
		}
	}
}

func runFixture(t *testing.T, reg *lint.Registry, dir string, m *lint.Meta) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	var inputs []lint.Input
	want := map[string]bool{}
	err = filepath.WalkDir(abs, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".yml") {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if !strings.Contains(rel, "/") {
			inputs = append(inputs, lint.Input{Abs: p, Path: rel})
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			if mm := wantRe.FindStringSubmatch(line); mm != nil && strings.Contains(mm[1], m.ID) {
				want[fmt.Sprintf("%s:%d", rel, i+1)] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	eng, err := lint.New(lint.Options{
		Registry:  reg,
		Config:    config.Default(abs),
		Overrides: map[string]diag.Severity{m.ID: m.Severity},
		RepoRoot:  abs,
		NoScripts: true,
		Display: func(p string) string {
			rel, _ := filepath.Rel(abs, p)
			return filepath.ToSlash(rel)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ds, err := eng.Lint(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	var all []string
	for _, d := range ds {
		if d.RuleID == m.ID {
			got[fmt.Sprintf("%s:%d", d.File, d.Line)] = true
			all = append(all, fmt.Sprintf("%s:%d:%d %s", d.File, d.Line, d.Column, d.Message))
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing %s at %s", m.ID, k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected %s at %s", m.ID, k)
		}
	}
	if t.Failed() {
		sort.Strings(all)
		t.Logf("reported:\n  %s", strings.Join(all, "\n  "))
	}
}
