# Configuration

Every setting is an environment variable with the `GITLAB_MCP_` prefix,
and most also have a command-line flag. A flag given on the command line
beats the environment; the environment beats the default. Every
subcommand accepts the same flags, so `doctor` and `status` check the
configuration the server would run with.

Environment variables are the ones that matter in practice: an MCP
client passes command, args and env to a stdio server, and nothing
else. `internal/config` holds the list, and `make staleness` holds this
page to it both ways.

The configuration is validated once, before the server announces
itself, and every problem is reported together.

## Settings

| Variable | Flag | Default | What it does |
|---|---|---|---|
| `GITLAB_MCP_INSTANCE` | `--instance` | `https://gitlab.com` | The GitLab instance: gitlab.com or a self-managed base URL. A missing scheme means `https`; a trailing `/api/v4` is dropped; a sub-path such as `/gitlab` is kept. When neither the flag nor the variable is given, the profile's instance is used, else gitlab.com. |
| `GITLAB_MCP_PROFILE` | `--profile` | `default` | Which stored sign-in to use. Separate profiles keep separate tokens, such as one for gitlab.com and one for a self-managed instance. Lowercase letters, digits, `-` and `_`. |
| `GITLAB_MCP_CLIENT_ID` | `--client-id` | the profile's | The OAuth application id. Overrides the one the profile recorded at login. |
| `GITLAB_MCP_READ_ONLY` | `--read-only` | `false` | Register only the Read tools and request `read_api`. Needs a login made in this mode. |
| `GITLAB_MCP_ENABLE_SHIP` | `--enable-ship` | `false` | Register merging, approving, running and canceling CI, and creating releases. |
| `GITLAB_MCP_ENABLE_DESTRUCTIVE` | `--enable-destructive` | `false` | Register deletions. Each call also needs `confirm: true`. |
| `GITLAB_MCP_TOOLSETS` | `--toolsets` | none | Comma-separated optional toolsets: `activity`, `deployments`, `releases`, `snippets`, `wiki`, or `all`. |
| `GITLAB_MCP_WRITE_NAMESPACES` | `--write-namespaces` | anywhere | Comma-separated group or project paths that Write, Ship and Destructive calls are confined to. A call aimed elsewhere is `[blocked]`. |
| `GITLAB_MCP_CA_FILE` | `--ca-file` | none | A PEM file of extra certificate authorities to trust for the instance, on top of the system's. Read at start; a file that cannot be read or holds no certificate is refused then. |
| `GITLAB_MCP_ALLOW_HTTP` | `--allow-http` | `false` | Allow plain `http` to an instance that is not loopback. The token then travels in the clear. |
| `GITLAB_MCP_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn` or `error`. Logs go to stderr. |
| `GITLAB_MCP_LOG_FORMAT` | `--log-format` | `text` | `text` or `json`. |
| `GITLAB_MCP_HTTP_TIMEOUT` | `--http-timeout` | `60s` | Deadline for one attempt at an API call, as a Go duration between `1s` and `10m`. |
| `GITLAB_MCP_CONFIG_DIR` | `--config-dir` | your OS config directory plus `gitlab-mcp` | Where profiles are stored. Must be inside your home directory. |

Two more are read from the environment only:

| Variable | Default | What it does |
|---|---|---|
| `GITLAB_MCP_CONFIG_DIR_ALLOW_OUTSIDE_HOME` | `false` | Accept a `GITLAB_MCP_CONFIG_DIR` outside your home directory. The directory is created `0700` and its files `0600`, so a mistyped path such as `/etc` would otherwise be re-permissioned. |
| `GITLAB_MCP_REFRESH_TOKEN` | none | A refresh token to use instead of the stored one, for CI and automation. It has no flag because a command line is visible to every process on the machine. |

A switch takes `true`, `false`, `1`, `0`, `yes`, `no`, `on` or `off`,
and a bare flag such as `--read-only` means `true`.

### Refused combinations

- `GITLAB_MCP_READ_ONLY=true` with `GITLAB_MCP_ENABLE_SHIP` or
  `GITLAB_MCP_ENABLE_DESTRUCTIVE`. Guessing which was meant would drop
  either a guard or a tool, so start-up fails naming both.
- A token whose granted scopes cannot serve the mode, such as a
  `read_api` login with read-only mode off. Start-up fails naming the
  missing scope; run `gitlab-mcp login` again in the mode you want.
- An `http` instance that is not loopback, unless
  `GITLAB_MCP_ALLOW_HTTP=true`.
- An unknown toolset, a namespace that is not a group or project path, a
  timeout outside its bounds, a log level or format not listed above.

## What the settings register

GitLab's `api` scope cannot tell writing a comment from merging, so the
flags decide what is registered rather than what is requested:

| Setting | Registers | Requests |
|---|---|---|
| `GITLAB_MCP_READ_ONLY=true` | Read | `read_api` |
| default | Read and Write | `api` |
| `GITLAB_MCP_ENABLE_SHIP=true` | adds Ship | `api` |
| `GITLAB_MCP_ENABLE_DESTRUCTIVE=true` | adds Destructive | `api` |
| `GITLAB_MCP_TOOLSETS` | adds the tools of each toolset named | no change |
| `GITLAB_MCP_WRITE_NAMESPACES` | nothing; confines Write, Ship and Destructive | no change |

A tool that is not registered does not exist for the model: it cannot
be called, persuaded or not. The server's instructions name the
settings that would add more, so a model can tell you what to turn on
rather than guess. `docs/security.md` says what this does and does not
protect.

The instance's version also decides: a tool whose route is newer than
the instance is not registered.

## Where things are stored

The config directory is your OS config directory plus `gitlab-mcp`
(`~/.config/gitlab-mcp` on Linux, `~/Library/Application
Support/gitlab-mcp` on macOS, `%AppData%\gitlab-mcp` on Windows), or
`GITLAB_MCP_CONFIG_DIR`. The `default` profile lives at its top; any
other under `profiles/<name>/`. Each profile directory holds:

- `config.json`, the non-secret state: the instance, the application
  id, your username, where the token went and the scopes GitLab granted.
  It is restricted to your account like the token, because it names a
  person and an instance.
- The token pair, in the OS keyring (Secret Service on Linux, Keychain
  on macOS, Credential Manager on Windows) under the service
  `gitlab-mcp` and the profile's name.
- `token.json`, only when the keyring was unavailable at login: the
  token pair in a file restricted to your account (`0600`, or an ACL on
  Windows). Every use of it prints a warning, not only the first.
- `refresh.lock`, which two processes on the same profile take in turn
  to refresh the token. GitLab revokes a refresh token the moment it is
  used, so two processes spending one would sign each other out.

Credentials resolve in this order: `GITLAB_MCP_REFRESH_TOKEN`, then the
keyring, then the file.

`gitlab-mcp status` prints the profile directory and everything above
except the token.

## A refresh token from the environment

GitLab rotates the refresh token on every refresh and revokes the old
one, so a value in `GITLAB_MCP_REFRESH_TOKEN` works exactly once. The
pair it rotates into is stored in the keyring or the file, stamped with
a hash of the variable's value, and while the variable still holds that
value the stored pair is used in its place. Change the variable and the
new value is used.

`logout` neither revokes nor removes a token that came from the
environment; it is not the command's to remove.

## Proxies and private certificate authorities

Proxies come from the standard `HTTPS_PROXY`, `HTTP_PROXY` and
`NO_PROXY` variables. An MCP client starts the server with the
environment it is configured with, which may not be your shell's, so
put them in the client's `env` block as well.

For an instance whose certificate is signed by a private authority, set
`GITLAB_MCP_CA_FILE` to that authority's PEM bundle. It is added to the
system's roots, not in place of them.

## In an MCP client

```json
{
  "mcpServers": {
    "gitlab": {
      "command": "gitlab-mcp",
      "env": {
        "GITLAB_MCP_PROFILE": "work",
        "GITLAB_MCP_TOOLSETS": "wiki"
      }
    }
  }
}
```

The Claude Desktop bundle asks for the instance, the application id,
the profile and read-only mode, and passes them as the matching
variables. It does not log you in; run `gitlab-mcp login` from a
terminal first (`docs/setup.md`).
