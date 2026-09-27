# Security policy

## Reporting a vulnerability

Report security issues privately through GitHub's
[security advisory](https://github.com/mmedum/gitlab-mcp/security/advisories/new)
form, not in a public issue.

Say what you did, what happened, and what you expected. Describe the
shape of the problem rather than pasting from your instance.

**Never paste** a token of any kind (anything starting `glpat-`,
`gloas-` or another GitLab prefix, or an OAuth access or refresh
token), your OAuth application id, an instance hostname, a group or
project path, a username or email address, issue or merge request text,
file contents or a job log. A tool result is your organization's code
and other people's words.

`doctor`, `status`, `--version` and a debug log are safe to include.

You should get an acknowledgment within a week.

## Scope

This server runs locally, speaks MCP over stdio, and acts as one user
against the GitLab REST API with that user's own OAuth application.
There is no hosted component.

In scope and worth reporting:

- Anything that puts instance content, a path, a search term or a token
  into a log, an error message, a fixture or this repository.
- Content the server presents as something other than untrusted data,
  or a tool description that tells the model to act on what content
  says.
- A body sent to GitLab that runs a quick action.
- A commit that reaches the default branch or a protected branch
  without a merge request.
- A Ship tool registered without `GITLAB_MCP_ENABLE_SHIP`, a
  Destructive tool registered without `GITLAB_MCP_ENABLE_DESTRUCTIVE`,
  or a deletion without `confirm: true`.
- A create that is retried, or a followed redirect.
- Anything that lets stdout carry something other than a JSON-RPC frame.
- A dependency vulnerability `make vuln` does not catch.

## What the design already assumes

Tool annotations are not a control. A tool that must not run unattended
is not registered unless its flag is set. Do not report "the client did
not prompt"; do report a tool that is registered while its flag is off.

The default `api` token can merge, approve and run pipelines. GitLab has
no narrower scope that allows writing issues but not merging. Leaving
Ship unregistered stops this server doing so; it does not stop anything
else holding the token, which is why the token lives in the OS keyring.
`docs/security.md` says more.

## Verifying a release

Each release's `checksums.txt` is signed with a keyless Sigstore
certificate, and every archive and the bundle carry build provenance.
The release notes and `README.md` give the exact commands.
