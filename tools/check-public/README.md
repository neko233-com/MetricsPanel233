# Public publication gate

Read-only, standard-library-only Go 1.23 tool; Git must support `--no-lazy-fetch`.
From the parent checkout:

```sh
go run ./tools/check-public/main.go --root . --require-submodules --history --allow-synthetic-test-ips
```

Success is silent (exit 0). Findings/failures are `path:line:rule` only (exit 1); matching values, Git error text and
file contents are never printed. Quoted paths escape control characters. Paths containing a deny string or token
are redacted. No network operations, Git mutations, index refresh, file rewriting or secret validity checks run.

The parent, `metrics233-server`, `metrics233-lib-go` and `metrics-lib-rust` must be independent Git worktrees. Each uses its own staged
inventory and unignored untracked files. Both staged blobs and working files are scanned: cleaning a working file
does not hide a secret still in the index. Ignored local files are outside the proposed publication set, but tracked
ignored files remain checked. Unexpected gitlinks, symlinks and unsafe paths fail closed. Missing repositories,
objects or requested main history also fail; shallow history is rejected. Files above 32 MiB require review rather
than being silently skipped.

The gate rejects `.env`/`.env.*` (except exact `.env.example`), private-key/database container filenames and private
deploy directories. `.env.example` contents receive every content rule. Rules cover GitHub token shapes, AWS key
shapes, PEM private-key markers and RFC1918 HTTP(S) URLs. Loopback and documentation address ranges are allowed.
`--allow-synthetic-test-ips` explicitly waives **only** the URL rule in `*_test.go` and `testdata`; secret and private
deny rules apply everywhere. It is not a general permission to publish private test configuration.

To reject actual private domains/hosts or other private strings, keep one literal per line in an **external UTF-8
file** and supply only its path:

```sh
go run ./tools/check-public/main.go --root . --deny-file /outside/public/tree/deny.txt --require-submodules --history --allow-synthetic-test-ips
```

Empty lines and `#` comments are ignored. UTF-8 BOM and CRLF are accepted; matches are case-insensitive. The deny
file must resolve outside the parent tree and contain 1–1,024 entries (at most 1 MiB, 4,096 bytes per entry).
Findings identify only `deny#N`;
its values are never printed or added to a public config. Never put private values in command arguments.

`.gitmodules` permits exactly these three canonical sections, paths and HTTPS URLs, with no extra directives:

```ini
[submodule "metrics233-server"]
    path = metrics233-server
    url = https://github.com/neko233-com/metrics233-server.git
[submodule "metrics233-lib-go"]
    path = metrics233-lib-go
    url = https://github.com/neko233-com/metrics233-lib-go.git
[submodule "metrics-lib-rust"]
    path = metrics-lib-rust
    url = https://github.com/neko233-com/metrics-lib-rust.git
```

`--require-submodules` additionally requires all parent gitlinks to match the checked-out child HEADs.
`--history` scans every tree/blob and commit message reachable from each local `main` (or `origin/main` in a
detached checkout without a local branch), including removed files; grafted/shallow histories are rejected.
Text/config sources must be UTF-8 without literal NUL bytes. Git replacement objects and lazy object fetching
are disabled, as are filesystem monitor hooks. Historical findings include a commit suffix.
Other refs, unreachable objects, compressed archive contents,
encoded/constructed secrets and unknown provider formats require separate review. This is a focused publication
gate, not proof that arbitrary sensitive information is absent.

```sh
cd tools/check-public
go test ./... -count=1
go vet ./...
```

Tests create isolated temporary Git fixtures with constructed synthetic patterns; no public checkout is mutated.
