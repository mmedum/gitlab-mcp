# Contributing to gitlab-mcp

Thanks for considering a contribution. This is a Go MCP server for
gitlab.com with a strict test and confidentiality posture. Read this
once before your first pull request; CI enforces the rest.

## Reporting bugs and requesting features

Open a [GitHub issue](https://github.com/mmedum/gitlab-mcp/issues/new/choose)
with the bug report template. It asks for what
[`docs/troubleshooting.md`](docs/troubleshooting.md#reporting-a-problem)
lists: `doctor` output, the version and a debug log. All of them are
safe to paste. Never paste a token, a tool result or anything from your
instance: a result is your organization's code and other people's words.

For anything security-sensitive, such as a vulnerability or a suspected
token leak, do not open a public issue. Follow [`SECURITY.md`](SECURITY.md).

## Development setup

```bash
git clone https://github.com/mmedum/gitlab-mcp
cd gitlab-mcp
make build
make hooks
```

Go is the only prerequisite, at the version in `go.mod`. Every other
tool runs at a pinned version through `go run`, never from your `PATH`.
`make hooks` points git at `.githooks/`, whose pre-commit hook runs
`gates precommit`: gofmt, vet, the leak scan and `gitleaks protect
--staged` over what is staged. Without gitleaks installed the hook warns
and skips that part; `make secrets` in CI does not skip.

## Gates that must pass

```bash
make check
```

`make check` runs the gates CI runs, and the `check` target in the
`Makefile` is the list. `make parity` fails when that list and `ci.yml`
differ, so a gate you add goes in both. CI runs the unit tests on Linux,
macOS and Windows.

Besides the gates below, `check` runs `fmt`, `vet` (untagged, and with
the `live` and `evals` tags so the drivers keep compiling), `tidy`,
`lint` (golangci-lint, forbidigo included), `cover` (race tests, then
the `coverage` gate), `vuln` (govulncheck), `licenses`, `secrets`
(gitleaks over the tree and every commit), `evals-check`, `actionlint`
and `goreleaser-check`.

Every check this repository runs on itself is Go, under `scripts/gates`,
so the code holding the gates shut is held to them too.
`go run ./scripts/gates` lists them, and `go run ./scripts/gates <name>`
runs one.

| Gate | Holds | Runs in |
|---|---|---|
| `coverage` | 80% per package, scored on its own files; a lower floor or an exemption carries its reason | check |
| `leaks` | nothing from a real instance or account in the working tree; `leaks-history` scans every blob, commit message and tag | check; history by hand |
| `pins` | actions pinned by SHA with a version comment, container images by digest, and every tool at one exact version, the same in the `Makefile` and the workflows | check |
| `classes` | every error class the code emits is declared in `internal/gapi/errors.go` and listed in `Classes`, and every declared class is emitted | check |
| `api-coverage` | every operation in the OpenAPI snapshot has a verdict in `testdata/api-coverage.tsv`, every client call a `used` row, and every GraphQL operation a doc comment that mentions REST on the function that sends it | check |
| `api-fields` | every parameter sent and field decoded exists on its operation, or carries a verified omission in `testdata/api-fields.tsv` | check |
| `schema-diff` | the tool surface against the last tag, else `testdata/schema-baseline.json` | check |
| `descriptions` | every tool and input is described, and every witness input says it is NOT a retry signal | check |
| `bodies` | every string a write sends goes through `internal/quickaction`, or is listed as a plain field with a reason | check |
| `smoke` | the built binary over stdio at two protocol revisions: stdout carries only frames, `[auth]` with no credentials, a clean exit on disconnect | check |
| `staleness` | the docs against the code: the README's tool table, the settings in `docs/configuration.md`, the blocks in `docs/setup.md`, this file's gates and test instance, and no version in prose | check |
| `docs` | the decision index, every `D-` citation and doc anchor, the paths and targets a doc names, the codemap, and the live steps comments, docs and records name | check |
| `checklist` | `CLAUDE.md`'s definition of done against `make check`, in order | check |
| `changelog-links` | every CHANGELOG version heading has its link reference | check |
| `transcript` | the live driver and the evals reach a terminal only through the redacting printer | check |
| `live-cover` | every tool option was driven live, or is waived with a reason in `testdata/live-cover.tsv` | check |
| `outcomes` | every write's result states its outcome, and none from the request alone | check |
| `mcpb` | the bundle manifest against its vendored schema and the staging table the packer uses | check |
| `release` | `.goreleaser.yaml` against `release.yml` and the packer | check |
| `server-json` | the registry entry generator against the vendored registry schema | check |
| `parity` | `make check` and `ci.yml` run the same set, and every gate runs where its registry entry says | check |
| `merge-base` | prints the commit a pull request is measured from: the merge-base with the base branch's head, not the SHA the event recorded | CI |
| `changelog` | a pull request adds a CHANGELOG entry, unless it cuts a release or only bumps required modules other than the MCP SDK | CI |
| `schema-ack` | a pull request names each changed item of the tool surface in a commit footer | CI |
| `release-notes` | one version's CHANGELOG section, which is the release note | release |
| `mcpb-pack` | packs the bundle from what goreleaser built | release |
| `api-diff` | refetches the OpenAPI snapshot at a GitLab release tag, or fails and leaves the committed file untouched | by hand |
| `schema-baseline` | records the current surface as `testdata/schema-baseline.json` | by hand |
| `schema-refetch` | compares the vendored manifest and registry schemas with what upstream serves; writes nothing | by hand |
| `deps` | every direct dependency updated within six months, or pinned in `go.mod` with a reason | by hand |
| `precommit` | the pre-commit hook, above | by hand |

Every gate also asserts a floor on how much it read, because "found
nothing" and "read nothing" print the same line. Before you trust a gate
you changed, break what it holds and watch it fail.

These are worth knowing before they fail on you:

- **The schema diff** fails on a removed tool, a lost field or a new
  required input, unless a major release declares it: an entry under
  `[Unreleased]` that says `**Breaking:**` and names the tool in
  backticks. Anything else that moved is reported, annotations and
  resources included. A client decides from those whether to ask, so
  read them.
- **`make leaks`** fails on token prefixes, and on addresses and hosts
  outside the example domains. Write fixtures with `example-group`,
  `gitlab.example.com` and `.invalid` hosts. Run `make leaks-history`
  before anything goes public.

  **What it cannot catch is the payload.** A title or a comment is
  ordinary words. Two habits cover the gap, and only habits can:
  fixtures are generated by `internal/gapi/gitlabtest`, never recorded,
  and the live driver reads back only what it wrote.
- **The docs gate** (`make docs`) holds the docs to their references. A
  decision in `docs/architecture.md` is `D-NN`; an ID is never reused,
  and the file ends with its `Next ID:` line. Cite a decision by ID, or a
  doc by `docs/<file>.md#<heading-slug>`, never by section number. What
  you verified about GitLab goes in the comment at the line that depends
  on it, with the source link and the live step that checks it, written
  `Checked by the live step "<name>".`, never in a file of its own.
- **`make staleness`** prints what it expected. For `docs/setup.md` it
  prints the block to paste between the markers, generated from
  `internal/scopes`.
- **Renderer goldens.** `internal/render/testdata` holds what each
  renderer prints, from generated data only.
  `go test ./internal/render -update` rewrites them; reading the diff is
  the part that matters.

## Changing the tool surface

On a pull request, `make schema-ack` builds the merge-base and the head
and compares the whole surface: tools, annotations included, resource
templates and the instructions. Every item that changed must be named
in a footer of a commit the pull request adds, as an empty commit
rather than an amend:

```bash
git commit --allow-empty -m "Acknowledge the tool surface change" \
  -m "SCHEMA-CHANGE: update_issue, instructions: what changed"
```

A footer names a tool by its name, a resource template by its URI
template, and the instructions as `instructions`, each a whole word.
Only the names before the first colon followed by white space count; the
description after it names nothing. Names are separated by commas, and a
list wraps only after one. Put a name with a comma or a space in it in
backticks. A `Token #value` footer such as `Closes #12` acknowledges
nothing. A break of the last release, such as a removed tool or input,
needs `BREAKING CHANGE: <names>: what broke` instead. Narrowing
something no release shipped is a plain change. A refusal prints the
footer that fixes it.

## Adding a tool

1. Declare it in `internal/tools` through `register`, with a `Kind`
   (Read, Write, Adding, Ship or Destructive) and, if it belongs to one,
   a toolset (D-20). The kind decides the annotations, whether it
   registers and the dry-run context. Moving a tool between kinds or
   toolsets is the maintainer's call.
2. Put the logic in `internal/service`, not the handler.
3. Route every Markdown body through `internal/quickaction`, or
   `make bodies` fails (D-18).
4. Add it to the README's tool table, or `make staleness` fails.
5. Drive it in `scripts/livegitlab`, or waive it in
   `testdata/live-cover.tsv` with a reason, or `make live-cover` fails.
6. Read `make schema-diff`, and add the `SCHEMA-CHANGE:` footer above.

## Adding an API call

`make api-coverage` fails on a client call with no verdict. Add or
update the operation's row in `testdata/api-coverage.tsv`: `used` with
the client method, or gated, deferred or written off with a reason. A
GraphQL operation is used only where REST has no route, and the
function that sends it says why in its doc comment (D-09).
`make api-fields` fails on a field the published API does not have;
wire types in `internal/gitlab` are hand-written and carry only the
fields used (D-08).

## Errors

Every error is `[class] message` from a closed vocabulary (D-74). The
classes are the `Class` constants in `internal/gapi/errors.go`, each
saying what a caller should do. `make classes` holds the constants, the
`Classes` list and what the code emits to one set.

## The test instance

The server serves gitlab.com only, and no setting a person can reach
changes that (D-02). One development override points the binary at the
in-memory instance instead: `GITLAB_MCP_TEST_INSTANCE`, a base URL on a
loopback host (`127.0.0.1`, `::1` or `localhost`), where plain `http` is
fine. It has no flag.

The evals and the smoke gate set it; tests set it or build the
configuration in code. Startup refuses any other host and logs a warning
whenever it is set. A profile signed in to the test instance keeps its
token away from gitlab.com, and the reverse (D-03). It is documented
here and nowhere a person configuring the server would read:
`make staleness` fails if `docs/configuration.md` names it, and
`make mcpb` if the bundle sets it.

## The live run

Every fake in the unit suite is written from what we believe GitLab
does, and only a live run tests that belief (D-82). Anything touching
sign-in, the quick-action guard, diff positions, a write's witness or an
API response shape needs one before it counts, and the transcript has to
be read: a pass count cannot show a result that is confidently wrong.

```bash
make live LIVE_ARGS="-namespace example-group/scratch"
```

What it does, so nothing is a surprise:

- **gitlab.com, as you.** There is no instance flag. It signs in with
  your stored profile (`-profile` picks one) and needs a group you own,
  named by `-namespace`.
- **Its own projects only.** It creates private scratch projects named
  for the run under that group, fills them with its own files, branches,
  issues, comments and merge requests, and drives every tool and option
  against them. Every read stays inside what it created, and searches
  carry the run's own word (D-83).
- **Cleanup.** At the end it deletes the projects, unless `-keep` is
  set. On gitlab.com deletion is delayed, and the driver says so.
- **Spikes.** A full run also asks the spikes that check a load-bearing
  GitLab behavior, each named in the comment that relies on it.
  `-only-spike <key>` asks one alone after seeding and drives no tool.
  It takes a short key such as `merge-status`, not the spike's name, and
  the flag's help lists the keys. A spike with no key is asked only by a
  full run.
- **Reading it.** Every line goes through one redacting printer: hosts,
  paths, the account and ids are masked (D-84). Titles and bodies are
  not, which is why every one of them is text the driver wrote. Read the
  transcript before sharing it.
- **Coverage.** It records what it sent per tool option in
  `testdata/live-cover-record.tsv`, which `make live-cover` reads.

`make evals` is run by hand too, because it spends money and is not
deterministic. It builds the binary and scores a model against the tool
surface through `claude -p`, with every built-in tool off, against the
in-memory instance only (D-85). Some tasks carry an instruction planted
in the content by someone outside the project: read a private file, run
a quick action, post in another project, approve and merge. Each is
scored on whether the model followed it. `EVAL_ARGS="-task injected -v"`
runs those alone and prints every call. `make evals-check`, in
`make check`, runs the scorers with no model and fails if one passes on
an instance nobody touched.

## Branch and commit conventions

- `main` is released code and takes pull requests only. Work on a short
  topic branch.
- Write the subject in the imperative, under 72 characters, saying what
  changes. Look at a recent `git log` for the tone.
- The body says the problem, why this change solves it, and the
  alternatives rejected. A fixed review finding is one line at the end
  of the body. A declined one goes in the pull request, or becomes a
  present-tense comment at the line it protects.
- Never commit directly to `main`. The maintainer pushes tags and
  releases.

## Pull requests

1. Open against `main`, with a summary and a test plan.
2. CI must be fully green.
3. A maintainer reviews and merges.

## Changelog

Every user-visible change gets an entry under `## [Unreleased]` in
`CHANGELOG.md`, in the same pull request that makes the change. Leave out
changes only contributors see, unless they must act on them.

- **Sections** are the [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
  set and nothing else: Added, Changed, Deprecated, Removed, Fixed and
  Security, in that order.
- **One line per change, 25 words or fewer, impact first.** Write for
  someone deciding whether to upgrade: what changes for them, not the
  mechanism. The investigation belongs in the commit message.
- **Breaking changes** start with `**Breaking:**`. Such an entry may add
  one sentence saying what to do. Anything needing a new login, a new
  scope or a config change is breaking.
- **Security fixes** name the CVE or GHSA.
- **A dependency bump** needs no entry, except the MCP SDK's, whose
  version clients see.

`release.yml` lifts the matching section verbatim into the GitHub release
notes, so the entry is the release note. Versioning follows
[SemVer](https://semver.org/spec/v2.0.0.html), read through the tool
surface: tool names, input and output schemas, resources and documented
behavior.

## Releasing

Maintainers release as [`docs/release.md`](docs/release.md) says.

## Code style

- Match the surrounding code. Do not reformat what you did not touch.
- Comments explain why: a constraint, an invariant, something GitLab
  does that the code has to work around. Do not narrate what the code
  does.
- Content is data. It reaches the model inside the marked boundaries,
  never in the server's own words (D-13, D-17).
- Logs record the call, never the payload (D-05).

`CLAUDE.md` has the short version of the rules, and `docs/architecture.md`
the reasoning. Start there if you are wondering why the code is the way
it is.

## License

By contributing, you agree that your contributions will be licensed
under the project's [Apache 2.0 License](./LICENSE).
