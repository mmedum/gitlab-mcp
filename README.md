# gitlab-mcp

[![Release](https://img.shields.io/github/v/release/mmedum/gitlab-mcp?include_prereleases&sort=semver)](https://github.com/mmedum/gitlab-mcp/releases)
[![ci](https://github.com/mmedum/gitlab-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/mmedum/gitlab-mcp/actions/workflows/ci.yml)

An MCP server for GitLab. One binary over stdio, signed in as you, on
gitlab.com or a self-managed instance. It works inside projects: issues,
merge requests, reviews, the repository and CI. Instance and group
administration, runners, CI variables and tokens are out of scope.

Unofficial, and not affiliated with GitLab Inc. See `NOTICE`.

## Status

Under construction: the release badge above names the newest tag, and
`CHANGELOG.md` says what each one holds. Until the first tag, only the
read tools below are being built; nothing writes yet.
`docs/architecture.md` §16 is the plan.

## Tools

| Tool | What it does |
|---|---|
| `get_me` | Who is signed in, on which instance, with which scopes |
| `resolve_url` | Turn a GitLab web URL into the project, issue, merge request, file or commit it names |
| `search_projects` | Find projects by name, or within a group |
| `get_project` | One project: default branch, visibility, what it has turned on |
| `search_issues` | Find issues across the instance, a group or a project |
| `get_issue` | One issue, its description marked as untrusted content |
| `list_discussions` | The comment threads on an issue or a merge request |
| `search_merge_requests` | Find merge requests across the instance or a project |
| `get_merge_request` | One merge request with its approvals |
| `get_file` | A file at a ref, bounded |
| `list_tree` | A directory listing at a ref |
| `list_branches` | A project's branches |
| `list_commits` | Commits on a ref or a path |
| `get_commit` | One commit and its diff, bounded |

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

1. **Register an application**, once. On GitLab: your avatar → Edit
   profile → Applications (or a group's or the instance's). Redirect URI
   `http://127.0.0.1/callback`, **Confidential unchecked**, scope `api`
   (`read_api` for read-only). `docs/setup.md` has the exact values for
   gitlab.com and for a self-managed instance.
2. **Log in** from a terminal:

   ```bash
   gitlab-mcp login --client-id <application id>
   # self-managed:
   gitlab-mcp login --client-id <application id> --instance https://gitlab.example.com
   ```

   It prints the scopes it will ask for, opens your browser, and stores
   the token in the OS keyring. On a machine without a browser,
   `--no-browser` prints the URL and the `ssh -L` line to forward the
   callback.
3. **Check it**:

   ```bash
   gitlab-mcp doctor
   ```

   `doctor` walks the instance, TLS, the version, the application, the
   granted scopes and one call as you, and names what is missing.
   `status` and `logout` do what they say; `--profile` keeps a gitlab.com
   login and a self-managed one side by side.

Then point your MCP client at the binary:

```json
{
  "mcpServers": {
    "gitlab": { "command": "gitlab-mcp" }
  }
}
```

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
