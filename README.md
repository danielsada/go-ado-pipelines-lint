# go-ado-pipelines-lint

`adolint` is a CLI that parses and lints Azure DevOps (Azure Pipelines) YAML.

**Documentation, with a page for every rule: <https://danielsada.github.io/go-ado-pipelines-lint/>**

It does more than a schema check. adolint expands local templates and compile-time `${{ }}` expressions, validates the result against the [official Azure Pipelines schema](https://github.com/microsoft/azure-pipelines-vscode), and runs lint rules. The rules cover structure, dependencies, parameters, expressions, conditions and best practices. Inline bash and PowerShell scripts are linted with [ShellCheck](https://www.shellcheck.net) and [PSScriptAnalyzer](https://learn.microsoft.com/powershell/utility-modules/psscriptanalyzer/overview), and every finding points back to the original line and column in your YAML, including inside templates.

```console
$ adolint lint
azure-pipelines.yml:20:10: error [ADO101] job name "build-linux" is invalid: names may only contain letters, digits and '_' and may not start with a digit
azure-pipelines.yml:24:9: warning [ADO701/SC2164] Use 'cd ... || exit' or 'cd ... || return' in case cd fails.
azure-pipelines.yml:33:7: warning [ADO602] ${{ if }} condition is always false for every allowed value of parameters.env; its branch is never included
azure-pipelines.yml:37:17: error [ADO205] parameter "config" of template "templates/test.yml" must be one of Debug, Release, got "Relese"
azure-pipelines.yml:40:14: error [ADO103] stage "Deploy" depends on unknown stage "Biuld"
azure-pipelines.yml:41:14: warning [ADO603] condition does not check status, so the stage runs even if earlier work failed (a custom condition replaces the default succeeded()); use and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))
...
```

## Install

```sh
go install github.com/danielsada/go-ado-pipelines-lint/cmd/adolint@latest
```

Or download `adolint` from the [releases page](https://github.com/danielsada/go-ado-pipelines-lint/releases) (Linux, macOS and Windows; amd64 and arm64).

Script linting is optional. It uses tools from your `PATH`, which are not bundled:

- [ShellCheck](https://github.com/koalaman/shellcheck#installing) (`shellcheck`)
- PowerShell 7 (`pwsh`, or Windows PowerShell on Windows) with the PSScriptAnalyzer module: `Install-Module PSScriptAnalyzer -Scope CurrentUser`

## Usage

```sh
adolint lint                          # discover and lint pipelines in the current directory
adolint lint ci/azure-pipelines.yml   # lint specific files or directories
adolint lint -f sarif -o adolint.sarif
adolint parse --expand azure-pipelines.yml   # print the template-expanded pipeline
adolint extract azure-pipelines.yml --out scripts/   # dump inline scripts + manifest.json
adolint rules                         # list rules
adolint explain ADO603                # describe a rule, with examples
adolint version
```

Without arguments, `lint` searches the working directory for `azure-pipelines*.yml` and `azure-pipelines*.yaml` files. It also picks up any other YAML file that looks like a pipeline or template: one with `extends`, or with `stages`/`jobs`/`steps` lists of stages, jobs or steps. A file that another file references (via `template:`, or any value naming it) is linted as part of that pipeline, not on its own. A file that looks like a template is validated as a template. That means it has one of `stages`, `jobs` or `steps` (or only `variables`), no pipeline-only keys such as `trigger` or `pool`, and it either declares or uses `parameters`, or sits under a directory whose name contains `template`.

### `lint` flags

| Flag | Description |
| --- | --- |
| `-f, --format text\|json\|sarif` | Output format (default `text`). |
| `-o, --output FILE` | Write the report to a file. |
| `-c, --config FILE` | Config file (default: `.adolint.yaml` or `.adolint.yml`, searched from the working directory up to the repository root). |
| `--enable IDS` / `--disable IDS` | Enable (at default severity) or disable rules, comma-separated. These override the config file. |
| `--fail-on error\|warning\|info\|none` | Lowest severity that makes the exit code 1 (default `warning`). |
| `--no-templates` | Don't expand template files. |
| `--no-scripts` | Don't run ShellCheck or PSScriptAnalyzer. |
| `--require-script-linters` | Exit with code 2 if a needed script linter is missing. |
| `--repo-root DIR` | Root for template paths that start with `/` (default: the nearest directory containing `.git`). |

Exit codes: `0` means no findings at or above `--fail-on`, `1` means findings, `2` means a usage, I/O or configuration error.

### Output formats

- **text**: `file:line:col: severity [RULE] message`, then a summary line.
- **json**: `{"version", "summary": {"error", "warning", "info"}, "diagnostics": [{"ruleId", "subRule", "name", "severity", "message", "file", "line", "column", "endLine", "endColumn"}]}`
- **sarif**: [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html), with URIs relative to the repository root. Upload it to GitHub code scanning or publish it from Azure Pipelines to show findings in PRs:

```yaml
# GitHub Actions
- run: adolint lint -f sarif -o adolint.sarif --fail-on none
- uses: github/codeql-action/upload-sarif@v3
  with:
    sarif_file: adolint.sarif
```

## How parsing works

1. **YAML** is parsed with positions. Syntax errors (ADO001) and duplicate keys (ADO002) are reported first.
2. **Templates** are expanded the way Azure Pipelines does it:
   - It handles `- template:` references in stages, jobs, steps and variables lists, plus `extends`.
   - It binds parameters with type and `values` checks.
   - It evaluates `${{ }}` expressions and `if`/`elseif`/`else`/`each`/`insert` directives.
   - Local paths are resolved relative to the including file, or to the repository root for paths starting with `/`.
   - It applies the documented limits of 100 files and 100 nesting levels, and detects include cycles.
   - Templates from other repositories (`path@alias`) are skipped with an info diagnostic (ADO202). Stages, jobs and steps passed as parameters to such an `extends` template are still linted.
   - `path@self` references are resolved relative to the including file, then from the repository root.
   - Templates that declare parameters in the legacy mapping form, or not at all, accept any parameter.
3. **Unknown values stay unknown**:
   - Pipeline parameters of type `string`, `number` and `boolean` can change at queue time, so they are treated as unknown. Other parameters use their defaults.
   - When a condition can't be decided, every branch is expanded and marked conditional, so the whole pipeline is linted without false duplicate-name errors.
4. **Schema validation** runs on the expanded document using the embedded `service-schema.json` (ADO003 to ADO007). It also checks built-in task names, versions and inputs (ADO501 to ADO503).
5. **Rules** run on both the original files (expressions, conditions, parameters) and the expanded pipeline (structure, dependencies, best practices). Every diagnostic is reported at its original file and position.

### Expressions

adolint implements the [expression language](https://learn.microsoft.com/azure/devops/pipelines/process/expressions): literals, `parameters`/`variables`/`dependencies` access, every documented function with its argument counts, and the documented type-casting rules.

Evaluation is three-valued, so `and(x, false)` is known to be false even when `x` is unknown. This drives the condition rules: ADO601 flags conditions that are always true or always false. ADO602 flags `${{ if }}` branches that are constant for every allowed value of the parameters they use.

### Inline scripts

adolint lints these inline scripts after template expansion:

| Source | Linted with |
| --- | --- |
| `bash:` steps | ShellCheck |
| `script:` steps on `ubuntu*` or `macOS*` pools (or with no pool, which defaults to `ubuntu-latest`) | ShellCheck |
| `Bash@3` (inline) | ShellCheck |
| `AzureCLI@2` with `scriptType: bash` | ShellCheck |
| `pwsh:` and `powershell:` steps | PSScriptAnalyzer |
| `PowerShell@2` (inline), `AzurePowerShell@4`/`@5` (inline) | PSScriptAnalyzer |
| `AzureCLI@2` with `scriptType: ps` or `pscore` | PSScriptAnalyzer |

Before a script is linted, Azure Pipelines syntax is replaced with **same-length placeholders** so every column stays put:

- `${{ ... }}` and `$(Macro.Name)` macros become underscores. In PowerShell expression positions, such as `.AddDays(${{ parameters.days }})`, the placeholder starts with `$` so it parses as a variable.
- A bare `$(name)` is treated as a macro unless it is a common command such as `$(pwd)` or `$(date)`, because variables also come from variable groups and the pipeline UI. Command substitutions with arguments, like `$(git rev-parse HEAD)`, are left alone.

Findings are reported as `ADO701/SCxxxx` or `ADO702/RuleName` at the exact YAML line and column. ShellCheck severities are kept. For PSScriptAnalyzer, parse errors are errors, other Error and Warning findings are warnings, and Information findings are info. A severity configured for the rule overrides these. Run `adolint extract` to see exactly what is linted.

The default `recommended` profile is a curated set of CI-relevant checks:

- **ShellCheck**: every SC1xxx parse error, plus SC2086, SC2046, SC2164, SC2155, SC2181 and SC2034. SC2154, SC1090 and SC1091 are skipped because they misfire on agent-provided variables and files.
- **PSScriptAnalyzer**: parse errors, plus PSAvoidUsingPlainTextForPassword, PSAvoidUsingConvertToSecureStringWithPlainText, PSAvoidUsingInvokeExpression, PSUseDeclaredVarsMoreThanAssignments, PSAvoidUsingEmptyCatchBlock and PSPossibleIncorrectComparisonWithNull.

Use `profile: all` or `profile: none`, or `include` and `exclude` lists, to change this. Tool-native suppressions inside scripts (`# shellcheck disable=SC2086`) keep working.

## Configuration

`.adolint.yaml` is found by searching from the working directory upward until a directory containing `.git`:

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

Precedence is: built-in defaults, then the config file, then `--enable`/`--disable`.

### Inline suppressions

```yaml
pool:
  vmImage: ubuntu-22.04 # adolint:disable-line ADO404
steps:
# adolint:disable-next-line ADO603
- script: ./cleanup.sh
  condition: eq(variables['Build.Reason'], 'Schedule')
```

- `# adolint:disable-file ADO401` applies to the whole file.
- Omit the IDs to suppress all rules.
- `ADO701/SC2086`-style IDs target a single script finding.

## Rules

Each rule has a page with details, examples and configuration at `https://danielsada.github.io/go-ado-pipelines-lint/rules/<ID>/`, and `adolint explain <ID>` shows the same in a terminal.

| ID | Name | Default | Summary |
| --- | --- | --- | --- |
| ADO001 | `yaml-syntax` | error | The file is not valid YAML. |
| ADO002 | `duplicate-key` | error | A mapping defines the same key twice. |
| ADO003 | `unknown-property` | error | A property is not allowed by the Azure Pipelines schema. |
| ADO004 | `invalid-root` | error | The pipeline root mixes stages, jobs, steps or extends. |
| ADO005 | `missing-required` | error | A required property is missing. |
| ADO006 | `invalid-value` | error | A value has the wrong type or is not an allowed value. |
| ADO007 | `deprecated-syntax` | warning | Deprecated syntax or a deprecated task version is used. |
| ADO101 | `invalid-identifier` | error | A stage or job name is not a valid identifier. |
| ADO102 | `duplicate-name` | error | Two stages, jobs in a stage, or steps in a job share a name. |
| ADO103 | `unknown-dependency` | error | dependsOn references a stage or job that doesn't exist. |
| ADO104 | `dependency-cycle` | error | Stages or jobs depend on each other in a cycle. |
| ADO105 | `task-version-missing` | error | A task reference has no major version. |
| ADO201 | `invalid-template-reference` | error | A local template can't be found or doesn't fit where it is used. |
| ADO202 | `template-not-resolved` | info | A template wasn't expanded, so its contents aren't linted. |
| ADO203 | `unexpected-parameter` | error | A parameter is passed that the template doesn't declare. |
| ADO204 | `missing-parameter` | error | A required template parameter is not passed. |
| ADO205 | `parameter-value-invalid` | error | A parameter value doesn't match the declared type or allowed values. |
| ADO206 | `undeclared-parameter-reference` | warning | An expression references a parameter the file doesn't declare. |
| ADO207 | `unused-parameter` | info | A declared parameter is never used. |
| ADO208 | `template-limits` | error | Template includes form a cycle or exceed documented limits. |
| ADO301 | `expression-syntax` | error | An expression or template directive is malformed. |
| ADO302 | `invalid-function` | error | An unknown function is called, or a function has the wrong number of arguments. |
| ADO303 | `runtime-expression-partial` | warning | A runtime expression $[ ] is mixed with other text. |
| ADO304 | `template-expression-unsupported-location` | warning | ${{ }} is used where template expressions aren't expanded. |
| ADO401 | `implicit-ci-trigger` | warning | No trigger is defined, so CI runs for every branch. |
| ADO402 | `implicit-pr-trigger` | info (off by default) | No pr trigger is defined, so PR validation runs for every branch. |
| ADO403 | `hardcoded-secret` | warning | A secret-looking variable has a literal value. |
| ADO404 | `retired-vm-image` | warning | A Microsoft-hosted image is retired or deprecated. |
| ADO405 | `continue-on-error` | info | continueOnError: true hides failures. |
| ADO501 | `unknown-task` | info | The task isn't a known built-in task or version. |
| ADO502 | `invalid-task-input` | warning | A task input isn't defined for this built-in task version, or has a value outside its allowed list. |
| ADO503 | `missing-task-input` | warning | A required input of a built-in task is missing. |
| ADO601 | `constant-condition` | warning | A condition is always true or always false. |
| ADO602 | `constant-template-if` | warning | A ${{ if }} or ${{ elseif }} is always true or always false. |
| ADO603 | `condition-overrides-succeeded` | warning | A custom condition doesn't check job status. |
| ADO604 | `always-runs-on-cancel` | info | always() also runs when the run is canceled. |
| ADO605 | `dependency-not-declared` | warning | dependencies.X is used without depending on X. |
| ADO701 | `shellcheck` | per finding | ShellCheck finding in an inline bash script. |
| ADO702 | `psscriptanalyzer` | per finding | PSScriptAnalyzer finding in an inline PowerShell script. |
| ADO703 | `script-linter-unavailable` | info | ShellCheck or PSScriptAnalyzer isn't installed, so scripts weren't linted. |

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). Run `make all` to lint, vet, test and build, and `make schema-update` to refresh the embedded schema.

## License

[MIT](LICENSE). The embedded `service-schema.json` comes from [microsoft/azure-pipelines-vscode](https://github.com/microsoft/azure-pipelines-vscode) under the MIT license; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
