# gitlab-mcp

[![CI](https://github.com/mmedum/gitlab-mcp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/mmedum/gitlab-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/mmedum/gitlab-mcp?include_prereleases&sort=semver)](https://github.com/mmedum/gitlab-mcp/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/mmedum/gitlab-mcp.svg)](https://pkg.go.dev/github.com/mmedum/gitlab-mcp)
[![License: Apache 2.0](https://img.shields.io/github/license/mmedum/gitlab-mcp)](./LICENSE)

An MCP server for gitlab.com. One binary over stdio, signed in as you.
It works inside projects: issues,
merge requests, reviews, the repository and CI. Instance and group
administration, runners, CI variables and tokens are out of scope.

Unofficial, and not affiliated with GitLab Inc. See `NOTICE`.

## What makes this one different

- **A small surface, gated by registration.** About fifty tools rather
  than an API mirror, and a tool that is switched off is not registered,
  so it cannot be called at all.
- **Nothing it writes runs a quick action.** `/merge` in a comment is
  refused or escaped, never executed.
- **Content is marked as untrusted.** Issues, reviews, files and job
  logs come back inside boundaries the content cannot close.
- **Signs in like a desktop app.** Your own OAuth application, a browser
  tab, the token in the OS keyring.
- **Verifiable releases.** Signed checksums, build provenance and SBOMs.

## Status

Under construction: the release badge above names the newest tag, and
`CHANGELOG.md` says what each one holds. Until the first tag, only the
read tools below are being built; nothing writes yet.
`docs/architecture.md` §16 is the plan.

## Tools

| Tool | What it does |
|---|---|
| `get_me` | Who is signed in, with which scopes, and what GitLab reports about itself |
| `resolve_url` | Turn a GitLab web URL into the arguments another tool takes, and name that tool |
| `search_projects` | Find projects by name, or within a group |
| `get_project` | One project: default branch, visibility, what it has turned on |
| `list_members` | Who has access to a project, and with which role |
| `find_users` | Accounts by exact username or by name |
| `search_issues` | Find issues across gitlab.com, a group or a project |
| `get_issue` | One issue, its description marked as untrusted content |
| `list_discussions` | The comment threads on an issue or a merge request |
| `search_merge_requests` | Find merge requests across gitlab.com or a project |
| `get_merge_request` | One merge request with its approvals |
| `list_mr_files` | The files a merge request changes, with line counts and GitLab's markers |
| `get_mr_diff` | A merge request's diffs, file by file, bounded |
| `list_mr_commits` | A merge request's commits |
| `list_review_comments` | Your unpublished review comments on a merge request |
| `get_file` | A file at a ref, bounded |
| `list_tree` | A directory listing at a ref |
| `list_branches` | A project's branches |
| `list_commits` | Commits on a ref or a path |
| `get_commit` | One commit and its diff, bounded |
| `compare_refs` | The commits and diffs between two refs, bounded |
| `list_tags` | A project's tags |
| `list_pipelines` | A project's CI pipelines |
| `get_pipeline` | One pipeline with the jobs that failed |
| `list_jobs` | A pipeline's jobs |
| `get_job_log` | A window of a job's log, secrets masked; the failing section on request |
| `lint_ci` | Check a project's CI configuration at a ref |
| `list_labels` | The labels a project's issues and merge requests can carry |
| `list_milestones` | A project's or a group's milestones |
| `search` | Code, commits, comments and more, in a project, a group or everywhere |
| `list_todos` | Your to-do items |

Three resources carry the same text for clients that attach rather
than call: an issue, a merge request and a job log.

## Install

Download an archive for your platform from the
[releases](https://github.com/mmedum/gitlab-mcp/releases) page, or:

```bash
go install github.com/mmedum/gitlab-mcp/cmd/gitlab-mcp@latest
```

Claude Desktop users can open the `.mcpb` bundle from the same release.
It does not log you in; do the steps below first.

## Sign in

You bring your own OAuth application. Nothing is shipped in the binary,
and there are no personal access tokens.

1. **Register an application**, once. On gitlab.com: your avatar → Edit
   profile → Applications (or a group's). Redirect URI
   `http://127.0.0.1/callback`, **Confidential unchecked**, scope `api`
   (`read_api` for read-only). `docs/setup.md` has the exact values.
2. **Log in** from a terminal:

   ```bash
   gitlab-mcp login --client-id <application id>
   ```

   It prints the scopes it will ask for, opens your browser, and stores
   the token in the OS keyring. On a machine without a browser,
   `--no-browser` prints the URL and the `ssh -L` line to forward the
   callback.
3. **Check it**:

   ```bash
   gitlab-mcp doctor
   ```

   `doctor` walks the connection, TLS, the sign-in, the application, the
   granted scopes and one call as you, and names what is missing.
   `status` and `logout` do what they say; `--profile` keeps two
   gitlab.com logins side by side.

## Connect a client

Claude Code:

```bash
claude mcp add gitlab -- gitlab-mcp
```

Any client that takes a JSON server list:

```json
{
  "mcpServers": {
    "gitlab": { "command": "gitlab-mcp" }
  }
}
```

Claude Desktop can instead open the `.mcpb` bundle from a release.

## Configuration

Every setting is an environment variable with the `GITLAB_MCP_` prefix
and a matching flag. `docs/configuration.md` lists them all.

## Safety

GitLab content was written by someone other than you, and some of it is
written to steer an agent. The server marks it as untrusted data, never
fetches what it references, and nothing it adds tells the model to act
on it.

What can be registered depends on the settings, because GitLab's `api`
scope cannot separate any of it:

| Setting | Registers |
|---|---|
| `GITLAB_MCP_READ_ONLY=true` | Read tools only, with a `read_api` token |
| default | Read and Write: issues, comments, reviews, branches, merge requests |
| `GITLAB_MCP_ENABLE_SHIP=true` | adds merging, approving, running CI and releases |
| `GITLAB_MCP_ENABLE_DESTRUCTIVE=true` | adds deletion, and each call must pass `confirm: true` |

- **No quick actions.** GitLab runs `/merge`, `/close` and the rest from
  a description or comment. Every body this server sends is checked, and
  a quick-action line is refused, or escaped when you ask.
- **Protected branches.** Code reaches the default branch or a protected
  branch only through a merge request; a direct commit there is refused.
- **No blind retries.** A create that may or may not have happened is
  settled by reading, never by creating again.

The default token can still merge and approve; leaving Ship off stops
this server doing so, not anything else holding the token.
`docs/security.md` says more.

## Verify a release

Each release signs `checksums.txt` with a keyless Sigstore certificate
and attests every archive and the bundle:

```bash
sha256sum -c checksums.txt --ignore-missing
cosign verify-blob checksums.txt --bundle checksums.txt.bundle \
  --certificate-identity "https://github.com/mmedum/gitlab-mcp/.github/workflows/release.yml@refs/tags/<tag>" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify <archive> --repo mmedum/gitlab-mcp
```

## Contributing

`CONTRIBUTING.md`. Security reports go through `SECURITY.md`, not an
issue.

## License

Apache-2.0. See `LICENSE` and `NOTICE`.
