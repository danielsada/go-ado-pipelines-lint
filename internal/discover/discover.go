// Package discover finds Azure Pipelines YAML files.
package discover

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var skipDirs = map[string]bool{".git": true, ".github": true, "node_modules": true, "vendor": true, ".terraform": true}

// stepKeys identify a step mapping.
var stepKeys = []string{"task", "script", "bash", "pwsh", "powershell", "checkout", "download",
	"downloadBuild", "getPackage", "publish", "upload", "reviewApp", "template"}

func isYAML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

// IsPipelineName reports whether a file name looks like a pipeline (azure-pipelines*.yml).
func IsPipelineName(name string) bool {
	return isYAML(name) && strings.HasPrefix(strings.ToLower(name), "azure-pipelines")
}

// LooksLikePipeline reports whether YAML content looks like an Azure Pipelines file (a pipeline or a
// stages/jobs/steps template): it extends a template, or has stages, jobs or steps lists whose items
// are stages, jobs or steps. GitHub Actions workflows (jobs is a mapping), GitLab CI (stages is a list
// of names) and other YAML don't match.
func LooksLikePipeline(data []byte) bool {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		switch k {
		case "extends":
			if hasKey(v, "template") {
				return true
			}
		case "stages":
			if itemsWith(v, "stage", "template") {
				return true
			}
		case "jobs":
			if itemsWith(v, "job", "deployment", "template") {
				return true
			}
		case "steps":
			if itemsWith(v, stepKeys...) {
				return true
			}
		}
	}
	return false
}

func hasKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// itemsWith reports whether a sequence has a mapping item with one of the keys (directive items count too).
func itemsWith(seq *yaml.Node, keys ...string) bool {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return false
	}
	for _, it := range seq.Content {
		if it.Kind != yaml.MappingNode || len(it.Content) == 0 {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(it.Content[0].Value), "${{") {
			return true
		}
		for _, k := range keys {
			if hasKey(it, k) {
				return true
			}
		}
	}
	return false
}

// Find returns the absolute paths of pipeline files for the given arguments. Files are
// returned as-is; directories are searched for azure-pipelines*.yml files and for YAML files
// whose content looks like a pipeline or template. With no arguments, dir is searched.
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
		if !isYAML(d.Name()) {
			return nil
		}
		if IsPipelineName(d.Name()) {
			out = append(out, p)
			return nil
		}
		if data, err := os.ReadFile(p); err == nil && LooksLikePipeline(data) {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
