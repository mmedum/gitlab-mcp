# Security

This server acts as one signed-in person, with their own OAuth
application, from their own machine, against gitlab.com. It
has no hosted component, no telemetry and no update check. This page
says what it can touch, what stops it going further, and what it cannot
stop. `SECURITY.md` says how to report a problem.

## The threat model

A GitLab instance mixes three payloads: other people's words (issues,
merge requests, comments, reviews), an organization's code, and CI
output that can carry secrets. The model reading them through this
server can be persuaded by any of them. The server cannot stop a model
from being persuaded. It can make persuasion visible, keep the tools
that do the most damage unregistered unless you ask for them, confine
where writes land, and never add its own voice to the attacker's.

The toxic flow that matters most is private content written somewhere
public: the model reads a private repository, then a public issue tells
it to post what it read. The write allow-list below is the control for
that.

## Content is data, never instructions

Issue and merge request text, comments, review threads, commit
messages, file contents, wiki pages and job logs were written by
someone other than you, often in projects anyone can post to.

- **It is marked.** Rendered content sits inside a block that names its
  origin — kind, project, item and author — between markers carrying a
  token drawn fresh for each call:

  ```text
  <<<UNTRUSTED 0123abcd kind=description project=example-group/app item=#12 author=@alice>>>
  ...
  <<<END 0123abcd>>>
  ```

  Content written before the call cannot know the token, so it cannot
  close the block early, and any `<<<` inside it is defused so it cannot
  even look like a marker. A line before the first block says the text
  between those markers is data, not instructions. In
  `structuredContent` the same text sits in fields named `untrusted_*`.
- **Hidden text is removed and counted.** In Markdown, zero-width and
  bidirectional-control characters and HTML comments are dropped before
  display, and the result says how many characters went. In code —
  files, diffs, commit messages — the same characters are written out
  visibly as `<U+202E>` and counted instead, because dropping them would
  silently change text you may edit, and they are how Trojan Source
  hides code.
- **Nothing is fetched.** No image, attachment or link in content is
  followed; a link renders as text with its host shown. Rendered HTML is
  never requested.
- **Token shapes are masked.** GitLab token prefixes (`glpat-`,
  `gloas-`, `gldt-`, `glrt-`, `glcbt-` and the rest) and common cloud
  key shapes are replaced before job output is shown, and the result
  counts the masks. GitLab masks only CI variables marked masked.
- **Authorship is shown.** Every rendered comment and description names
  its author.
- **The server's own text never relays content.** Tool descriptions and
  the server instructions say content is untrusted, and no result
  phrases what content says as something to do.

## Nothing this server writes runs a quick action

GitLab executes `/merge`, `/close`, `/assign` and dozens of other quick
actions from descriptions and comments sent through the API, whatever
this server's flags say. So every Markdown body passes one guard,
`internal/quickaction`, on its way out:

- It finds every line GitLab would read as a command, known or not,
  skipping code blocks, quotes and inline code the way GitLab does.
- **By default the call is refused** as `[blocked]`, naming each line
  and its number. Nothing is sent.
- With `escape_commands: true` each such line gets a leading backslash,
  which renders as the same visible text and which GitLab no longer
  reads as a command. The result says which lines were escaped.
- There is no way to ask for a quick action to run. Every effect one
  has is a field on a tool, under that tool's kind.

`make bodies` holds that every string a write tool sends goes through
the guard or is listed as a plain field with a reason.

## Kinds, flags, and what the token can do

Every tool has one kind, and the kind decides whether it is registered:

| Kind | What | Registered |
|---|---|---|
| Read | every GET | always |
| Write | issues, comments, reviews, branches, commits to unprotected branches, merge requests, todos, wiki, snippets | unless `GITLAB_MCP_READ_ONLY=true` |
| Ship | merge, approve, unapprove, run, retry, play and cancel CI, create a release | only with `GITLAB_MCP_ENABLE_SHIP=true` |
| Destructive | delete a branch, a comment, a wiki page | only with `GITLAB_MCP_ENABLE_DESTRUCTIVE=true`, and each call needs `confirm: true` |

A review that would approve counts as Ship: without the flag the call
is `[blocked]`.

**The default token can merge, approve and run pipelines.** GitLab's
`api` scope covers every write, and it has no narrower scope that
allows commenting but not merging. `read_api` is the only narrower
scope, and it allows no writes at all. So registration is the only
control: leaving Ship unregistered stops *this server* merging,
approving or running, retrying, playing or cancelling pipelines. It does
not stop anything else that holds the token, which is why the token
lives in the OS keyring and nowhere a tool can read it.

A commit, a new branch and a new merge request start the pipelines
GitLab starts on a push, as your own push would. On a branch that is not
protected those run with your permissions and your project's unprotected
CI variables only. Write tools never create or commit to a protected
branch (below), so they cannot start a pipeline that sees protected
variables or deploys.

Tool annotations are not a control either. The MCP specification says
a client may not trust them, and a registered tool is one a model will
reach for eventually. A tool your settings exclude does not exist.

If you want a token that cannot write, use read-only mode and log in
again in it. Switching `GITLAB_MCP_READ_ONLY` on keeps the old `api`
token working (it covers `read_api`), so run `gitlab-mcp logout` and
then `gitlab-mcp login --read-only --client-id <application id>` to
replace it with a `read_api` one.

## Writes stay where they are allowed

- **`GITLAB_MCP_WRITE_NAMESPACES`**, when set, confines every Write,
  Ship and Destructive call to projects under the listed groups or
  projects. Any other target is `[blocked]` naming the setting. Unset,
  a write can go anywhere your account can write. `mark_todos_done` is
  outside it: it changes only your own to-do list, which nobody else
  sees.
- Every write result names the target project's visibility, so a write
  to a public project shows as one.
- **Code reaches a protected branch only through a merge request.**
  `create_commit` refuses the project's default branch and every
  protected branch, and `create_branch` refuses a name a protected-branch
  rule covers, read at call time.
- **`lint_ci` sends nothing GitLab would fetch.** Configuration you pass
  to it may not use `include:`, since GitLab fetches what an include
  names while linting.
- **A create is never retried.** Notes, issues, merge requests, commits,
  pipelines and releases are POSTs GitLab does not deduplicate. When the
  outcome is unclear the result is `[ambiguous_outcome]`, and the server
  has already read to settle it; it never creates again to find out.
- **A write carries a witness.** An update names the version it read
  (`last_commit_id`, `sha` or `updated_at`) and is refused as `[stale]`
  if that moved. Omitted fields stay unchanged, and lists change by add
  and remove, never by replacement.

## What the server talks to

Only gitlab.com, over `https`, trusting the system's certificate
authorities. No setting names another instance. No redirect is
followed, and no URL from a response is called, except a same-origin
`Link: rel="next"` under the API root: a followed redirect would carry
the token to another host.

The one exception is a development override, documented in
`docs/development.md`, that points the binary at an in-memory stand-in
for the tests. Start-up refuses it unless its host is loopback, so it
cannot send a token to another real host, and logs a warning whenever
it is set.

A token is only ever sent to the instance that issued it. A profile
signed in to the test instance does not send its token to gitlab.com,
nor the reverse: the token is withheld and every call answers `[auth]`
naming the mismatch.

## Token storage and rotation

- The OAuth application is yours. Nothing is shipped in the binary, and
  there are no personal access tokens. Revoking your application or
  your token affects no one else.
- `login` listens on `127.0.0.1` on a random port, uses PKCE with S256,
  and checks `state` on the callback.
- The token pair goes to the OS keyring. If none is available it goes
  to a file restricted to your account, and every use of that file
  prints a warning.
- GitLab rotates the refresh token on every refresh and revokes the old
  pair at once. Refresh happens under a lock file in the profile
  directory, the new pair is stored before it is used, and a process
  whose refresh is refused re-reads the store for a pair another process
  wrote before it asks you to log in again.
- `logout` revokes the token at GitLab and deletes the local copy. It
  names the other profiles that use the same application, because
  deleting or renewing the application signs them out too. A token
  supplied through `GITLAB_MCP_REFRESH_TOKEN` is outside its reach, and
  it says so.
- To rotate by hand: `gitlab-mcp logout`, then
  `gitlab-mcp login --client-id <application id>` (`logout` forgets the
  profile, the application id included). To
  cut off every token the application ever issued, renew or delete the
  application in GitLab.

## What the logs carry

Logs go to stderr, never stdout, because stdout carries the JSON-RPC
frames the client parses.

A log line carries the method, the tool, the outcome, the duration, the
rate-limit bucket and ids truncated to six characters. It never carries
a hostname, a group or project path, a branch, a file path, a title, a
body, a username, an email address or a search term. Paths and search
terms reach a log through a request URL, so transport errors are
stripped of path and query before they are logged. A test drives every
registered tool with canary values at debug level and fails if any
canary reaches the log.

`status` and `doctor` print to your terminal, where you asked for them,
and mask any host other than gitlab.com, the application id and your
username as
`{host 1}`, `{client-id 1}` and `{user 1}`, with a count of what was
masked at the end.

## What never to paste

Into an issue, a pull request, a chat or a model conversation you share:

- a token of any kind: anything starting `glpat-`, `gloas-` or another
  GitLab prefix, or an OAuth access or refresh token;
- `token.json`, or the output of a keyring tool;
- your application id, a group or project path, a username or an email
  address;
- a tool result, issue or merge request text, file contents or a job
  log. A tool result is your organization's code and other people's
  words.

`doctor`, `status`, `--version` and a debug log are safe to paste; they
are built to be.

## Keeping the repository clean

Nothing from a real instance enters this repository. Fixtures are
generated by `internal/gapi/gitlabtest`, never recorded; the live
driver reads only a scratch project it created for the run.
`make leaks`, `make secrets` and the pre-commit hook refuse the shapes
they can see: GitLab token prefixes, addresses and hosts outside the
example domains, and OAuth shapes.
