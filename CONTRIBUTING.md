# Contributing

Thank you for considering a contribution to MetricsPanel233. Please open an issue first for substantial behavior changes so the scope and compatibility target can be agreed before implementation.

## Development setup

Use the [development guide](docs/development.md) for prerequisites, the one-command local workspace and validation commands. Read the [project goals](docs/project-goals.md) before expanding Grafana compatibility. The project targets Go 1.27, Node.js 24 and React 19.

## Pull requests

- Keep each pull request focused and explain the user-visible behavior it changes.
- Add or update tests for behavior changes. Include the exact commands and results in the pull request description.
- Update the README or a focused contract page when a public behavior or compatibility claim changes.
- Describe known limitations and the exact compatibility behavior verified. Full Grafana parity is outside the project scope and must not be presented as a release goal.
- For UI changes, include a screenshot or short recording when it helps reviewers understand the visible result.
- Keep built-in UI and renderer code first-party; isolate Grafana SDK use in compatibility adapters.
- Remove local data, credentials, tokens and machine-specific paths from examples and logs.

The CI workflow is the final automated check for a pull request. A passing unit test does not imply Docker, browser or production acceptance unless those checks also ran.
