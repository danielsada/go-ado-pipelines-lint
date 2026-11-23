package model

import (
	"testing"

	"github.com/danielsada/go-ado-pipelines-lint/internal/yamlx"
)

func build(t *testing.T, src string) *Pipeline {
	t.Helper()
	s, ds := yamlx.Parse("a.yml", "", []byte(src))
	if len(ds) > 0 {
		t.Fatal(ds)
	}
	return Build(yamlx.NewDoc(s), false)
}

func TestImplicitStageAndJob(t *testing.T) {
	p := build(t, "pool: Default\nsteps:\n- bash: echo\n- task: Bash@3\n  inputs:\n    Script: x\n")
	if len(p.Stages) != 1 || !p.Stages[0].Implicit || len(p.Stages[0].Jobs) != 1 || !p.Stages[0].Jobs[0].Implicit {
		t.Fatalf("unexpected shape: %+v", p.Stages)
	}
	steps := p.AllSteps()
	if len(steps) != 2 || steps[0].Kind != "bash" || steps[1].TaskRef() != "Bash@3" || steps[1].InputStr("script") != "x" {
		t.Errorf("steps = %+v", steps)
	}
	if pool := p.Stages[0].Jobs[0].EffectivePool(); pool == nil || pool.Name != "Default" {
		t.Errorf("pool = %+v", pool)
	}
}

func TestStagesJobsAndDeployments(t *testing.T) {
	p := build(t, `
variables:
  a: 1
stages:
- stage: A
  pool:
    vmImage: ubuntu-latest
  jobs:
  - job: J
    dependsOn: [X, Y]
    strategy:
      matrix:
        leg1:
          imageName: x
    steps:
    - script: echo
  - deployment: D
    pool:
      vmImage: windows-latest
    strategy:
      runOnce:
        preDeploy:
          steps:
          - script: pre
        deploy:
          steps:
          - script: deploy
        on:
          failure:
            steps:
            - script: rollback
- stage: B
  dependsOn: A
  jobs: []
`)
	if len(p.Stages) != 2 || p.Stages[1].DependsOn[0].Name != "A" || !p.Stages[1].HasDependsOn {
		t.Fatalf("stages = %+v", p.Stages)
	}
	j, d := p.Stages[0].Jobs[0], p.Stages[0].Jobs[1]
	if len(j.DependsOn) != 2 || j.EffectivePool().VMImage != "ubuntu-latest" {
		t.Errorf("job = %+v", j)
	}
	if !d.Deployment || len(d.Steps) != 3 || d.EffectivePool().VMImage != "windows-latest" {
		t.Errorf("deployment = %+v (steps %d)", d, len(d.Steps))
	}
	names := p.DeclaredVariableNames()
	if !names["a"] || !names["imagename"] {
		t.Errorf("declared variables = %v", names)
	}
}
