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
