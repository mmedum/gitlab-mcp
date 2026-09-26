# Release runbook

`main` is released code, and pushing, tagging and publishing are the
maintainer's. This file is release day; `docs/development.md` is every
other day.

## What the tag does

Pushing a `vX.Y.Z` tag runs `.github/workflows/release.yml`:

| Job | Does |
|---|---|
| `verify-ci` | refuses the tag unless the `ci` run for the tagged commit finished green; a tag is only a pointer |
| `goreleaser` | runs `go test`; lifts the tag's CHANGELOG section into the release notes with `gates release-notes`; builds one target twice and compares the hashes; runs goreleaser with `.goreleaser.yaml`; attests build provenance |
| `registry` | calls `.github/workflows/publish-mcp.yml`, skipped for a prerelease |

goreleaser produces six platform archives (`.tar.gz`, and `.zip` for
Windows) with `LICENSE`, `NOTICE` and `README.md` in each, an SBOM per
archive, the macOS universal binary, the `.mcpb` bundle for Claude
Desktop (packed by `gates mcpb-pack` in the universal binary's hook, so
it reaches `checksums.txt`), `checksums.txt`, and a keyless cosign
signature bundle over it. The provenance attestation covers the
archives, `checksums.txt` and the bundle. A tag with a suffix such as
`-rc1` is marked a prerelease.

`publish-mcp.yml` downloads the published `checksums.txt`, verifies the
signature over it against this repository's `release.yml` at that exact
tag, generates `server.json` with `gates server-json`, verifies the
`mcp-publisher` binary against its own release workflow, and publishes
the entry to the MCP registry. It is its own workflow so it can be run
again for a tag that already shipped.

## Rehearse it

```bash
make release-rehearse
make release-notes VERSION=Unreleased
```

`release-rehearse` runs the goreleaser `release.yml` installs (the
`Makefile` pins it and `make pins` holds the two equal) with
`--snapshot --clean --skip=publish,sign,sbom`, and leaves everything
under `dist/`. `release-notes` prints what the release page would say.

## Before the tag

- `make check` on the commit to be tagged, and CI green on it.
- A live run of anything that touched sign-in, the quick-action guard,
  diff positions, a write's witness or an API response shape, **with the
  transcript read** (`docs/development.md`).
- `make schema-diff`, read for anything breaking, resources included.
  A breaking change has `BREAKING CHANGE:` in the range, and a
  `**Breaking:**` CHANGELOG line.
- `make deps`, `make schema-refetch` and `make leaks-history`. They need
  the network, so they are not in `check`, and this is when they are
  run. The vendored schemas are frozen in both directions: their hashes
  say the bytes are the ones reviewed, never that upstream still serves
  them.
- `/security-review` over the previous tag to `HEAD`, committed under
  `audit/security-reviews/` in a file named for the tag, with findings
  fixed or recorded in `docs/architecture.md` §16a.
- The release commit, on a topic branch and through a pull request like
  any other: `main` is never pushed to directly, release commits
  included. It renames `[Unreleased]` in `CHANGELOG.md` to the version
  with the date, adds its link reference, and leaves an empty
  `[Unreleased]` above it. The `changelog` gate lets a release cut
  through without a new entry, and `staleness` accepts the new heading
  before the tag exists.
- The status line of `docs/architecture.md` says what the release holds
  and what is owed.

`gates release-notes` fails on a version with no section, so a tag
pushed before the rename stops the release before goreleaser runs.

## Push the tag

After the release commit is merged:

```bash
git checkout main
git pull --ff-only
make check
git tag -a vX.Y.Z -m "vX.Y.Z: <what shipped>"
git push origin vX.Y.Z
```

Annotated, and the message says what shipped: whoever finds the tag
reads it rather than the release page.

**Push tags one at a time.** GitHub drops tag events past the third in
one push, and the release never runs. If that happens, or the run fails
for a reason unrelated to the tag, run `release.yml` by hand with
**Run workflow**, choosing the tag rather than a branch: the checkout,
the notes and goreleaser all read the ref it runs on.

## After the workflow

Check the version in five places: the bundle's file name, the archive
file names, `manifest.json` inside the bundle, the binary's own
`--version`, and `checksums.txt`. Four agreeing is what a broken bundle
looks like. The one to look for is the bundle missing from
`checksums.txt`: it then ships unsigned and looks no different.

Then verify from outside, with the commands the release page prints in
its footer:

```bash
sha256sum -c checksums.txt --ignore-missing
cosign verify-blob checksums.txt --bundle checksums.txt.bundle \
  --certificate-identity 'https://github.com/mmedum/gitlab-mcp/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify gitlab-mcp_X.Y.Z_linux_amd64.tar.gz --repo mmedum/gitlab-mcp
gh attestation verify gitlab-mcp_X.Y.Z.mcpb --repo mmedum/gitlab-mcp
```

Archive and bundle names carry the version without the `v`. The
certificate identity is the exact workflow at the exact tag, not a
pattern: a checksum file signed by another workflow or for another tag
is not this release.

**Exit 0 on empty output is not evidence.** `cosign verify-blob` and
`gh attestation verify` say little when they pass, and a command that
verified nothing also says little. Run one against a deliberately
corrupted copy and confirm it exits non-zero before believing the run
that passed.

Check the `registry` job itself, not the release page: a green release
with no entry looks the same as one with an entry. Then download the
released binary and run `go run ./scripts/gates smoke <path>` against
it (not `make smoke`, which builds its own first), run
`gitlab-mcp doctor` with it, and open the bundle in Claude Desktop.

Record it under `audit/release-smoke/`, in a file named for the tag: the gates, the
reproducible-build hashes, the schema re-fetches, anything found, and
the go-ahead.

## What no rehearsal reaches

Three steps need the OIDC token only a real workflow run has: the
**cosign signature**, the **provenance attestation** and the **registry
publish**. `make check`, `make goreleaser-check`, `make actionlint` and
`make release-rehearse` all pass while any of the three is wrong. Read
the first run after any change to the pipeline rather than watching it
go green, and know the recovery for each, because they differ:

| Fails | State afterward | Recovery |
|---|---|---|
| cosign signing | **no release**: signing comes before publishing | fix it on `main` through a pull request, delete the tag locally and on GitHub, tag again |
| provenance attestation | release published, unattested | re-run the failed job on the same tag |
| registry publish | release fine, no entry | dispatch `publish-mcp.yml` with the tag; never tag again for this |

Dispatching the registry publish for an existing tag:

```bash
gh workflow run publish-mcp.yml --ref main -f tag=vX.Y.Z
```

It reads the published release's `checksums.txt` and verifies its
signature first, so the hash in the entry is the one cosign signed, not
one from a local build. An entry cannot be withdrawn, which is why a
prerelease never reaches the registry.

The rehearsal is quiet about more than tokens:

- **The SBOMs.** `release-rehearse` skips them, because they need syft.
  A broken `sboms:` block is green locally and fails the tag before
  anything is published. Recovery: fix, delete the tag, tag again.
- **The dirty-tree check.** goreleaser refuses to release from a dirty
  tree, and `--snapshot` skips that check. This is why the `before`
  hook is `go mod download` and never `go mod tidy`, and why the
  workflow writes its notes outside the checkout. Recovery: find what
  the run wrote into the tree, fix, delete the tag, tag again.
- **The reproducible build.** Two builds of one target that hash
  differently stop the job before goreleaser releases. The two hashes
  are in the log. Recovery: find the non-determinism, fix, delete the
  tag, tag again.
- **The bundle's own contents.** `make mcpb` holds the manifest against
  the staged tree on every commit, and the packer's tests read back an
  archive they wrote, but only installing it in Claude Desktop shows it
  runs.

Delete a tag only while nobody can have fetched the release. If
goreleaser already created a release for the tag, delete that release
too before tagging again: the workflow does not overwrite one that
exists.

## After the release

New work lands under the empty `[Unreleased]` heading the release
commit left. A tag does not start the next phase of
`docs/architecture.md` §16; the maintainer does.
