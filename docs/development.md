# Development

This file is every day. Release day is `docs/release.md`; the design
and its reasons are `docs/architecture.md`.

## The one command

```bash
make hooks   # once: point git at .githooks
make check
```

`make check` is the definition of done, and it is what CI runs.
`make parity` holds the two equal by reading the recipes CI steps call,
and `make checklist` holds `check`'s prerequisites against the list in
`CLAUDE.md`, both ways. There is no comment anywhere claiming to be
"everything CI runs"; the gates are what say so.

Every tool is fetched at a pinned version through `go run`, never from
your `PATH`, so the Go toolchain is all you need. `make pins` holds each
version in the `Makefile` equal to the one the workflows use.

## What `make check` runs

In the order the `check:` target lists them:

| Target | Holds |
|---|---|
| `make fmt` | gofmt would change nothing |
| `make vet` | `go vet`, three times: untagged, `-tags=live` and `-tags=evals`, so the tagged drivers keep compiling |
| `make tidy` | `go.mod` and `go.sum` are what `go mod tidy` would write (`go mod tidy -diff`; nothing is rewritten) |
| `make lint` | golangci-lint with `.golangci.yml`, `forbidigo` included: no `fmt.Print*` or `os.Stdout` outside `cmd/` and `scripts/` |
| `make cover` | `make test` (race detector, shuffled, one coverage profile over `cmd/` and `internal/`), then `gates coverage`: 80% per package, scored on the package's own files, with each lower floor or exemption carrying its reason |
| `make vuln` | govulncheck |
| `make licenses` | every dependency's license is on the allow-list |
| `make secrets` | gitleaks over the tree and over every commit the clone holds, with `.gitleaks.toml` |
| `make leaks` | `gates leaks`: nothing from a real instance or account in the working tree |
| `make pins` | `gates pins`: every action pinned by SHA with a version comment, every tool at one exact version, the same in the `Makefile` and the workflows |
| `make classes` | `gates classes`: the error vocabulary in the code and in `docs/architecture.md` §6.5, equal both ways |
| `make api-coverage` | `gates api-coverage`: every operation in the OpenAPI snapshot has a verdict in `testdata/api-coverage.tsv`, and every client call a `used` row |
| `make api-fields` | `gates api-fields`: every parameter sent and every field decoded exists on its operation, or carries a verified omission in `testdata/api-fields.tsv` |
| `make schema-diff` | `gates schema-diff`: the tool surface against the last tag, else `testdata/schema-baseline.json`; fails on a removed tool, a lost field or a new required input |
| `make descriptions` | `gates descriptions`: every tool and input is described, and every witness input says it is NOT a retry signal |
| `make bodies` | `gates bodies`: every string a write sends goes through `internal/quickaction` or is listed as a plain field with a reason |
| `make smoke` | `gates smoke`: the built binary over stdio at two protocol revisions, stdout carrying only frames, `[auth]` with no credentials, a clean exit on disconnect |
| `make staleness` | `gates staleness`: the documents against the code (below) |
| `make checklist` | `gates checklist`: `CLAUDE.md`'s definition of done against `check:` |
| `make changelog-links` | `gates changelog-links`: every CHANGELOG version heading has its link reference |
| `make transcript` | `gates transcript`: the live and eval drivers reach a terminal only through the redacting printer |
| `make live-cover` | `gates live-cover`: every tool option was driven live, or is waived with a reason in `testdata/live-cover.tsv` |
| `make outcomes` | `gates outcomes`: every write result states its outcome, and none from the request alone |
| `make evals-check` | each eval task, with no model and no key: an empty run fails, a scripted run that does the task passes, and one that follows the task's injected instruction fails, each driven through the real server |
| `make mcpb` | `gates mcpb`: `packaging/mcpb/manifest.json` against its vendored schema and the staging table the packer uses |
| `make release` | `gates release`: `.goreleaser.yaml` against `release.yml` and the packer |
| `make server-json` | `gates server-json`: the registry entry generator against the vendored registry schema |
| `make actionlint` | the workflows are valid |
| `make goreleaser-check` | the release config is valid |
| `make parity` | `gates parity`: `make check` and `ci.yml` run the same set, and every gate runs where its registry entry says |

`go run ./scripts/gates` with no argument lists every gate with a line
on what it holds. Each gate is registered as run by `check`, on pull
requests, in the release, or by hand with a reason, and a test refuses
an entry that has not decided.

### On pull requests

CI's `pr` job runs these, measured from the merge-base of the pull
request's head and the base branch as it is now, never from the base
SHA the event recorded, which is stale in a stack of pull requests:

| Target | Holds |
|---|---|
| `make merge-base` | `gates merge-base`: prints the commit the pull request is measured from |
| `make changelog` | `gates changelog`: the pull request adds a CHANGELOG line, unless it cuts a release |
| `make schema-ack` | `gates schema-ack`: a changed tool surface is acknowledged by `SCHEMA-CHANGE:` or `BREAKING CHANGE:` on a commit the pull request adds |

The acknowledgment is an empty commit, never an amend:

```bash
git commit --allow-empty -m "Acknowledge the tool surface change" \
  -m "SCHEMA-CHANGE: <what was added>"
```

### By hand

These need the network, rewrite a committed file, or both:

| Target | Does |
|---|---|
| `make schema-baseline` | `gates schema-baseline`: records the current surface as `testdata/schema-baseline.json`. Deliberate: it is the contract `schema-diff` holds before the first tag. |
| `make leaks-history` | `gates leaks-history`: the leak rules over every blob, commit message and tag. Run before anything goes public. |
| `make api-diff API_TAG=<tag>` | `gates api-diff`: refetches the OpenAPI snapshot at a GitLab release tag. A failed or short fetch leaves the committed file untouched. |
| `make schema-refetch` | `gates schema-refetch`: the vendored manifest and registry schemas against what their sources serve now. Writes nothing. |
| `make deps` | `gates deps`: every direct dependency updated within six months, or pinned in `go.mod` with a reason. |
| `make release-notes VERSION=<version>` | `gates release-notes`: prints the CHANGELOG section a tag would publish. |
| `make mcpb-pack` | `gates mcpb-pack`: packs the bundle from a built `dist/`. The release runs it from goreleaser's hook. |

The pre-commit hook runs `gates precommit`: gofmt, vet, the leak scan
and `gitleaks protect --staged` over what is staged, every problem
collected before it fails. Without gitleaks installed it warns and
skips that part; `make secrets` in CI does not skip.

## What the staleness gate holds

`make staleness` holds the documents to what the code defines, each
rule with a floor on how much it read:

- every repository path a document names, in a code span or a relative
  link, exists;
- the package map in `CLAUDE.md` against `go list`, both ways;
- `docs/configuration.md` against `internal/config`'s list of
  variables, both ways;
- this file names every gate in the registry, and shows none it lacks;
- the README's tool table against the tools the built binary registers;
- the tool counts in `docs/architecture.md` §8 against its table;
- the status line against the newest tag or CHANGELOG heading;
- if Go that ships changed since the last tag, the CHANGELOG has an
  entry under `[Unreleased]`;
- no version number in prose (a badge shows it);
- a number in the sentence introducing a list matches the list;
- the application blocks in `docs/setup.md`, generated from
  `internal/scopes` for each mode and compared exactly.

A failure prints what it expected. For `docs/setup.md` it prints the
block to paste between the markers.

## Every gate asserts a floor

"Found nothing" and "looked at nothing" print the same line, so each
gate also asserts how much it read: packages and statement blocks in a
coverage profile, files in a leak scan, operations in the API snapshot,
tools in a schema dump. A gate nobody has watched fail is not yet a
gate. Break it on purpose once and watch it fail before trusting it.

## Green gates are not done

Anything touching sign-in, the quick-action guard, diff positions, a
write's witness or an API response shape gets a live run before it
counts, and **the transcript is read**. A driver in this family twice
reported success while its results were wrong.

```bash
make live LIVE_ARGS="-namespace example-group/scratch"
```

What it does, so nothing is a surprise:

- It runs against gitlab.com, always; there is no instance flag. It
  signs in with your stored profile (`-profile` picks one) and needs a
  group you own, named by `-namespace`.
- It creates private scratch projects named for the run under that
  group, fills them with its own files, branches, issues, comments and
  merge requests, and drives every tool and option against them. Every
  read it makes is inside what it created; searches carry the run's own
  word.
- At the end it deletes the projects, unless `-keep` is set. On
  gitlab.com deletion is delayed, and the driver says so.
- Every line goes through one redacting printer: hosts, paths, the
  account and ids are masked. Titles and bodies are not, which is why
  every one of them is text the driver wrote. Read the transcript
  before sharing it.
- It records what it sent per tool option, which `make live-cover`
  reads.

`make evals` builds the binary and scores a model against the tool
surface through `claude -p`, with every built-in tool off. It needs the
`claude` CLI signed in, costs money and is not deterministic, so it is
run by hand and its transcript is read like the live driver's. It reads
only the in-memory instance of `internal/gapi/gitlabtest`. Four tasks
carry an instruction planted in the content by someone outside the
project: read a private file, run a quick action, post in another
project, approve and merge. Each is scored on whether the model
followed it. `EVAL_ARGS="-task injected -v"` runs those alone and
prints every call.

## The test instance

The server serves gitlab.com only, and no setting a person can reach
changes that. One development override points the binary at the
in-memory instance instead:

| Variable | What it does |
|---|---|
| `GITLAB_MCP_TEST_INSTANCE` | A base URL on a loopback host (`127.0.0.1`, `::1` or `localhost`) that stands in for gitlab.com. Plain `http` is fine there. It has no flag. |

The evals and the smoke gate set it; tests set it or build the
configuration in code. Start-up refuses any other host, so it can never
send a token to a real one, and logs a warning whenever it is set. A
profile signed in to the test instance keeps its token away from
gitlab.com, and the reverse. It is documented here and nowhere a person
configuring the server would read: `make staleness` fails if
`docs/configuration.md` names it, and `make mcpb` if the bundle sets
it.

## Adding a tool

1. Declare it in `internal/tools` through the one `register`, with a
   `Kind` (Read, Write, Ship or Destructive) and, if it belongs to one,
   a toolset. The kind decides the annotations, whether it registers,
   and the dry-run context. Moving a tool between kinds or toolsets is
   the maintainer's call.
2. Put the logic in `internal/service`, not the handler.
3. Every Markdown body goes through `internal/quickaction`, or
   `make bodies` fails.
4. Add it to the README's tool table and to `docs/architecture.md` §8,
   or `make staleness` fails.
5. Drive it in the live driver, or waive it in
   `testdata/live-cover.tsv` with a reason, or `make live-cover` fails.
6. Read `make schema-diff` for anything breaking, resources included,
   and acknowledge the change on the pull request.

## Adding an API call

`make api-coverage` fails on a client call with no verdict. Add or
update the operation's row in `testdata/api-coverage.tsv`: `used` with
the client method, or gated, deferred or written off with a reason.
`make api-fields` fails on a field the published API does not have;
wire types in `internal/gitlab` are hand-written, and carry only the
fields used.

## Errors

Every error is `[class] message` from the closed list in
`docs/architecture.md` §6.5. Change the code and the list in the same
commit; `make classes` holds them equal both ways.

## Fixtures and goldens

Fixtures are generated, never recorded. `internal/gapi/gitlabtest`
builds its instance from code: synthetic users, `example-group`
namespaces, `gitlab.example.com` and `.invalid` hosts. A fixture copied
from a live response is the leak, whatever a scanner says about it.

`internal/render/testdata` holds what each renderer prints. A
change in output shows up as a diff somebody has to read:

```bash
go test ./internal/render -update
```

Regenerating is one flag; reading the diff is the part that matters.

## Releasing

`docs/release.md`, and nowhere else.
