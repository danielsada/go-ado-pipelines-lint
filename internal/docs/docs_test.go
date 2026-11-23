package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocsUpToDate fails when the committed docs/rules pages don't match the rule registry.
// Regenerate them with `make docs`.
func TestDocsUpToDate(t *testing.T) {
	pages, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "docs")
	for rel, want := range pages {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || strings.ReplaceAll(string(got), "\r\n", "\n") != want {
			t.Errorf("docs/%s is missing or out of date; run `make docs`", rel)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "rules", "*.md"))
	if len(files) != len(pages) {
		t.Errorf("docs/rules has %d pages, the registry generates %d; run `make docs`", len(files), len(pages))
	}
}

func TestEveryRuleIsDocumented(t *testing.T) {
	pages, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for rel, p := range pages {
		if rel == "rules/index.md" {
			continue
		}
		for _, section := range []string{"## Description", "## Configuration", "| **Default severity** |"} {
			if !strings.Contains(p, section) {
				t.Errorf("%s lacks %q", rel, section)
			}
		}
		if strings.Count(p, "{% raw %}") != 1 || strings.Count(p, "{% endraw %}") != 1 {
			t.Errorf("%s must wrap its content in exactly one raw block", rel)
		}
	}
}
