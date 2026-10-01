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

- **A small surface, gated by registration.** About sixty tools rather
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

Stable: the release badge above names the newest tag, and
`CHANGELOG.md` says what each one holds. The tool surface is a
contract: tools keep their names and output fields.
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
| `get_issue` | One issue, its description marked as untrusted content, and its linked merge requests |
| `create_issue` | Create an issue; labels, assignees and milestone checked first |
| `update_issue` | Change an issue's fields, refused if it changed since you read it |
| `list_discussions` | The comment threads on an issue or a merge request |
| `list_item_events` | An issue's or a merge request's label, state, milestone and weight changes, newest first |
| `add_comment` | Comment on an issue or merge request, reply in a thread, or start one, on a diff line or not |
| `update_comment` | Edit one of your own comments in place, in its thread and on its diff line |
| `resolve_discussion` | Resolve or reopen a thread on a merge request or an issue |
| `link_issues` | Link two issues, in one project or two |
| `unlink_issues` | Remove the link between two issues |
| `search_merge_requests` | Find merge requests across gitlab.com or a project |
| `get_merge_request` | One merge request with its approvals and linked issues |
| `list_mr_files` | The files a merge request changes, with line counts and GitLab's markers |
| `get_mr_diff` | A merge request's diffs, file by file, bounded |
| `list_mr_commits` | A merge request's commits |
| `create_merge_request` | Open a merge request from a branch |
| `update_merge_request` | Change a merge request's fields, refused if it changed since you read it |
| `track_time` | Set or reset an issue's or a merge request's time estimate, and add or reset its time spent |
| `add_review_comment` | A draft review comment, on the merge request or a diff line |
| `list_review_comments` | Your unpublished review comments on a merge request |
| `delete_review_comment` | Delete one of your drafts |
| `submit_review` | Publish all your drafts at once, with a summary and reviewer state |
| `get_file` | A file at a ref, bounded |
| `list_tree` | A directory listing at a ref |
| `list_branches` | A project's branches |
| `list_commits` | Commits on a ref or a path |
| `get_commit` | One commit and its diff, bounded, and the merge requests that contain it |
| `compare_refs` | The commits and diffs between two refs, bounded |
| `list_tags` | A project's tags |
| `create_branch` | Create a branch from a ref |
| `create_commit` | Commit file changes to a branch; never the default or a protected one |
| `get_blame` | Who last changed each line of a file, bounded |
| `cherry_pick_commit` | Apply a commit to a branch; never the default or a protected one |
| `revert_commit` | Undo a commit on a branch with a new one; never the default or a protected one |
| `list_pipelines` | A project's CI pipelines |
| `get_pipeline` | One pipeline with the jobs that failed, trigger jobs included |
| `list_jobs` | A pipeline's jobs |
| `get_job_log` | A window of a job's log, secrets masked; the failing section on request |
| `get_test_report` | A pipeline's test counts and failed cases, secrets masked, bounded |
| `lint_ci` | Check a project's CI configuration at a ref, or configuration you pass |
| `list_job_artifacts` | The files a job kept as artifacts |
| `get_job_artifact` | One text file of a job's artifacts, secrets masked, bounded |
| `list_labels` | The labels a project's issues and merge requests can carry |
| `list_milestones` | A project's or a group's milestones |
| `list_boards` | A project's issue boards and their lists, each with the `search_issues` arguments that read it |
| `search` | Code, commits, comments and more, in a project, a group or everywhere |
| `list_todos` | Your to-do items |
| `mark_todos_done` | Mark your to-do items done |
| `add_todo` | Add a to-do for yourself on an issue or a merge request |
| `subscribe` | Subscribe to an issue's or a merge request's notifications, or unsubscribe |
| `merge_merge_request` | Merge at the head you reviewed, or when the pipeline succeeds (Ship) |
| `approve_merge_request` | Approve at the head you reviewed (Ship) |
| `unapprove_merge_request` | Withdraw your approval (Ship) |
| `rebase_merge_request` | Rebase a merge request's source branch, from the head you reviewed (Ship) |
| `move_issue` | Move an issue to another project, never to one more people can see (Ship) |
| `run_pipeline` | Run a pipeline for a ref, with variables whose values are never shown (Ship) |
| `run_merge_request_pipeline` | Run a merge request's pipeline, merged results or detached as GitLab picks; not for a fork's merge request (Ship) |
| `retry_pipeline` | Retry a pipeline's failed and canceled jobs (Ship) |
| `retry_job` | Run a finished job again, with inputs (Ship) |
| `play_job` | Start a manual job, with variables and inputs (Ship) |
| `cancel_pipeline` | Cancel a running pipeline (Ship) |
| `delete_branch` | Delete a branch; never the default, a protected or an unmerged one unless asked (Destructive) |
| `delete_comment` | Delete one of your own comments (Destructive) |
| `list_wiki_pages` | A project wiki's pages (`wiki` toolset) |
| `get_wiki_page` | One wiki page, bounded (`wiki` toolset) |
| `save_wiki_page` | Create a wiki page, or change one unchanged since you read it (`wiki` toolset) |
| `delete_wiki_page` | Delete a wiki page (`wiki` toolset, Destructive) |
| `list_snippets` | A project's snippets, or your own (`snippets` toolset) |
| `get_snippet` | One snippet and a file of it, bounded (`snippets` toolset) |
| `create_snippet` | Create a snippet, always private (`snippets` toolset) |
| `update_snippet` | Change one of your own private snippets: title, description, files; refused if it changed since you read it (`snippets` toolset) |
| `delete_snippet` | Delete one of your own snippets (`snippets` toolset, Destructive) |
| `list_releases` | A project's releases (`releases` toolset) |
| `get_release` | One release and its notes (`releases` toolset) |
| `create_release` | Create a release, and its tag at a ref, with asset links to the project's own pages (`releases` toolset, Ship) |
| `create_tag` | Create a tag at a ref; never a protected one (`releases` toolset) |
| `delete_tag` | Delete a tag; never a protected one (`releases` toolset, Destructive) |
| `create_label` | Create a project label (`planning` toolset) |
| `update_label` | Change a project label unchanged since you read it (`planning` toolset) |
| `delete_label` | Delete a project label (`planning` toolset, Destructive) |
| `create_milestone` | Create a project milestone (`planning` toolset) |
| `update_milestone` | Change, close or reopen a milestone unchanged since you read it (`planning` toolset) |
| `delete_milestone` | Delete a project milestone (`planning` toolset, Destructive) |
| `list_environments` | A project's environments and their last deployment (`deployments` toolset) |
| `list_deployments` | What was deployed where, and by which job (`deployments` toolset) |
| `list_events` | Recent activity, yours or a project's (`activity` toolset) |

Ship and Destructive tools are registered only with the settings below,
and the six toolsets only when `GITLAB_MCP_TOOLSETS` names them.

Three resources carry the same text for clients that attach rather
than call: an issue, a merge request and a job log.

## Install

Download an archive for your platform from the
[releases](https://github.com/mmedum/gitlab-mcp/releases) page, or:

```bash
go install github.com/mmedum/gitlab-mcp/v2/cmd/gitlab-mcp@latest
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

When your MCP client supports elicitation, the server also asks you
before it merges, approves, runs a manual job or a pipeline on a
protected ref or for a merge request between protected branches, publishes a release or a tag, makes a confidential issue
public, or deletes anything. Only your accept writes.

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
