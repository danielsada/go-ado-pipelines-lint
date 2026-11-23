package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	dir := t.TempDir()
	pipeline := "trigger: none\nsteps:\n- script: echo\n"
	files := map[string]string{
		"azure-pipelines.yml":                  pipeline,
		"azure-pipelines-release.yaml":         "",
		"eng/pipelines/ci.yml":                 "extends:\n  template: v1/t.yml@tpl\n",
		".ado/templates/steps.yaml":            "parameters:\n  a: b\nsteps:\n- ${{ if true }}:\n  - bash: echo\n",
		"build/jobs.yml":                       "jobs:\n- job: A\n",
		".azuredevops/dependabot.yml":          "version: 2\nupdates: []\n",
		".github/workflows/ci.yml":             "on: push\njobs:\n  build:\n    steps:\n    - run: echo\n",
		".gitlab-ci.yml":                       "stages: [build, test]\n",
		"node_modules/pkg/azure-pipelines.yml": pipeline,
		"k8s/deploy.yml":                       "apiVersion: v1\nkind: Pod\n",
		"broken.yml":                           "a: b: c\n",
	}
	for f, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Find(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rels []string
	for _, g := range got {
		r, _ := filepath.Rel(dir, g)
		rels = append(rels, filepath.ToSlash(r))
	}
	want := ".ado/templates/steps.yaml azure-pipelines-release.yaml azure-pipelines.yml build/jobs.yml eng/pipelines/ci.yml"
	if strings.Join(rels, " ") != want {
		t.Errorf("Find = %v", rels)
	}
	got, err = Find(dir, []string{"k8s/deploy.yml", "azure-pipelines.yml", "azure-pipelines.yml"})
	if err != nil || len(got) != 2 {
		t.Errorf("explicit files = %v, %v", got, err)
	}
	if _, err := Find(dir, []string{"missing.yml"}); err == nil {
		t.Error("expected error for a missing file")
	}
}
