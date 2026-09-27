# Runbook

What goes wrong at setup and after, how to recognize it, and the fix.
Start with:

```bash
gitlab-mcp doctor
```

`doctor` checks, in order: the instance (always gitlab.com), TLS, the
sign-in, the version and edition, the application the token was issued to, the granted
scopes, and one call to `/user`. It stops at the first failure a later
check depends on. Each check prints a line, and a failure prints what
it found beneath it:

```text
[ok  ] instance
       https://gitlab.com (gitlab.com)
[FAIL] TLS
       the instance could not be reached: lookup: no such host

1 problem(s).
redacted: nothing
```

The sections below are keyed by the check that fails. Run `doctor`
with the same flags and environment your MCP client gives the server:
the client may not pass your shell's environment, and `doctor` checks
what it is given. `gitlab-mcp status` shows the settings it read.

Tool errors read `[class] message`. `[auth]` means the sign-in,
`[forbidden]` your role or a project rule, and `[unsupported]` a route
your account's tier lacks. `docs/architecture.md` §6.5 lists every class.

## Login fails with `invalid_client`

```text
gitlab-mcp: login failed: GitLab refused the application as a public client (invalid_client). Open the application in GitLab (user settings > Applications), untick "Confidential", save, and run `gitlab-mcp login` again. If it is already unticked, check the application id
```

The application was saved with **Confidential** ticked, which GitLab
does by default. Open it in GitLab, untick Confidential, save, and log
in again; the application id does not change. If it was already
unticked, the id is wrong: check `--client-id`.

If it starts happening to a sign-in that worked, somebody ticked the
box since. `doctor` then fails on `sign-in` with the same text.

## The browser shows "The redirect URI included is not valid"

GitLab refused the redirect before asking you anything, so `login`
waits until it gives up with `auth: timed out waiting for the browser`.
`doctor` has no sign-in to check and fails on `sign-in`; when no
application id is known it prints the application block to compare
with.

The registered redirect URI must be exactly `http://127.0.0.1/callback`:
the IP literal, `http`, no port, that path. `localhost` does not work,
because GitLab ignores the port only for a loopback IP literal. Edit the
application and fix the URI; the id stays the same. `docs/setup.md` has
the block.

## A scope is missing

After changing `GITLAB_MCP_READ_ONLY` from `true` to off, the stored
token has `read_api` and the configuration needs `api`. The server
refuses to start:

```text
[auth] profile "default" was granted read_api, and this configuration needs api: run `gitlab-mcp login` to sign in again, or set GITLAB_MCP_READ_ONLY=true to serve the read tools only
```

and `doctor` fails on `granted scopes`:

```text
[FAIL] granted scopes
       granted read_api; not granted: api
       a setting that changes scopes needs `gitlab-mcp login` again; grant every scope asked for
```

Log in again in the mode you want. If GitLab's authorization page then
says the requested scope is invalid, the application does not carry
that scope: edit it, tick the scope, and log in again.

`login` itself warns when GitLab granted less than it asked for, and
`status` names the missing scope under `scopes needed` and exits 1.

Going the other way, an `api` token covers read-only mode, so nothing
fails. `docs/security.md` says why you may still want a `read_api`
token.

## The sign-in was revoked, or rotated by another process

```text
[auth] the sign-in was revoked or has expired: run `gitlab-mcp login`
```

`doctor` fails on `sign-in` with the same text. GitLab revokes a refresh
token the moment it is used. Processes on the same profile — Claude
Desktop and Claude Code, say — refresh in turn under a lock in the
profile directory, and one whose refresh is refused re-reads the store
for the pair another process wrote before it gives up. So this message
means the token really is dead: revoked in GitLab, the application
renewed or deleted, or a login elsewhere replaced it. Run
`gitlab-mcp login`.

Related messages:

- `[unavailable] another gitlab-mcp process is refreshing the sign-in
  and has not finished; try again`: the lock was held too long. Try
  again; if it persists, a process is stuck.
- `the profile records a token in the OS keyring and the keyring
  returned nothing`: the keyring is locked or unreachable, not empty.
  Unlock it. Logging in again works around it and writes a second token
  beside the first.
- `[FAIL] application`, `the token was issued to {client-id 2}, not to
  {client-id 1}`: `GITLAB_MCP_CLIENT_ID` names a different application
  from the one the stored token came from. Unset it, or log in with it.
- `[FAIL] /user`, `signed in as {user 2}, and the last login recorded
  {user 1}`: the token acts as someone else. Log in again.
- `warning: ... token.json ...` on every start: the keyring was
  unavailable at login and the token is in a file. It works; the
  warning is deliberate. Log in again once a keyring is available.

## gitlab.com cannot be reached

```text
[FAIL] TLS
       the instance could not be reached: lookup: no such host
```

or `dial: ... connection refused`, or `timed out`. Behind a proxy, set
`HTTPS_PROXY` (and `NO_PROXY` for anything that must bypass it). Set
them in the MCP client's `env` block too: the client starts the server
with its own environment, and a proxy that works in your shell may be
missing there. `doctor` run from the shell passing while the client
fails is the sign.

If instead `doctor` says the certificate is not trusted, something
between you and gitlab.com presented its own: a proxy that inspects
TLS needs its authority in this machine's trust store.

## The token is withheld: another instance

```text
[FAIL] sign-in
       profile "default" is signed in to another instance than gitlab.com; run `gitlab-mcp login` to sign it in to this one
```

Every tool answers `[auth]` with the same text. The profile's token
was issued by another instance, and a token is only ever sent to the
instance that issued it. The profile was made by a build that served
other instances, or against the test instance of
`docs/development.md`. Run `gitlab-mcp login` to sign it in to
gitlab.com.

## Not signed in

```text
[FAIL] sign-in
       no OAuth application id: register an application in GitLab, then run `gitlab-mcp login --client-id <Application ID>` ...
```

followed by the application block, or `not signed in: run gitlab-mcp
login`. The server still starts, lists its tools, and answers every
call with `[auth]` until you log in. `docs/setup.md` is the whole
procedure. Check that the client runs the server with the same profile
and config directory you logged in with.

## Rate limited

```text
[rate_limited] GitLab is rate limiting this account ...
```

In `doctor` it shows on `/user`. The server already waited: it retries
a 429 up to four attempts, honoring `Retry-After`, within the call's
deadline. gitlab.com limits notes to 60 a minute. Wait, and ask the
model to make fewer calls. `get_me` reports the last rate-limit reading
it saw.

## `[unsupported]`, or a tool is missing

```text
[unsupported] GitLab has no API route for ... here: it may need an edition or tier this account lacks
```

Some routes exist only in a paid tier, and a call to one from a Free
namespace answers `[unsupported]`. gitlab.com runs the newest release,
so no tool is left out for its version; `doctor`'s `version and
edition` line is information.

A tool can also be missing because a setting leaves it unregistered:
read-only mode, Ship and Destructive off, a toolset not named.
`docs/configuration.md` says which setting adds what.

## `[forbidden]`

You are signed in, and your role in the project or a project rule
refuses the action. The server cannot do more than your account can.
Check your role in the project.

## Reporting a problem

Use the bug form. Paste `doctor` and `status` output, `--version`, and
a log at `GITLAB_MCP_LOG_LEVEL=debug`: all mask or leave out what
identifies you. Never paste a token, a tool result or anything from
your projects; `docs/security.md` has the full list.
