// Package discover finds Azure Pipelines YAML files.
package discover

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PipelineDirs are directory names whose YAML files are all treated as pipeline files.
var PipelineDirs = []string{".azure-pipelines", ".azuredevops", ".pipelines", "azure-pipelines"}

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".terraform": true}

func isYAML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

// IsPipelineName reports whether a file name looks like a pipeline (azure-pipelines*.yml).
func IsPipelineName(name string) bool {
	return isYAML(name) && strings.HasPrefix(strings.ToLower(name), "azure-pipelines")
}

func inPipelineDir(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		for _, d := range PipelineDirs {
			if strings.EqualFold(part, d) {
				return true
			}
		}
	}
	return false
}

// Find returns the absolute paths of pipeline files for the given arguments. Files are
// returned as-is; directories are searched for azure-pipelines*.yml files and for any YAML
// file inside PipelineDirs. With no arguments, dir is searched.
func Find(dir string, args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{dir}
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, a := range args {
		if !filepath.IsAbs(a) {
			a = filepath.Join(dir, a)
		}
		st, err := os.Stat(a)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			add(filepath.Clean(a))
			continue
		}
		found, err := walk(a)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			add(f)
		}
	}
	return out, nil
}

func walk(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if IsPipelineName(d.Name()) || (isYAML(d.Name()) && inPipelineDir(rel)) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
