# Setting up sign-in

You bring your own OAuth application. The binary ships no client id, and
there are no personal access tokens and no device flow. Register the
application once, run `gitlab-mcp login` once, and the token lives in
your OS keyring from then on.

The application form takes the same four values everywhere. The blocks
below are generated from `internal/scopes` and `make staleness` compares
them with the code exactly, so what you copy here is what `login` sends.

For the default mode (Read and Write tools, and Ship or Destructive if
you turn them on):

<!-- setup:begin default -->
```text
Name:          gitlab-mcp
Redirect URI:  http://127.0.0.1/callback
Confidential:  unchecked
Scopes:        api
```
<!-- setup:end default -->

For read-only mode (`GITLAB_MCP_READ_ONLY=true`):

<!-- setup:begin read-only -->
```text
Name:          gitlab-mcp
Redirect URI:  http://127.0.0.1/callback
Confidential:  unchecked
Scopes:        read_api
```
<!-- setup:end read-only -->

Why each value is what it is:

- **Redirect URI** is the IP literal with no port. `login` listens on a
  random port of `127.0.0.1`, and GitLab ignores the port when both the
  registered and the requested URI are a loopback IP literal. It does
  not do that for `localhost`, so do not write `localhost`.
- **Confidential unchecked.** GitLab ticks it by default. A confidential
  application needs a secret, which a program on your laptop cannot
  keep, and GitLab answers `invalid_client` when the box is ticked.
- **Scopes** follow the mode alone. GitLab has nothing between
  `read_api` and `api`: `api` also permits merging, approving, running
  pipelines and deleting, whichever flags you set. `docs/security.md`
  says what that means. If you will use both modes, tick both scopes on
  the one application.

Leave "Trusted" and the other boxes as they are unless you are an
administrator (below).

## gitlab.com

1. Open your avatar → **Edit profile** → **Applications**.
2. **Add new application** and fill in the block for your mode.
3. Save. Copy the **Application ID** GitLab shows. There is no secret to
   copy, because the application is not confidential.

## Self-managed

The same steps work on any instance whose administrator has not turned
off user applications: your avatar → **Edit profile** → **Applications**
on your instance. A group owner can register one under the group's
**Settings** → **Applications** instead, for the group's members to
share.

### An administrator registering one application for everyone

On an instance with many users, an administrator can register a single
application and hand out its id:

1. **Admin area** → **Applications** → **New application**.
2. Fill in the block for the mode people will use (tick both scopes if
   some use read-only mode and some do not).
3. Tick **Trusted** if people should not see GitLab's authorization
   screen on each login. Leave **Confidential** unchecked.
4. Publish the **Application ID** internally. It is not a secret, but it
   is not something to paste into a public issue either.

Each person still runs `login` with that id, and each gets a token of
their own that acts as them. Deleting or renewing the application signs
out everyone who uses it.

## Log in

From a terminal, once per machine and profile:

```bash
gitlab-mcp login --client-id <application id>
# a self-managed instance:
gitlab-mcp login --client-id <application id> --instance https://gitlab.example.com
# read-only mode:
gitlab-mcp login --client-id <application id> --read-only
```

`login` prints the profile, the instance and the scopes it will ask
for, opens your browser at GitLab's authorization page, and waits up to
ten minutes for the redirect back. It then stores the token pair in the
OS keyring, reads who you are, and records the instance, the
application id, your username and the scopes GitLab **granted** in the
profile. If GitLab granted less than was asked, it says so.

The instance and application id are remembered, so later runs of the
server need neither. `--profile <name>` keeps a second sign-in beside
the first, such as gitlab.com and a self-managed instance:

```bash
gitlab-mcp login --profile work --client-id <application id> --instance https://gitlab.example.com
```

and the server then runs with `GITLAB_MCP_PROFILE=work`.

Changing `GITLAB_MCP_READ_ONLY` changes the scope, so it needs a new
`login` in the new mode. The server refuses to start with a token that
cannot serve the mode, and says which scope is missing.

## Check it

```bash
gitlab-mcp doctor
```

`doctor` walks what goes wrong at setup, in the order it goes wrong:
the instance, TLS, the sign-in, the version and edition, the
application the token was issued to, the granted scopes, and one call
as you. Each line is `[ok  ]` or `[FAIL]` with what to do. Hostnames,
the application id and your username are masked as `{host 1}`,
`{client-id 1}` and `{user 1}`, so the output is safe to paste into an
issue. `docs/runbook.md` lists each failure and its fix.

`gitlab-mcp status` shows the profile and settings without the walk;
`status --json` is the same for a script.

## Over SSH, or with no browser

The browser has to reach `127.0.0.1` on the machine running `login`.
When that machine is remote, ask for the URL instead of a browser:

```bash
gitlab-mcp login --client-id <application id> --no-browser
```

It prints the authorization URL, the port it is listening on, and the
exact line to forward that port, in this shape:

```text
ssh -L <port>:127.0.0.1:<port> <this-host>
```

Run that line on the machine with the browser, leave it open, then open
the URL there. The port is random per login, so copy the line `login`
printed rather than one from an earlier attempt.

## If login says `invalid_client`

GitLab refused the application as a public client. `login` says:

```text
GitLab refused the application as a public client (invalid_client). Open the application in GitLab (user settings > Applications), untick "Confidential", save, and run `gitlab-mcp login` again. If it is already unticked, check the application id
```

Open the application, untick **Confidential**, save, and log in again.
Unticking it does not change the application id. If the box was already
unticked, the id you passed is wrong or belongs to another instance.

## Removing it

```bash
gitlab-mcp logout
```

revokes the token at GitLab and deletes it from the keyring, the
fallback file and the profile. If other profiles use the same
application, `logout` names them first: deleting the application in
GitLab would sign them out too.
