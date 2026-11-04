# Troubleshooting

What to do when something does not work, then the maintenance tasks.
Start with these two commands:

```bash
gitlab-mcp status
gitlab-mcp doctor
```

`status` prints the profile, the config directory, where the token is
stored, the scopes granted and needed, and the switches in effect, then
one call as you (`--no-probe` skips it). `doctor` checks, in order, the instance,
TLS, the sign-in, the version and edition, the application the token was
issued to, the granted scopes, and one call to `/user`. It stops at the
first failure a later check depends on, and each failure says what to
do:

```text
[ok  ] instance
       https://gitlab.com (gitlab.com)
[FAIL] TLS
       the instance could not be reached: lookup: no such host

1 problem(s).
redacted: nothing
```

Run both with the flags and environment your MCP client gives the
server. The client may not pass your shell's environment, and each
command checks what it is given.

## `GitLab refused the application as a public client (invalid_client)`

`login` fails with this, and `doctor` fails `sign-in` the same way.

**Cause:** the application was saved with **Confidential** ticked, which
GitLab does by default. A sign-in that worked and starts failing means
somebody ticked it since.

**Fix (resolves it):** open the application in GitLab (user settings →
Applications), untick **Confidential**, save, and run `login` again. The
application id does not change. If the box was already unticked, the id
is wrong: check `--client-id`.

## `The redirect URI included is not valid`

GitLab shows this in the browser before asking you anything, so `login`
waits until it gives up with `auth: timed out waiting for the browser`.

**Cause:** the registered redirect URI is not exactly
`http://127.0.0.1/callback`. GitLab ignores the port only for a loopback
IP literal, so `localhost` does not work.

**Fix (resolves it):** edit the application and set the URI to the IP
literal, `http`, no port and that path, as
[`docs/setup.md`](setup.md) shows. The id stays the same.

## `auth: timed out waiting for the browser`

**Cause:** the browser could not reach the callback. Over SSH, the
callback goes to the loopback interface of the remote machine, while the
browser runs on yours. A wrong redirect URI ends the same way.

**Fix (resolves it):** run `login --no-browser`. It prints the
authorization URL and the exact `ssh -L` line to run first, with the
port it picked.

## `[auth] profile "default" was granted read_api, and this configuration needs api`

The server refuses to start, and `doctor` fails `granted scopes` with
`granted read_api; not granted: api`.

**Cause:** the token was granted under another configuration.
`GITLAB_MCP_READ_ONLY` changes the scope, and a token never gains one.
An `api` token covers read-only mode, so only this direction fails.

**Fix (resolves it):** run `login` again in the mode you want. If
GitLab's authorization page then says the scope is invalid, the
application does not carry it: edit the application, tick the scope,
and log in again.

## `[auth] the sign-in was revoked or has expired: run gitlab-mcp login`

`doctor` fails `sign-in` with the same text.

**Cause:** the token is dead. It was revoked in GitLab, the application
was renewed or deleted, or a login elsewhere replaced it. Processes on
one profile refresh in turn under a lock, and a refused refresh re-reads
the store first, so a second client is not the cause.

**Fix (resolves it):** run `gitlab-mcp login`.

## `[unavailable] another gitlab-mcp process is refreshing the sign-in and has not finished; try again`

**Cause:** another process on the same profile held the refresh lock too
long.

**Fix:** try again. If it persists, a process is stuck: stop the other
clients using this profile.

## `the profile records a token in the OS keyring and the keyring returned nothing`

**Cause:** the keyring holds the token and will not hand it over. It is
more likely locked, or its session bus unreachable, than empty.

**Fix (resolves it):** unlock the keyring and try again.

**Workaround:** `login` writes a fresh token beside the first, which
leaves the cause in place.

## `keyring unavailable (…); token saved in plaintext at …`

Every later use warns `token read from the plaintext file`.

**Cause:** the OS keyring could not be reached at login, for example on
a headless Linux machine with no Secret Service running.

**Fix (resolves it):** start a keyring, or log in from a desktop
session, and run `login` again. The token moves to the keyring and the
file is deleted.

**Impact:** `token.json` is restricted to your account (`0600`, or an
ACL on Windows). Anyone who can read it holds an `api` token, which can
merge, approve and run pipelines as you, until you revoke it.

## `the token was issued to {client-id 2}, not to {client-id 1}`

`doctor` fails `application` with this.

**Cause:** `GITLAB_MCP_CLIENT_ID` names a different application from the
one the stored token came from.

**Fix (resolves it):** unset it, or run `login` with that application.

## `signed in as {user 2}, and the last login recorded {user 1}`

`doctor` fails `/user` with this.

**Cause:** the token acts as someone other than the account the profile
recorded, such as after a login as another account in the same profile.

**Fix (resolves it):** run `login` again, or keep each account in its
own profile ([Two accounts on one machine](#two-accounts-on-one-machine)).

## `the instance could not be reached`

`doctor` fails `TLS`, with `lookup: no such host`, `connection refused`
or `timed out` after it.

**Cause:** no route to gitlab.com from where the server runs. Behind a
proxy, the MCP client starts the server with its own environment, and a
proxy that works in your shell may be missing there. `doctor` passing in
your shell while the client fails is the sign.

**Fix (resolves it):** set `HTTPS_PROXY`, and `NO_PROXY` for what must
bypass it, in the client's `env` block too.

## `the instance's certificate is not trusted`

**Cause:** something between you and gitlab.com presented its own
certificate, such as a proxy that inspects TLS.

**Fix (resolves it):** add that proxy's authority to this machine's
trust store. The server trusts the system's authorities only.

## `profile "default" is signed in to another instance than gitlab.com`

Every tool answers `[auth]` with the same text, and `doctor` fails
`sign-in`.

**Cause:** the profile's token was issued by another instance, and a
token is only ever sent to the instance that issued it. The profile was
made by a build that served other instances, or against the test
instance.

**Fix (resolves it):** run `gitlab-mcp login` to sign it in to
gitlab.com.

## `no OAuth application id`

`doctor` fails `sign-in` and prints the application block to register;
a profile with an application but no token says `not signed in: run
gitlab-mcp login`.

**Cause:** this profile has never logged in, or `logout` forgot it.

**Fix (resolves it):** register the application and log in, as
[`docs/setup.md`](setup.md) says. Check that the client runs the server
with the same profile and config directory you logged in with. The
server still starts and lists its tools; every call answers `[auth]`
until you log in.

## `GITLAB_MCP_REQUIRE_PROMPT was removed`

The server refuses to start.

**Cause:** the server asks the person nothing, and your client's
approval decides each call. It refuses this setting when it is set to
anything but `false`, rather than ignore it.

**Fix (resolves it):** unset it. For a client that runs with nobody
watching, leave `GITLAB_MCP_ENABLE_SHIP` and
`GITLAB_MCP_ENABLE_DESTRUCTIVE` off.

## `[rate_limited] GitLab is rate limiting this account`

**Cause:** the account's per-minute limit is spent. The server already
waited: it retries a 429 up to four attempts, honoring `Retry-After`,
within the call's deadline. gitlab.com allows 60 notes a minute, and
fewer requests on a Free or Premium plan.

**Fix:** wait a minute, and ask the model to make fewer calls. `get_me`
reports the last rate-limit reading it saw.

## `[unsupported] GitLab has no API route for … here`

**Cause:** the route exists only in a tier the namespace lacks, so a
call from a Free namespace answers `[unsupported]`. gitlab.com runs the
newest release, so no tool is left out for its version.

**Fix (resolves it):** use a project in a namespace on the tier the
route needs. `doctor`'s `version and edition` line is information.

## A tool you expect is missing

**Cause:** a setting leaves it unregistered. `status` shows which are
on.

- `GITLAB_MCP_READ_ONLY=true` removes every write.
- Merging, approving, rebasing, applying suggestions, moving an issue,
  running CI and creating a release need `GITLAB_MCP_ENABLE_SHIP=true`.
- Deletion and `make_issue_public` need
  `GITLAB_MCP_ENABLE_DESTRUCTIVE=true`.
- The wiki, snippet, release, planning, deployment and activity tools
  need their toolset in `GITLAB_MCP_TOOLSETS`.

**Fix (resolves it):** set the switch as
[`docs/configuration.md`](configuration.md) shows, and restart the
server.

## `[forbidden] GitLab refused … for this account`

**Cause:** you are signed in, and your role in the project or a project
rule refuses the action. The server cannot do more than your account
can.

**Fix:** check your role in the project and its rules.

## `[auth] GitLab refused … for this account without calling the token invalid`

**Cause:** GitLab answers 401, not 403, for an action the role does not
allow, such as a merge. The token is fine, and a new one changes
nothing.

**Fix:** check your role and the project's merge rules. If every call is
refused this way, run `gitlab-mcp login`.

## `[not_found] … was not found, or you do not have access to it`

**Cause:** GitLab answers 404 both for what does not exist and for what
the account cannot see.

**Fix:** check the project path or id and the `iid`, then that your
account can open it in GitLab. `resolve_url` turns a GitLab link into
the arguments the tools take.

## `[blocked] GitLab would run a quick action in this text`

**Cause:** a line of the body is one GitLab would run as a command, such
as `/close` or `/merge`. Nothing was sent.

**Fix (resolves it):** to send the lines as text, pass
`escape_commands: true`, which adds a leading backslash that renders the
same. To make the change the line describes, use the tool for it.

## `[blocked] writes are confined by GITLAB_MCP_WRITE_NAMESPACES`

**Cause:** the project is outside every group and project that setting
names. Nothing was sent.

**Fix (resolves it):** if the write is wanted, add its group to the
setting and restart the server.

## `[stale] the … you passed is no longer current: read it again and retry`

Other `[stale]` messages say what moved, such as a branch's head or a
label's version.

**Cause:** the item changed after the read whose witness you passed.

**Fix (resolves it):** read it again, check what changed, and apply your
change to that. Nothing was written.

## `[ambiguous_outcome] GitLab did not confirm whether … took effect`

**Cause:** the request left, and the answer did not come back. Where a
read can settle it, the server has already read, and the result says
whether it happened.

**Fix:** read before doing anything again. The server never repeats a
create by itself.

**Impact:** a second call can create a duplicate comment, issue, merge
request, commit or pipeline, because GitLab does not deduplicate them.

## Tasks

### Rotating the token

```bash
gitlab-mcp logout
gitlab-mcp login --client-id <application id>
```

`logout` forgets the profile, the application id included, so `login`
needs it again. GitLab also rotates the refresh token on every refresh.

### Revoking access

```bash
gitlab-mcp logout
```

It revokes the token at GitLab, then deletes the stored token and the
profile. It names the other profiles that use the same application,
because renewing or deleting that application signs them out too. A
token in `GITLAB_MCP_REFRESH_TOKEN` is out of its reach. If the revoke
fails, revoke the application's access in GitLab under user settings →
Applications → Authorized applications.

### Suspected exposure

Treat a leaked token as access to everything your account can do in
every project it reaches: the `api` scope can merge, approve and run
pipelines, whatever this server's switches say. A copied home directory,
a shared machine or a `token.json` in a backup counts.

1. Run `gitlab-mcp logout` on any machine that still has it.
2. Renew or delete the application in GitLab. That cuts off every
   token it ever issued, and signs out every profile that uses it.
3. Check your activity in GitLab for merges, approvals, pipelines,
   comments and pushes you did not make.
4. Register again if you deleted the application, and log in
   ([`docs/setup.md`](setup.md)).

### A token that cannot write

Turning `GITLAB_MCP_READ_ONLY` on keeps an `api` token working. To hold
a `read_api` token instead:

```bash
gitlab-mcp logout
gitlab-mcp login --read-only --client-id <application id>
```

### Two accounts on one machine

Profiles keep separate tokens and accounts:

```bash
gitlab-mcp login --profile work --client-id <application id>
```

Then give the client entry `GITLAB_MCP_PROFILE=work`, as
[`docs/configuration.md`](configuration.md) shows.

### Reporting a problem

Use the bug form. Include the `doctor` and `status` output, the version
(`gitlab-mcp --version`), how you installed it, which client you use,
and the `GITLAB_MCP_*` switches you set. For a failing call, turn the
log level up first:

```bash
GITLAB_MCP_LOG_LEVEL=debug gitlab-mcp doctor
```

Logs carry no hosts, paths, titles, bodies or usernames, ids are cut to
six characters, and `doctor` and `status` mask the host, the
application id and your username, so all of these are safe to paste.
**Do not paste a token or a tool result**: a result is your
organization's code and other people's words. Describe it instead.
