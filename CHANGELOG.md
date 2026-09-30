# Changelog

All notable changes to this project are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this
project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

The versioning contract is the MCP tool surface: tool names, input and
output schemas, resources, and documented behavior. Package layout, log
lines and error wording outside the `[class]` prefix are not part of it.

Sections appear in this order: Added, Changed, Deprecated, Removed,
Fixed, Security. One line per change. A change that needs the reader to
act starts with **Breaking:**. The section for a tag is its release note,
lifted verbatim.

## [Unreleased]

### Added

- `get_issue` lists the merge requests related to the issue and those that close it, `get_merge_request` the issues it closes and mentions, and `get_commit` the merge requests that contain the commit: the first 20 of each, as GitLab returns them.
- `get_test_report` reads a pipeline's test report: the counts, each suite's, and the failed and errored cases with their output, secrets masked, 40,000 characters at a time.
- `list_item_events` reads an issue's or a merge request's change history, newest first: labels added and removed, state changes with the commit or merge request behind them, the milestone and an issue's weight.
- `list_boards` lists a project's issue boards and their lists in board order, with the `search_issues` arguments that read each list, and describes the Open and Closed lists GitLab does not return.

## [2.0.0] - 2026-09-29

### Added

- **Breaking:** Before a merge, an approval, `play_job`, `create_release`, `create_tag`, a `run_pipeline` on the default branch or a protected ref, an `update_issue` that makes a confidential issue public, and every delete, the server asks you through the MCP client (form elicitation) when the client supports it; only an accept writes, so a client that declares elicitation and answers with nobody there can no longer make these writes.
- `GITLAB_MCP_REQUIRE_PROMPT` (`--require-prompt`) refuses those writes as `[blocked]` when the client cannot ask you.
- `update_comment` edits one of your own comments on an issue or a merge request in place, keeping its thread, replies and diff position.
- `add_comment` returns the new comment's `updated_at`, which `update_comment` takes as its witness.

### Changed

- **Breaking:** the Go module path is now `github.com/mmedum/gitlab-mcp/v2`, as Go requires from v2 on; install with `go install github.com/mmedum/gitlab-mcp/v2/cmd/gitlab-mcp@latest`.

### Fixed

- A server no longer refreshes the sign-in before it serves when a login recorded its scopes, so a host that kills it during startup no longer signs the profile out.
- A server that is stopped waits for a sign-in refresh in progress to be stored before it exits.
- A refresh no longer spends the refresh token when the stored sign-in cannot be read again under the lock.

## [1.1.0] - 2026-09-27

### Added

- `update_label` takes `clear_description` and `clear_priority`, and `update_milestone` takes `clear_description`, `clear_start_date` and `clear_due_date`.

## [1.0.0] - 2026-09-27

### Added

- `login`, `logout`, `status` and `doctor`, signing in with your own OAuth application through `--client-id`.
- gitlab.com as the one instance, with profiles kept side by side; a sign-in is only ever sent to the instance that issued it.
- Token refresh under a cross-process lock, so two clients share one login.
- Read tools: `get_me`, `resolve_url`, `search_projects`, `get_project`, `search_issues`, `get_issue`, `list_discussions`, `search_merge_requests`, `get_merge_request`, `get_file`, `list_tree`, `list_branches`, `list_commits`, `get_commit`.
- GitLab content rendered inside untrusted-content boundaries, within a reply budget that says what it left out.
- `get_commit` continues a long commit message with `message_offset`, as every cut read can be continued.
- Review reads: `list_mr_files`, `get_mr_diff`, `list_mr_commits` and `list_review_comments`, your own unpublished drafts.
- History reads: `compare_refs` and `list_tags`.
- CI reads: `list_pipelines`, `get_pipeline` with its failed jobs, `list_jobs`, `get_job_log` and `lint_ci` at a ref.
- `get_job_log` reads a log in byte windows, the tail by default or the failing section, cleaned of colors and section markers, with token and key shapes masked.
- Planning and navigation reads: `list_labels`, `list_milestones`, `list_members`, `find_users`, `list_todos` and `search`.
- Resources for an issue, a merge request and a job log, carrying the same text as their tools.
- `resolve_url` names `get_pipeline`, `get_job_log` and `compare_refs` for pipeline, job and compare links.
- Read-only mode (`GITLAB_MCP_READ_ONLY`) registers only the read tools and requests only `read_api`.
- The quick-action guard every later write goes through.
- Write tools: `create_issue`, `update_issue`, `add_comment`, `resolve_discussion`, `create_merge_request`, `update_merge_request`, `create_branch`, `create_commit` and `mark_todos_done`.
- Reviews in drafts: `add_review_comment`, `delete_review_comment` and `submit_review`, which publishes every draft at once; an approving review needs `GITLAB_MCP_ENABLE_SHIP=true`.
- Inline comments land where asked: the server computes GitLab's diff position from a file, line and side, and reports where the comment landed.
- A line GitLab would run as a quick action refuses the write, or is sent as text with `escape_commands`.
- Every write takes `dry_run`, and names the project's visibility in its result.
- Updates to issues and merge requests require the `updated_at` you read, and are refused `[stale]` if it moved.
- `update_issue` makes a confidential issue public only with `GITLAB_MCP_ENABLE_SHIP=true`.
- `create_commit` refuses the default branch and every protected branch, and `create_branch` refuses a name a protected-branch rule covers; code reaches them through a merge request.
- A create whose answer is lost is never repeated: the server reads to say whether it happened.
- `GITLAB_MCP_WRITE_NAMESPACES` confines writes to the groups and projects it names.
- `lint_ci` checks configuration you pass, before it is committed; it may not use `include:`, since GitLab fetches what an include names.
- Ship tools, registered only with `GITLAB_MCP_ENABLE_SHIP=true`: `merge_merge_request`, `approve_merge_request` and `unapprove_merge_request`, which take the head `sha` you reviewed, and `run_pipeline`, `retry_pipeline`, `retry_job`, `play_job` and `cancel_pipeline`.
- Pipeline and job variables are sent and never shown: a result names their keys.
- Destructive tools, registered only with `GITLAB_MCP_ENABLE_DESTRUCTIVE=true` and refused without `confirm: true`: `delete_branch`, which refuses the default, protected and unmerged branches, and `delete_comment`, for your own comments only.
- The `wiki` toolset: `list_wiki_pages`, `get_wiki_page`, `save_wiki_page` and `delete_wiki_page`; a change carries a hash of the content you read.
- The `snippets` toolset: `list_snippets`, `get_snippet` and `create_snippet`, which only creates private snippets.
- The `releases` toolset: `list_releases`, `get_release` and `create_release`, which is Ship.
- The `deployments` toolset (`list_environments`, `list_deployments`) and the `activity` toolset (`list_events`).
- `resolve_url` names `get_wiki_page` for a wiki page's link, and `list_wiki_pages` for the wiki's index, when the `wiki` toolset is on.
- `make evals` scores a model against the tool surface, including four instructions planted in content that it must not follow.
- `get_job_log` reads only the window it shows, so a log of any size can be read, and each window no longer downloads the whole log.
- `get_mr_diff` reads from the page that holds `file_offset`, so paging through a large merge request reads each page about once.
- `get_project` continues a long description with `offset`.
- `get_mr_diff`, `get_commit` and `compare_refs` continue one file's diff larger than the budget with `diff_offset`.
- `get_pipeline` names its failed trigger jobs and the downstream pipeline each started.
- `add_comment` starts a resolvable thread with `thread`, and `resolve_discussion` resolves issue threads with `type: issue`.
- A lost comment is settled however many threads were started after it.
- `add_review_comment` reports the `line_code` GitLab computed for a draft on a line.
- `retry_job` and `play_job` take values for the inputs a job declares.
- `create_release` takes asset links, only to the project's own pages and packages.
- `link_issues` and `unlink_issues` relate two issues, in one project or two.
- `move_issue` moves an issue to another project, never to one more people can see; it is Ship.
- `rebase_merge_request` rebases a merge request's source branch from the head you reviewed; it is Ship.
- `cherry_pick_commit` and `revert_commit` commit to a branch, never the default or a protected one, and their dry run says whether the change applies.
- `get_blame` shows who last changed each line of a file.
- `list_job_artifacts` and `get_job_artifact` read a job's artifacts, one text file at a time, with secrets masked.
- The `planning` toolset: `create_label`, `update_label`, `delete_label`, `create_milestone`, `update_milestone` and `delete_milestone`; a label carries a `version` its writes take.
- The `releases` toolset gains `create_tag`, which refuses protected names, and `delete_tag`.
- Repository gates run by `make check` and CI on Linux, macOS and Windows.
- Signed release archives for six platforms with SBOMs, build provenance, a Claude Desktop bundle and an MCP registry entry.

[Unreleased]: https://github.com/mmedum/gitlab-mcp/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/mmedum/gitlab-mcp/compare/v1.1.0...v2.0.0
[1.1.0]: https://github.com/mmedum/gitlab-mcp/releases/tag/v1.1.0
[1.0.0]: https://github.com/mmedum/gitlab-mcp/releases/tag/v1.0.0
