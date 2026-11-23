package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFind(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"azure-pipelines.yml", "azure-pipelines-release.yaml", "docker-compose.yml",
		".azure-pipelines/ci.yml", ".azuredevops/templates/steps.yaml", "svc/azure-pipelines.yml",
		"node_modules/pkg/azure-pipelines.yml", "k8s/deploy.yml",
	} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
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
	want := ".azure-pipelines/ci.yml .azuredevops/templates/steps.yaml azure-pipelines-release.yaml azure-pipelines.yml svc/azure-pipelines.yml"
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
