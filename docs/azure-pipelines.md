---
title: Use in CI
nav_order: 2
permalink: /azure-pipelines/
---
{% raw %}
# Use adolint in CI

adolint is a single binary. Each release publishes builds for Linux, macOS and Windows on amd64 and arm64 on [GitHub Releases](https://github.com/danielsada/go-ado-pipelines-lint/releases).

ShellCheck and PSScriptAnalyzer are used for inline scripts when they're on the agent. Microsoft-hosted Ubuntu images normally include them. If they're missing, you get one info message and everything else is still linted. Pass `--require-script-linters` to make that an error.

## Azure Pipelines: reusable template

The repository ships a steps template, [`templates/adolint.yml`](https://github.com/danielsada/go-ado-pipelines-lint/blob/main/templates/adolint.yml). It installs adolint on Linux, macOS or Windows agents and then runs it. Referencing a GitHub repository needs a [GitHub service connection](https://learn.microsoft.com/azure/devops/pipelines/library/service-endpoints#github-service-connection) in your project.

```yaml
resources:
  repositories:
  - repository: adolint
    type: github
    endpoint: my-github-connection      # your GitHub service connection
    name: danielsada/go-ado-pipelines-lint
    ref: refs/tags/v0.1.2               # pin the template version

steps:
- checkout: self
- template: templates/adolint.yml@adolint
  parameters:
    version: latest                    # or pin a release, e.g. 0.1.2
    arguments: lint --fail-on error    # any adolint arguments
    sarif: true                        # also publish CodeAnalysisLogs/adolint.sarif
```

| Parameter | Default | Description |
| --- | --- | --- |
| `version` | `latest` | Release to install, e.g. `0.1.2`. Pin it for reproducible builds. |
| `arguments` | `lint` | Arguments passed to `adolint`. |
| `workingDirectory` | `$(Build.SourcesDirectory)` | Directory adolint runs in. |
| `sarif` | `false` | Also write a SARIF report and publish it as the `CodeAnalysisLogs` artifact. The [SARIF SAST Scans Tab](https://marketplace.visualstudio.com/items?itemName=sariftools.scans) extension shows that artifact on the build page. |

## Azure Pipelines: inline steps

If you'd rather not add a service connection, copy this step. It installs the latest release on Linux or macOS agents and lints the repository:

```yaml
steps:
- checkout: self
- bash: |
    set -euo pipefail
    case "$(uname -s)-$(uname -m)" in
      Linux-x86_64) platform=linux_amd64 ;;
      Linux-aarch64) platform=linux_arm64 ;;
      Darwin-x86_64) platform=darwin_amd64 ;;
      Darwin-arm64) platform=darwin_arm64 ;;
    esac
    tag_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/danielsada/go-ado-pipelines-lint/releases/latest)
    version="${tag_url##*/v}"
    curl -fsSL "https://github.com/danielsada/go-ado-pipelines-lint/releases/download/v${version}/adolint_${version}_${platform}.tar.gz" | tar -xz -C "$AGENT_TEMPDIRECTORY"
    "$AGENT_TEMPDIRECTORY/adolint" lint
  displayName: Lint pipelines with adolint
```

On Windows agents, use the PowerShell install step from [`templates/adolint.yml`](https://github.com/danielsada/go-ado-pipelines-lint/blob/main/templates/adolint.yml).

## GitHub Actions

```yaml
jobs:
  adolint:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      security-events: write
    steps:
      - uses: actions/checkout@v7
      - name: Install adolint
        run: |
          gh release download --repo danielsada/go-ado-pipelines-lint --pattern 'adolint_*_linux_amd64.tar.gz' --output - | tar -xz -C "$RUNNER_TEMP"
        env:
          GH_TOKEN: ${{ github.token }}
      - run: "$RUNNER_TEMP/adolint" lint -f sarif -o adolint.sarif --fail-on none
      - uses: github/codeql-action/upload-sarif@v4
        with:
          sarif_file: adolint.sarif
      - run: "$RUNNER_TEMP/adolint" lint --fail-on error
```

## Other ways to install

```sh
go install github.com/danielsada/go-ado-pipelines-lint/cmd/adolint@latest
```

## Choosing what fails the build

- `--fail-on error` fails only on errors.
- The default, `--fail-on warning`, also fails on warnings.
- `--fail-on none` never fails, which suits report-only runs.

Tune rules in `.adolint.yaml` (see [Configuration](../configuration/)), or suppress single findings inline with `# adolint:disable-next-line <ID>`.
{% endraw %}
