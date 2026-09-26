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

- `login`, `logout`, `status` and `doctor`, signing in with your own OAuth application through `--client-id`.
- gitlab.com as the one instance, with profiles kept side by side; a sign-in is only ever sent to the instance that issued it.
- Token refresh under a cross-process lock, so two clients share one login.
- Read tools: `get_me`, `resolve_url`, `search_projects`, `get_project`, `search_issues`, `get_issue`, `list_discussions`, `search_merge_requests`, `get_merge_request`, `get_file`, `list_tree`, `list_branches`, `list_commits`, `get_commit`.
- GitLab content rendered inside untrusted-content boundaries, within a reply budget that says what it left out.
- `get_commit` continues a long commit message with `message_offset`, as every cut read can be continued.
- Read-only mode (`GITLAB_MCP_READ_ONLY`) registers only the read tools and requests only `read_api`.
- The quick-action guard every later write goes through.
- Repository gates run by `make check` and CI on Linux, macOS and Windows.
- Signed release archives for six platforms with SBOMs, build provenance, a Claude Desktop bundle and an MCP registry entry.

[Unreleased]: https://github.com/mmedum/gitlab-mcp/compare/b0a78ab...HEAD
