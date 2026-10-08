# Configuration

The server talks to gitlab.com and nothing else, so no setting names an
instance. Every setting is an environment variable with the `GITLAB_MCP_` prefix,
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
| `GITLAB_MCP_PROFILE` | `--profile` | `default` | Which stored sign-in to use. Separate profiles keep separate tokens, such as one per GitLab account. Lowercase letters, digits, `-` and `_`. |
| `GITLAB_MCP_CLIENT_ID` | `--client-id` | the profile's | The OAuth application id. Overrides the one the profile recorded at login. |
| `GITLAB_MCP_READ_ONLY` | `--read-only` | `false` | Register only the Read tools and request `read_api`. Needs a login made in this mode. |
| `GITLAB_MCP_ENABLE_SHIP` | `--enable-ship` | `false` | Register merging, approving, running and canceling CI, and creating releases. |
| `GITLAB_MCP_ENABLE_DESTRUCTIVE` | `--enable-destructive` | `false` | Register deletions. Each call also needs `confirm: true`. |
| `GITLAB_MCP_REQUIRE_PROMPT` | `--require-prompt` | `false` | Refuse the writes that ask you, when your MCP client cannot ask. See below. |
| `GITLAB_MCP_TOOLSETS` | `--toolsets` | none | Comma-separated optional toolsets: `activity`, `deployments`, `planning`, `releases`, `snippets`, `wiki`, or `all`. |
| `GITLAB_MCP_WRITE_NAMESPACES` | `--write-namespaces` | anywhere | Comma-separated group or project paths that Write, Ship and Destructive calls are confined to. A call aimed elsewhere is `[blocked]`. |
| `GITLAB_MCP_UPLOAD_DIRS` | `--upload-dirs` | none | The absolute directories `upload_file` may read images from, separated as `PATH` is: `:` on Linux and macOS, `;` on Windows; a value that is one existing directory is taken whole. Unset, it reads none. See below. |
| `GITLAB_MCP_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn` or `error`. Logs go to stderr. |
| `GITLAB_MCP_LOG_FORMAT` | `--log-format` | `text` | `text` or `json`. |
| `GITLAB_MCP_HTTP_TIMEOUT` | `--http-timeout` | `60s` | How long a call to GitLab may go without progress: while its request is sent, waiting for the answer to start once it is, and between pieces of the answer. A large upload on a slow connection is not cut off while it keeps moving. A Go duration between `1s` and `10m`. |
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
- An unknown toolset, a namespace that is not a group or project path, an
  upload directory that is not absolute, a timeout outside its bounds, a
  log level or format not listed above.
- An upload directory that is a filesystem root, your home directory or
  a directory holding it: `upload_file` could read nearly every image
  you have. Name a dedicated folder, such as a screenshots folder.

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
| `GITLAB_MCP_UPLOAD_DIRS` | nothing; lets `upload_file` read images from more directories | no change |

A tool that is not registered does not exist for the model: it cannot
be called, persuaded or not. The server's instructions name the
settings that would add more, so a model can tell you what to turn on
rather than guess. `docs/security.md` says what this does and does not
protect.

## When the server asks you

Before a merge, an approval, a manual job, a release, a new tag, a
pipeline on the default branch or a protected branch or tag, making a
confidential issue public, an image upload, and every delete, the server asks you
through your MCP client when the client supports elicitation. The
question names the tool, what it touches and what cannot be undone.
Text from GitLab in it stands in backticks or code style. Accepting the
question is the confirmation. Declining, dismissing, a timeout, or an
answer the client gives without showing you anything all leave the call
`[blocked]`, and nothing is sent.

A client that cannot ask gets no question, and the flags and `confirm:
true` are the guard, as before. `GITLAB_MCP_REQUIRE_PROMPT=true` refuses
those writes there instead. A client that supports elicitation but runs
with nobody watching cannot make these writes at all.

`upload_file` has no flag of its own. With a client that cannot ask and
`GITLAB_MCP_REQUIRE_PROMPT` off, its only guards are the upload
directories and `GITLAB_MCP_WRITE_NAMESPACES`, so an instruction planted
in a comment could upload an image from those directories to a public
project the comment's author maintains. With such a client, set
`GITLAB_MCP_REQUIRE_PROMPT=true`, or confine writes with
`GITLAB_MCP_WRITE_NAMESPACES`.

## Where `upload_file` reads from

`upload_file` reads an image from this machine only inside the
directories `GITLAB_MCP_UPLOAD_DIRS` names. Unset, it refuses every path
and names the setting. It does not use your MCP client's roots, which
the protocol deprecates in favor of server configuration such as this:

```json
"env": { "GITLAB_MCP_UPLOAD_DIRS": "/home/you/Pictures/screenshots" }
```

It reads only a PNG, JPEG, GIF or WebP of at most 10 MiB, judged from
the bytes, and refuses a symbolic link and a path that leaves the
directory. The path is compared with the directory as written, so spell
both the same way. When your client can ask, the server asks you before
each upload.

## Where things are stored

The config directory is your OS config directory plus `gitlab-mcp`
(`~/.config/gitlab-mcp` on Linux, `~/Library/Application
Support/gitlab-mcp` on macOS, `%AppData%\gitlab-mcp` on Windows), or
`GITLAB_MCP_CONFIG_DIR`. The `default` profile lives at its top; any
other under `profiles/<name>/`. Each profile directory holds:

- `config.json`, the non-secret state: the instance it signed in to,
  the application id, your username, where the token went and the
  scopes GitLab granted. It is restricted to your account like the
  token, because it names a person.
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

## Proxies

Proxies come from the standard `HTTPS_PROXY`, `HTTP_PROXY` and
`NO_PROXY` variables. An MCP client starts the server with the
environment it is configured with, which may not be your shell's, so
put them in the client's `env` block as well. Certificates are checked
against the system's authorities; a proxy that inspects TLS needs its
authority in the system's trust store.

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

The Claude Desktop bundle asks for the application id, the profile,
read-only mode and one image directory for `upload_file`, and passes
them as the matching variables. It does not log you in; run `gitlab-mcp login` from a
terminal first (`docs/setup.md`).
