# Releasing

A maintainer releases by pushing a tag, and `release.yml` publishes a
release, never a draft. It refuses a tag whose commit has no green `ci`
run, builds one target twice and compares the hashes, and lifts the
tag's CHANGELOG section into the notes. The release carries the platform
archives with an SBOM each, the macOS universal binary and the `.mcpb`
bundle, all in `checksums.txt`, which cosign signs, with provenance
attested. `publish-mcp.yml` then publishes the registry entry from the
signed checksums. A tag such as `vX.Y.Z-rc1` is a prerelease and skips
the registry, whose entries cannot be withdrawn.

## Before the release commit

- `make check` passes, and `make live` has run with the transcript
  read.
- `make vuln` in a worktree of the previous tag. An advisory there and
  not at `HEAD` was fixed by a bump here, and gets a Security line.
- `make schema-diff`, read for anything breaking. A break needs
  `BREAKING CHANGE:` in the range and a `**Breaking:**` CHANGELOG line.
- `make api-diff`, `make deps`, `make schema-refetch` and
  `make leaks-history`, which need the network. An operation GitLab
  added fails `api-coverage` until it has a verdict.
- `/security-review` from the previous tag to `HEAD`. A fixed finding is
  one line in its commit body; a declined one goes in the PR body.
- `make release-rehearse` builds the release unsigned under `dist/`, and
  `make release-notes VERSION=Unreleased` prints the release page.
- On a minor release, read what GitLab's built-in MCP server offers; if
  it gains stdio, local tokens, enforced filtering and a read-only mode,
  say so in the README's goals.

## Steps

1. Merge a release commit through a pull request. It renames
   `[Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`, adds its link reference
   and leaves an empty `[Unreleased]` above it. `changelog` and
   `staleness` both accept it before the tag exists.
2. Wait for `ci.yml` to pass on that exact commit.
3. Tag it, annotated, saying what shipped, and push the tag on its own:

   ```bash
   git checkout main && git pull --ff-only && make check
   git tag -a vX.Y.Z -m "vX.Y.Z: <what shipped>"
   git push origin vX.Y.Z
   ```

   GitHub creates no tag events past the third in one push. If the
   release never starts, run `release.yml` by hand, choosing the tag
   rather than a branch.

`gates release-notes` refuses a version with no CHANGELOG section.

## What no rehearsal reaches

Signing, attestation and the registry publish need a workflow's OIDC
token, so read the first run after any change to the pipeline.

| Fails | State afterward | Recovery |
|---|---|---|
| reproducible build | no release | find the non-determinism from the two hashes in the log, fix it, release the next patch |
| cosign or SBOMs | no release; both precede publishing | fix it, then release the next patch |
| attestation | release published, unattested | re-run the failed job |
| registry publish | release fine, no entry | `gh workflow run publish-mcp.yml --ref main -f tag=vX.Y.Z`; do not tag again |

The rehearsal skips the SBOMs, which need syft. `--snapshot` also skips
goreleaser's dirty-tree check, so keep the `before` hook
`go mod download`, never `go mod tidy`, and the notes file outside the
checkout. A re-run cannot overwrite a release goreleaser already
created; delete that release first.

## Verify from outside

```bash
cd "$(mktemp -d)" && gh release download vX.Y.Z --repo mmedum/gitlab-mcp
sha256sum -c checksums.txt --ignore-missing
cosign verify-blob checksums.txt --bundle checksums.txt.bundle \
  --certificate-identity 'https://github.com/mmedum/gitlab-mcp/.github/workflows/release.yml@refs/tags/vX.Y.Z' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify gitlab-mcp_X.Y.Z_linux_amd64.tar.gz --repo mmedum/gitlab-mcp
gh attestation verify gitlab-mcp_X.Y.Z.mcpb --repo mmedum/gitlab-mcp
```

The identity is the exact workflow at the exact tag. A pass prints
little, so read the exit code, and check that a corrupted copy fails.
The version must agree across the filenames, the bundle's
`manifest.json`, `--version` and `checksums.txt`; a bundle missing from
`checksums.txt` ships unsigned and looks no different.

Check the `registry` job itself, since a release with no entry looks the
same. Run `go run ./scripts/gates smoke <path>` on the downloaded binary
(`make smoke` builds its own), then `gitlab-mcp doctor`, and open the
bundle in Claude Desktop.

Last, run the README's install command outside the repository, as a user
would, and check that `gitlab-mcp --version` names the release.

## A bad release

A bad release is fixed forward.

- Never move or delete a pushed tag. The Go module proxy caches it within
  minutes, and the checksum database records its hash.
- Ship a patch release with the fix.
- In that patch, add a `retract` directive to `go.mod` for the bad
  version: `retract vX.Y.Z // why`. The go command then skips it for
  `@latest`.
- Mark the bad version's CHANGELOG heading `[YANKED]`:
  `## [X.Y.Z] - YYYY-MM-DD [YANKED]`.
