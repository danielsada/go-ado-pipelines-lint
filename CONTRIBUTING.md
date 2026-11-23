# Contributing to go-ado-pipelines-lint

Thanks for your interest in contributing! Here's how to get started.

## Development Setup

1. **Clone the repo:**

   ```bash
   git clone https://github.com/danielsada/go-ado-pipelines-lint.git
   cd go-ado-pipelines-lint
   ```

2. **Install dependencies:**

   - Go 1.23+ — https://go.dev/dl/
   - golangci-lint v2 — `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`
   - Optional, for script-linting integration tests: [ShellCheck](https://github.com/koalaman/shellcheck#installing)
     and PowerShell 7 with `Install-Module PSScriptAnalyzer -Scope CurrentUser`

3. **Verify everything works:**

   ```bash
   make all   # runs lint, vet, test, build
   ```

## Project Layout

| Package | Purpose |
| --- | --- |
| `cmd/adolint` | Entry point (version set via `-ldflags`) |
| `internal/cli` | Cobra commands; `App` holds injectable I/O for tests |
| `internal/yamlx` | YAML loading with positions, duplicate keys, origin tracking for expanded nodes |
| `internal/expr` | Expression lexer, parser, `${{ }}`/`$[ ]`/`$( )` scanner, directives, three-valued evaluator |
| `internal/template` | Template expansion: references, extends, parameters, directives |
| `internal/model` | Typed view of an expanded pipeline (stages, jobs, steps, pools, variables) |
| `internal/schema` | Embedded `service-schema.json` and validator |
| `internal/lint` | Rule interfaces, engine, severity resolution, suppressions |
| `internal/rules` | Built-in rules and their metadata |
| `internal/scripts` | Inline script extraction, placeholders, ShellCheck/PSScriptAnalyzer runners |
| `internal/config` | `.adolint.yaml` loading |
| `internal/report` | text, JSON and SARIF output |

## Making Changes

1. Create a branch from `main`.
2. Make your changes.
3. Run `make all` and ensure lint, vet, tests, and build all pass.
4. Commit with a clear message describing the change.
5. Open a pull request.

### Adding a rule

1. Implement the check in `internal/rules` (a source rule for checks on the original files, a pipeline rule for checks on the expanded model).
2. Register it in `internal/rules/catalog.go` with an ID, name, default severity, summary, description, bad/good example and docs link.
3. Base the rule on the [Azure Pipelines documentation](https://learn.microsoft.com/azure/devops/pipelines/yaml-schema/) or the official schema. Don't guess at service behavior.
4. Add fixtures under `testdata/rules/<ID>/`. Mark each expected diagnostic with a trailing `# want: <ID>` comment on the reported line, and add `ok_*.yml` cases that must stay clean. The harness fails if a registered rule has no fixtures.
5. Make sure `testdata/valid/` still produces no errors or warnings.
6. Run `make docs` to regenerate the rule pages in `docs/rules/` (`go test ./internal/docs` fails if they are stale), and update the rules table in `README.md`.

### Documentation site

`docs/` is a Jekyll site (just-the-docs theme) published to GitHub Pages by `.github/workflows/pages.yml` on pushes to `main`. Rule pages are generated; `index.md`, `usage.md` and `configuration.md` are written by hand. Enable it once under **Settings → Pages → Source: GitHub Actions**.

## Code Style

- Follow standard Go conventions (`gofmt`, `goimports`).
- All exported types, functions, and constants must have doc comments.
- Check all returned errors (enforced by `errcheck` linter).
- Keep functions under a cyclomatic complexity of 50, files under 1000 lines and control nesting at most 4 levels deep (enforced by golangci-lint).
- Keep dependencies minimal — prefer stdlib over external packages.

## Testing

- Write tests for any new or changed functionality.
- Use table-driven tests where appropriate.
- Golden files: `go test ./internal/report -update` regenerates report goldens.
- Fuzz the expression parser with `go test ./internal/expr -run '^$' -fuzz FuzzParse`.
- Set `ADOLINT_INTEGRATION=1` to require ShellCheck/PSScriptAnalyzer in the integration test (CI does this).
- Run `make test` to execute the test suite.
- Aim for ≥80% coverage on new code.

## Updating the schema

`make schema-update` downloads the latest `service-schema.json` from
[microsoft/azure-pipelines-vscode](https://github.com/microsoft/azure-pipelines-vscode). Run the tests afterwards;
the valid fixtures catch schema changes that would cause false positives.

## Releasing

Push a `vX.Y.Z` tag on `main`: the Release workflow runs GoReleaser and publishes binaries for Linux, macOS and Windows (amd64 and arm64). The Azure Pipelines template in `templates/adolint.yml` installs `latest` by default, so it needs no change per release; update the pinned `ref: refs/tags/...` examples in `README.md` and `docs/azure-pipelines.md`.

## Reporting Issues

Open a GitHub issue with:

- A clear description of the problem or feature request.
- Steps to reproduce (for bugs), ideally a minimal pipeline YAML.
- Expected vs actual behavior.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
