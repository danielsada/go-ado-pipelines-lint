---
title: Usage
nav_order: 2
permalink: /usage/
---
{% raw %}
# Usage

```sh
adolint lint                          # discover and lint pipelines under the current directory
adolint lint ci/azure-pipelines.yml   # lint specific files or directories
adolint lint -f sarif -o adolint.sarif
adolint parse --expand azure-pipelines.yml          # print the template-expanded pipeline
adolint extract azure-pipelines.yml --out scripts/  # dump inline scripts plus manifest.json
adolint rules                         # list rules
adolint explain ADO603                # describe a rule, with examples
adolint version
```

## Discovery

Without arguments, `lint` searches the working directory for:

- files named `azure-pipelines*.yml` or `azure-pipelines*.yaml`;
- any other YAML file that looks like a pipeline or template, meaning it has `extends`, or `stages`/`jobs`/`steps` lists of stages, jobs or steps.

GitHub Actions workflows, GitLab CI files, Kubernetes manifests and similar YAML are skipped.

Files are linted as templates, not as standalone pipelines, if either of these is true:

- another file references them, for example `template: x.yml`, or any value naming the file;
- they look like a template. That means they have one of `stages`/`jobs`/`steps` (or only `variables`), no `trigger` or `pool`, and they either declare or use `parameters`, or sit under a directory whose name contains `template`.

## `lint` flags

| Flag | Description |
| --- | --- |
| `-f, --format text\|json\|sarif` | Output format (default `text`). |
| `-o, --output FILE` | Write the report to a file. |
| `-c, --config FILE` | Config file. The default is `.adolint.yaml` or `.adolint.yml`, searched for up to the repository root. |
| `--enable IDS` / `--disable IDS` | Enable (at the default severity) or disable rules, as a comma-separated list. |
| `--fail-on error\|warning\|info\|none` | Lowest severity that makes the exit code 1 (default `warning`). |
| `--no-templates` | Don't expand template files. |
| `--no-scripts` | Don't run ShellCheck or PSScriptAnalyzer. |
| `--require-script-linters` | Exit with code 2 if a needed script linter is missing. |
| `--repo-root DIR` | Root for template paths that start with `/` and for `@self` paths (default: the nearest directory containing `.git`). |

The exit code is `0` when there are no findings at or above `--fail-on`. It is `1` when there are such findings, and `2` on a usage, I/O or configuration error.

## Output formats

- **text**: `file:line:col: severity [RULE] message`, then a summary line.
- **json**: `{"version", "summary", "diagnostics": [{"ruleId", "subRule", "name", "severity", "message", "file", "line", "column", "endLine", "endColumn"}]}`.
- **sarif**: [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html), with URIs relative to the repository root. Each rule's `helpUri` links to its page on this site.

### GitHub code scanning

```yaml
- run: adolint lint -f sarif -o adolint.sarif --fail-on none
- uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: adolint.sarif
```

### Azure Pipelines

```yaml
- script: adolint lint --format text
  displayName: Lint pipelines
```

## How templates are handled

- **What is expanded:**
  - `- template:` references in stages, jobs, steps and variables lists.
  - `extends`.
  - Typed parameters, with type and `values` checks.
  - `${{ }}` expressions, including inside `$[ ]`.
  - `if`/`elseif`/`else`/`each`/`insert` directives.
- **Templates in other repositories:** `path@alias` references are not expanded (ADO202). Stages, jobs and steps passed as parameters to such an `extends` template are still linted.
- **Old-style parameters:** templates that declare parameters as a mapping, or not at all, accept any parameter, as Azure Pipelines does.
- **Values that can't be known while linting:**
  - Pipeline parameters of type string, number and boolean are treated as unknown, because they can be changed at queue time.
  - When a condition can't be decided, every branch is expanded and marked conditional.
  - YAML scalars are strings, except for typed `boolean`/`number` parameters.
{% endraw %}
