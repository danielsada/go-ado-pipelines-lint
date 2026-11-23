---
title: Home
nav_order: 1
permalink: /
---
{% raw %}
# adolint

`adolint` parses and lints Azure DevOps (Azure Pipelines) YAML.

- It expands local templates and compile-time `${{ }}` expressions, the way Azure Pipelines does.
- It validates the result against the [official Azure Pipelines schema](https://github.com/microsoft/azure-pipelines-vscode).
- It runs [40 rules](rules/) covering structure, dependencies, parameters, expressions, conditions and best practices.
- It lints inline bash and PowerShell scripts with [ShellCheck](https://www.shellcheck.net) and [PSScriptAnalyzer](https://learn.microsoft.com/powershell/utility-modules/psscriptanalyzer/overview).

Every finding points at the original file, line and column, even inside templates.

```console
$ adolint lint
azure-pipelines.yml:20:10: error [ADO101] job name "build-linux" is invalid: names may only contain letters, digits and '_' and may not start with a digit
azure-pipelines.yml:24:9: warning [ADO701/SC2164] Use 'cd ... || exit' or 'cd ... || return' in case cd fails.
azure-pipelines.yml:40:14: error [ADO103] stage "Deploy" depends on unknown stage "Biuld"
```

## Install

```sh
go install github.com/danielsada/go-ado-pipelines-lint/cmd/adolint@latest
```

Or download `adolint` from the [releases page](https://github.com/danielsada/go-ado-pipelines-lint/releases). Builds are available for Linux, macOS and Windows on amd64 and arm64.

Script linting is optional. It uses tools on your `PATH`, which are not bundled:

- `shellcheck`
- `pwsh` with the PSScriptAnalyzer module

## Next steps

- [Use in CI](azure-pipelines/): a reusable Azure Pipelines template, copy-paste install steps and GitHub Actions.
- [Usage](usage/): commands, flags and output formats.
- [Configuration](configuration/): `.adolint.yaml` and inline suppressions.
- [Rules](rules/): one page per rule, with examples.
{% endraw %}
