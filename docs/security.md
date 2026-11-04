# Security model

The threat model and the invariants the code holds.
[`SECURITY.md`](../SECURITY.md) says how to report a vulnerability.

It applies to one binary, running as a subprocess of your MCP client, on
your machine, with your own OAuth application and your own token,
against gitlab.com only (D-01, D-02). It has no listener except while
`login` waits, no telemetry and no update check.

## Trust boundaries

| Boundary | Crosses | Assumption |
|---|---|---|
| MCP client ↔ this server | An OS pipe | Both run as the same OS user, unauthenticated. |
| This server ↔ gitlab.com | TLS | A token goes only to the instance that issued it (D-03). No redirect is followed, except a same-origin next page and a moved project's GET under the API root (D-65). |
| This server ↔ disk | The file system | The config directory, and the directories `GITLAB_MCP_UPLOAD_DIRS` names, read only by `upload_file` (D-44). |
| Browser ↔ the login listener | The loopback interface, on a random port | Alive only while `login` waits. PKCE and `state` are checked on the callback (D-66). |
| Content authors ↔ the model | Everything a tool returns: issues, comments, files, job logs and the rest | Written by someone else, often in public projects, sometimes to steer an assistant (D-13). |

## What is worth stealing

- **The token pair.** GitLab has nothing between `read_api` and `api`,
  and `api` can merge, approve, run pipelines and delete in every
  project your account reaches. It lives in the OS keyring where there
  is one, and in an owner-only file where there is not (D-71).
- **Private code, issues and CI output.** They pass through tool results
  to your client and nowhere else. CI output can carry secrets.
- **Your protected branches and their pipelines**, which see protected
  variables and may deploy.

## Adversaries

**An author writing to steer your assistant.** The server cannot stop a
model being persuaded, so it limits what persuasion reaches, through the
content and write invariants below. The flow that matters most is
private content written somewhere public: the model reads a private
repository, then a public issue tells it to post what it read.
`GITLAB_MCP_WRITE_NAMESPACES` is the control for that (D-38).

**A malicious MCP client, or a persuaded model driving an honest one.**
It can call any registered tool under your credentials. A client may
ignore annotations, so registration is the control: Ship and Destructive
tools exist only under their flags (D-21, D-22). `confirm: true` is a
witness the model writes, never your approval (D-28). Past that,
GitLab's permissions and the guards below are the bound.

**A co-resident process.** It cannot finish a login without the PKCE
verifier, and the token file is readable by your account alone.

## Invariants the code holds

A test or a gate holds each of these. A way to break one is a
vulnerability.

- **Stdout carries only JSON-RPC.** forbidigo and the smoke test hold
  it (D-04).
- **Logs never carry the payload**, and a transport error loses its path
  and query. `TestLogsNeverCarryThePayload` and
  `TestTransportErrorsCarryNoPathOrQuery` hold it (D-05).
- **A token goes only to its instance, and no redirect carries it
  elsewhere.** `TestRedirectsElsewhereAreNeverFollowed` holds it (D-03,
  D-65).
- **Content stays inside its block**, hidden text is counted, nothing
  it references is fetched, and the server's own text never relays it.
  `TestBoundaryCannotBeClosedByContent` holds the block (D-13 to D-15,
  D-17).
- **Job logs, artifacts and test output are masked** for token and key
  shapes. `TestMaskSecrets` holds it (D-16).
- **No body runs a quick action.** The `bodies` gate and
  `FuzzEscapeNeverYieldsCommand` hold it (D-18, D-19).
- **The gated tools are absent without their flags.**
  `TestShipAndDestructiveAreRegisteredOnlyByTheirFlags` and
  `TestDestructiveNeedsConfirm` hold it (D-21, D-28).
- **A dry run writes nothing.** `TestDryRunRefusesEveryWrite` holds it
  (D-29).
- **Read-only mode asks for `read_api`**, so GitLab refuses a write too.
  `TestAReadAPITokenCannotServeWrites` holds it (D-30).
- **Code reaches a protected branch only through a merge request**, and
  no write creates or moves a protected branch or tag.
  `TestCodeReachesMainThroughAMergeRequest` holds it (D-33).
- **Writes stay inside `GITLAB_MCP_WRITE_NAMESPACES`** when it is set.
  `TestTheWriteAllowList` holds it (D-38).
- **Nothing becomes more visible.** Snippets are private (D-39), only
  `make_issue_public` opens a confidential issue (D-40), and
  `move_issue` never moves to a wider audience (D-41).
- **A create is never repeated; an unclear one is settled by reading.**
  `TestALostCreateIsSettledByReading` holds it (D-34).
- **A write that moved since its read is refused `[stale]`.**
  `TestMergeAfterAPushIsStale` holds it (D-36).
- **`upload_file` reads only an image, only from the directories you
  named**, after the project is checked (D-44, D-45).
- **What is committed for a suggestion is the text the call carried.**
  `TestApplySuggestionsHoldsTheText` holds it (D-46).
- **`run_merge_request_pipeline` refuses a fork's merge request**,
  `lint_ci` refuses supplied content holding an `include:`, and release
  links stay in their project (D-47, D-48, D-49).
- **Variable values are sent and never shown**, and `get_project` never
  decodes a secret field (D-50, D-61).
- **A refresh token is spent once**, under a cross-process lock.
  `TestTwoProcessesRefreshOnceAndShareTheResult` holds it (D-67).
- **The token file is owner-only and warns on every use** (D-71), and
  **`logout` revokes at GitLab** (D-72).
- **The server asks the person nothing** (D-26).
- **Nothing in the repository identifies a real instance, account or
  person** (D-06). Fixtures are generated (D-07), and the live driver
  reads only what it wrote (D-83).

## Not a vulnerability

Do not report these as vulnerabilities. Open an ordinary issue if one
surprises you.

- **The client did not prompt.** Annotations and the
  `requiresUserInteraction` mark are hints. A tool registered while its
  flag is off is a vulnerability.
- **What runs as you can act as you.** Any process running as you can
  read the token and use it, with or without this server.
- **The `api` token can merge.** Leaving Ship off stops this server
  merging, not anything else holding the token. A model calling a
  registered tool under your credentials is the design.
- **A model following instructions in content.** That is the client's
  concern. Content that makes the server itself break an invariant is a
  vulnerability.
- **Someone else's code.** A flaw in GitLab goes to GitLab, and one in a
  dependency to that project. We will help route it.

## Dependency vulnerabilities

`govulncheck` runs in `make check` and in CI on every pull request and
push to `main`. It fails when this server's code can reach a vulnerable
symbol.

A CVE in a dependency is a vulnerability here only when `govulncheck`
reports it reachable. It is then fixed in a release whose CHANGELOG entry
names the CVE or GHSA. Otherwise the dependency is updated in the normal
course, and a report about it is not a vulnerability.

## What you are responsible for

- **Your own OAuth application.** Nothing is shipped in the binary, so
  revoking yours affects no one else.
  [`docs/troubleshooting.md`](troubleshooting.md#tasks) says how to
  rotate the token, revoke access and handle a suspected exposure.
- **The switches you turn on.** `GITLAB_MCP_ENABLE_SHIP` and
  `GITLAB_MCP_ENABLE_DESTRUCTIVE` arm tools that merge, run CI or cannot
  be taken back. Leave them off for a client that runs with nobody
  watching, and use read-only mode, logged in again, where reading is
  enough.
- **`upload_file`.** It has no flag of its own. A client that runs it
  unasked lets a planted instruction upload an image from your upload
  directories to a public project. Give it an `ask` rule, or set
  `GITLAB_MCP_WRITE_NAMESPACES`.
- **Where the token lives.** Keep a keyring available, and keep
  `GITLAB_MCP_REFRESH_TOKEN` out of shared shells and logs.
- **What you paste.** `doctor`, `status` and a debug log are built to be
  safe. A token, `token.json` and a tool result are not.
