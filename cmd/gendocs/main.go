// Command gendocs writes the generated rule documentation into a docs directory.
//
//	go run ./cmd/gendocs docs
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danielsada/go-ado-pipelines-lint/internal/docs"
)

func main() {
	dir := "docs"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	pages, err := docs.Generate()
	if err != nil {
		return err
	}
	// Remove stale generated rule pages (for example a rule that was renumbered).
	old, _ := filepath.Glob(filepath.Join(dir, "rules", "*.md"))
	for _, p := range old {
		rel, _ := filepath.Rel(dir, p)
		if _, keep := pages[filepath.ToSlash(rel)]; keep {
			continue
		}
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), docs.Header) {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	for rel, content := range pages {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d pages to %s\n", len(pages), dir)
	return nil
}
