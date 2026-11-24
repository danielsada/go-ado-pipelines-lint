---
title: Configuration
nav_order: 5
permalink: /configuration/
---
{% raw %}
# Configuration

adolint looks for `.adolint.yaml` (or `.adolint.yml`) in the working directory, then in each parent directory up to the repository root. You can also pass a file with `--config`.

```yaml
rules:
  ADO401: off          # off | info | warning | error
  ADO404: error
  ADO402: info         # enable a rule that is off by default
ignore:                # globs relative to this file; ** matches any depth
  - "vendor/**"
  - "**/generated/*.yml"
templates:
  enabled: true
  repoRoot: .          # root for template paths starting with "/"
  lintReferenced: false # opt in to checking branches no caller enables
  ignoreRepositories: [] # aliases to skip, e.g. [OneBranch, External]
repositories:          # local checkouts, relative to this config file
  Infra: ../Infra
tasks:
  known: [1ES.PublishPipelineArtifact@1, yarn-berry-auth@2]
scripts:
  enabled: true
  scriptShell: auto    # how to treat `script:` steps: auto | bash | skip
  shellcheck:
    profile: recommended   # recommended | all | none
    include: []
    exclude: [SC2034]
  psscriptanalyzer:
    profile: recommended
```

Settings are applied in this order, with later ones winning:

1. Built-in defaults.
2. The config file.
3. `--enable` and `--disable` on the command line.

## Repository checkouts

`repositories` maps case-insensitive aliases to local checkout roots. `steps/checkout.yml@Infra` is resolved from the mapped checkout root. Inside that template, relative references use its directory and `/` references use its checkout root. `@self` refers to the original pipeline repository.

No repositories are fetched or authenticated. Keep checkouts at the revisions used by your pipeline. Diagnostics retain each template's local file and line numbers. Missing mapped files use ADO201; unmapped aliases use ADO202 (both off by default).

Set `templates.ignoreRepositories: [OneBranch, External]` to skip template references to those aliases entirely. Aliases match case-insensitively and ignore takes precedence over a local checkout mapping. No template files are read or expanded, and no ADO202 notices are emitted for these references, even when the path is dynamic. The remaining pipeline is still checked with the ignored content treated as unknown. Caller-owned stages, jobs and steps passed to an ignored `extends` template are still linted.

This setting only affects `@alias` references, not files supplied directly or found during a directory scan; use `ignore` globs to exclude those files. `self` cannot be ignored.

## Template coverage

By default, referenced templates are linted through their callers. Set `templates.lintReferenced: true` to also lint them independently, with all parameter values unknown. This checks conditional branches no caller enables, including in nested and mapped templates. Duplicate findings are removed. A standalone template's inherited pool is unknown, so plain `script:` steps need an explicit Linux/macOS pool or `scripts.scriptShell: bash` to be checked as bash.

References use the same checkout-relative resolution as expansion; pipeline filenames in trigger or PR path filters do not become template references. Files used by `extends` retain the extends-template schema during independent checks.

Conditional mapping branches are checked in separate views so alternate values of the same key are not lost. Nested alternatives are included; unrelated choices are varied individually rather than as an exhaustive Cartesian product. Per-expansion safety budgets of 100,000 node operations and 16 MiB of generated or evaluated scalar text also cover alternatives. Exceeding either budget aborts that entry's expansion with ADO208.

## Custom tasks

`tasks.known` accepts exact task references, including a major or full version. Matching is case-insensitive. Unknown custom tasks on this list do not produce ADO501, and their inputs are not validated. Built-in tasks continue to use their schema even if listed here. Full version pins such as `Bash@3.250.1` use the known major version's input schema.

## Inline suppressions

```yaml
pool:
  vmImage: ubuntu-22.04 # adolint:disable-line ADO404
steps:
# adolint:disable-next-line ADO603
- script: ./cleanup.sh
  condition: eq(variables['Build.Reason'], 'Schedule')
```

- `# adolint:disable-file ADO401` turns rules off for a whole file.
- Leaving out the IDs suppresses every rule.
- An ID such as `ADO701/SC2086` or `ADO702/PSAvoidUsingInvokeExpression` targets a single script finding. IDs are not case-sensitive.

## How `script:` steps are linted

With `scriptShell: auto`, a `script:` step is linted as bash when its job runs on a Linux or macOS pool. That means one of:

- a `vmImage` starting with `ubuntu` or `macOS`;
- an `os: linux` pool;
- no pool at all, since YAML pipelines default to `ubuntu-latest`.

The step is skipped when:

- its condition targets `Windows_NT`;
- the same template step also runs on a Windows pool, where `script:` runs under cmd.
{% endraw %}
