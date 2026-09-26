# Architecture — gitlab-mcp

**Status: phase 0 built on a topic branch and run live, 2026-09-26;
some of its spikes are owed. Nothing is tagged.** This document holds the platform
facts, the design bets, a verdict on every API operation group, the
phase plan and the spikes that must answer before the phases that
depend on them. What phase 0 still owes is at the end of its entry in
§16.

## 1. Mission and scope

A production-grade Go MCP server for GitLab, distributed to other
people. One binary, stdio, per-user sign-in, no hosted deployment. It
works against gitlab.com and against self-managed instances, Community
and Enterprise Edition, from the floor version of §17.4 up.

The server works **inside projects**: finding and reading issues and
merge requests, reviewing a merge request the way a person does (drafts,
inline comments, a submitted review), reading and changing the
repository through branches and merge requests, and reading and — when
the person opts in — driving CI. It stops at administration: instance
and group settings, runners, CI variables, access tokens, deploy keys,
webhooks and project deletion are out of scope, and §8a says why group
by group.

### Why build it (research summary, checked 2026-09-25)

Nine servers were surveyed; their trackers are where §3 comes from.

- **GitLab's built-in MCP server** (`/api/v4/mcp`). Beta, GA planned for
  19.5; Free tier since 19.2; about 56 tools, most introduced in 19.3 and
  19.4. Streamable HTTP only — a stdio client needs a Node bridge. OAuth
  only, through Dynamic Client Registration with the `mcp` scope, rate
  limited to 10 registrations an hour per IP; personal access tokens are
  an open request. It must be switched on by a group owner or an
  administrator. Its toolset and allow-list headers filter `tools/list`
  but not `tools/call`, by design
  (<https://gitlab.com/gitlab-org/gitlab/-/issues/631017>). It has no
  read-only mode, no draft notes and no todos, and its note tools refuse
  lines starting with `/` to avoid quick actions. Its `manage_pipeline`
  deletes a pipeline when given only an id.
- **GitLab's CLI in MCP mode.** An experiment: about 200 tools generated
  from CLI commands, no filtering, and a raw authenticated API tool.
- **The archived reference server.** Nine tools, PAT only, unmaintained.
- **The most-starred community server** (about 2,000 stars). Around 260
  tools behind toolset flags; fifteen published security advisories in
  2026, three critical: local file read leading to token exfiltration,
  SSRF, DNS rebinding, path traversal through unencoded ids, a token
  forwarded across a redirect, and read-only mode bypassed through a raw
  GraphQL tool and verb heuristics.
- **A Go community server** with near-total coverage (865–1,091 actions,
  most behind a find-and-execute pair). Its tracker shows the cost of
  that breadth: a `read_api` token offered every write tool, a
  permission refusal reported as an expired token, a branch name
  escaped twice, GraphQL errors rendered as empty results.
- Four smaller community servers, and GitHub's and Atlassian's official
  servers for patterns: toolsets, a read-only mode that beats every
  other setting, lockdown of content from untrusted authors, and an
  interactive OAuth path beside a headless token path.

What none of them offers together, and what this server is for: a local
stdio binary that signs in exactly as its Google siblings do, keeps its token
in the OS keyring and works the same on gitlab.com and an instance
behind a corporate CA; **a curated surface of about fifty tools** rather
than an API mirror; **gating enforced by registration**, so a tool that
is off cannot be called; **no quick action ever executed by what it
writes**; reviews built the way people review, with the server — not the
model — computing where an inline comment lands; reads with a stated
budget, job logs included; and releases that can be verified from
outside.

GitLab's built-in server is the one to watch (§17.10). If it gains stdio,
tokens, enforced filtering and a read-only mode, most of this list is
answered by GitLab.

### Non-goals

- **Administration.** Instance settings, users, runners, group and
  project settings, access tokens, deploy keys and tokens, webhooks,
  integrations, audit events. §8a.
- **CI variables and secure files.** Reading them puts secrets into a
  model's context; writing them is a supply-chain lever. Written off.
- **Deleting projects or groups, force pushes, protected-branch
  changes.** Written off, not gated.
- **An API escape hatch.** No raw REST or GraphQL tool. The most-starred
  server's read-only bypasses all came through one.
- **Push.** Webhooks need a listener a stdio process does not own.
- **Premium and Ultimate planning objects** — epics, iterations,
  vulnerabilities — before 1.0. §17.11.
- **Container registry and package contents.** Deferred.
- **A hosted or HTTP transport.** stdio only.

## 2. Hard constraints from the platform

Each is checked against GitLab's source at master `829b21d2` (VERSION
`19.5.0-pre`, 2026-09-25), the OpenAPI v3 file in that tree, Doorkeeper
v5.9.3, RFCs, or a GitLab page; §18 has the row and marks which.

1. **Sign-in is Doorkeeper OAuth 2.0.** A loopback redirect on an IP
   literal may use any port: when both the registered and the requested
   URI are loopback, Doorkeeper nils both ports before comparing,
   citing RFC 8252 §7.3. `localhost` is not an IP literal and gets no
   such relaxation. Scheme, host and path must still match, and plain
   `http` is accepted for loopback. GitLab's own MCP page says the
   redirect "must exactly match"; the code says otherwise for loopback,
   and spike A settles it on a live instance. §10.
2. **A public client needs no secret, and PKCE is not enforced.** An
   application with `confidential: false` exchanges codes and refresh
   tokens without a secret. GitLab enforces PKCE only for dynamically
   registered applications; S256 is supported and this server always
   sends it. Verifiers shorter than 43 characters are logged and may be
   refused later.
3. **Refresh tokens rotate with no grace.** A refresh revokes the old
   access and refresh tokens at once, under a lock; a second refresh
   with the old token is `invalid_grant`. Access tokens last
   `expires_in` seconds — 7,200 by default, configurable by
   administrators from 19.1 down to 300. §10.
4. **Dynamic Client Registration cannot mint an `api` token.** It
   creates public PKCE clients whose scope is forced to `mcp`, prefixes
   their name `[Unverified Dynamic Application]`, is rate limited per
   IP, and can be switched off. It is for GitLab's own endpoint.
5. **Device authorization (RFC 8628) exists and is off per application.**
   GA in 17.9; `POST /oauth/authorize_device`, user code valid 300 s.
   Applications created after the 18.10 migration have
   `device_code_enabled` false unless someone ticks it; the OAuth
   provider page does not mention the toggle. Not used (§10).
6. **Scopes cannot separate the writes this server cares about.** REST
   enforces `api` on every method and `read_api` on GET and HEAD only.
   Every issue, merge request, note, commit and pipeline write needs
   `api`, which also permits merging, approving, running pipelines and
   deleting. The narrower `mcp` scope covers a handful of routes tagged
   for GitLab's own server and not note creation or issue update.
   Registration is the only control. §4.3.
7. **Personal access tokens expire.** Expiry has been mandatory since
   16.0, at most 365 days (400 behind a flag), at midnight UTC;
   administrators can relax it on self-managed. Fine-grained tokens are
   GA from 19.2 and cover about 1,270 of about 1,583 route declarations.
   Not used (§10); recorded because instance policy on them is what
   people will ask about.
8. **Quick actions run through the API.** Creating a note or creating or
   updating an issue or merge request executes every quick-action line
   in the body — `/close`, `/merge`, `/assign`, `/move`, `/label`,
   `/approve` and the rest. A note that is only commands returns 202 and
   saves nothing. There is no parameter to turn this off, and the API
   reference does not mention it. A command is a paragraph line matching
   `^/<name>`; code blocks, block quotes, HTML blocks and inline code are
   skipped. §4.2.
9. **There is no `If-Match` and no idempotency key.** Nothing in
   `lib/api` reads either. Issues and merge requests have a
   `lock_version`, but only the web controllers accept it. Weak ETags
   on GET serve conditional reads only (spike H). §4.6.
10. **Where a guard exists, it is per API.** The Files API takes
    `last_commit_id` and refuses a mismatch with **400**, not 409. The
    Commits API takes `last_commit_id` per action and `start_sha`. Merge
    and approve take `sha` and refuse a mismatch with **409**, and a
    group may require it (400 when absent; one surveyed server broke on
    exactly this in 19.2). Issue, merge request, note and wiki updates
    have no guard.
11. **Creates are not idempotent.** A retried note, issue, commit,
    pipeline or fork is a second one. A merge request for a source and
    target that already have an open one is refused 409, and branches,
    tags and files refuse an existing name — those guard themselves.
12. **Pagination is offset or keyset, and both have edges.** `per_page`
    defaults to 20 and caps at 100. `X-Total` and `X-Total-Pages` vanish
    once a count reaches 10,000. On endpoints that support keyset, an
    offset past 50,000 rows is **405**. Keyset is followed through the
    `Link: rel="next"` header, never constructed. §7.1.
13. **Rate limits differ by deployment and are reported unevenly.**
    gitlab.com: 2,000 authenticated API requests a minute per user;
    60 notes a minute; 200 issues a minute; 100 searches a minute; 50
    group listings a minute; 25 pipeline creations a minute per
    project, user and commit; tier-aware limits proposed at 100 a minute
    for Free. `RateLimit-*` headers describe only the Rack::Attack
    throttles; an application limit can answer 429 while
    `RateLimit-Remaining` shows quota. A Rack::Attack 429 body is plain
    text; an application 429 is JSON. Self-managed instances ship the
    general throttles off. §11.
14. **Errors come in four shapes.** `{"message": "404 Project Not
    Found"}`; `{"message": {"field": ["…"]}}` for validation (400 or
    422); `{"message": {"error": "…"}}` for spam and application limits;
    `{"error": "…"}` for parameter validation and **for a route that
    does not exist**, which the catch-all answers `{"error": "404 Not
    Found"}`. That last shape is how a missing Enterprise route or an
    older instance is told apart from a missing resource (spike F).
    §6.5.
15. **404 hides private resources.** A project the token cannot read is
    404, not 403. Merging without permission is **401**, not 403. A
    moved project redirects GET and refuses other methods with 405.
16. **A project is a numeric id or a percent-encoded full path**
    (`group%2Fsub%2Fproject`), and reverse proxies that decode `%2F`
    break the second. Issues and merge requests are addressed by project
    plus `iid`, the number people see as `#12` and `!12`. §6.1.
17. **Sizes.** Titles 255 characters; descriptions and notes 1,048,576
    bytes by default. Diffs: 200 KB per patch, 1,000 files and 50,000
    lines by default before `collapsed` and `too_large` apply. Job logs
    read in windows of at most 500 KB through undocumented
    `byte_offset` and `byte_limit` parameters (spike J). Commits over
    20 MB are throttled and over 300 MB refused on gitlab.com.
18. **Tier is not advertised.** `/api/v4/metadata` gives `version`,
    `revision` and `enterprise` and needs authentication; there is no
    plan field a normal user can read, and a licensed-feature refusal is
    404 in some routes and 403 in others. The OpenAPI file marks tier on
    3 of 1,862 operations. Tier is probed, never assumed. §7.9.
19. **The machine-readable surface is the OpenAPI v3 file.**
    `doc/api/openapi/openapi_v3.yaml` is generated from the Grape routes
    and checked current in GitLab's CI: 1,384 paths and 1,862
    operations at `19.5.0-pre`. It omits experimental work-item routes
    and most policy routes, includes 11 internal paths, and leaves 526
    operations without a response schema. The v2 file is deprecated.
    The GraphQL schema is not committed. §8a.

### What the API cannot do (so we don't promise it)

- Say whether a failed create happened. Only a read afterwards can.
- Guard an issue, merge request or wiki update against a concurrent
  edit. §4.6 narrows the window; it cannot close it.
- Report the account's tier or plan to an ordinary user.
- Store a body without evaluating quick actions in it.
- Say which rate limit a request will hit before it hits it.

## 3. Requirements distilled from other servers' failures

Each traced to a public report in §1's servers; §18 has the evidence.

1. Sign-in survives its own refresh: a token never refreshed after
   startup, and a second process spending the same refresh token, both
   end in a forced re-login.
2. 401 and 403 are told apart and the missing scope is named; a
   permission refusal was reported as an expired token.
3. A `read_api` token is never offered a write tool.
4. The base URL is normalized: a host, a URL, a URL ending `/api/v4`, a
   sub-path install; `/api/v4` duplicated and `http` refused were both
   reported.
5. A private CA, `HTTPS_PROXY` and `NO_PROXY` work; self-signed
   instances were the most-reported setup failure.
6. Every path segment is escaped exactly once, from typed parts; a
   branch with a slash escaped twice, and an unencoded id walked `../`
   to other endpoints.
7. A token is never sent to another host, and no URL taken from a
   response body is fetched.
8. Lists never truncate silently and say how to continue; labels capped
   at 20 without saying so, a keyset cursor that never advanced, and a
   filtered-after-paging empty page reported as the end.
9. Output is bounded: a single list call returned 120–180k tokens of
   embedded objects.
10. Diffs keep `too_large` and `collapsed`, are fetched per file, and
    binary content is never decoded as text.
11. Job logs are read as a tail or a byte range, ANSI stripped, with a
    `failed_only` path.
12. `iid` and project are required, named consistently, and explained;
    optional `project_id` sent a model into a 404 loop.
13. Inline comments land: the position is computed by the server from
    the diff and the merge request's diff refs; `line_code can't be
    blank` was the most-reported write failure.
14. Reviews are first-class: drafts, then one submission.
15. `sha` is carried on merge and approve.
16. Schemas stay flat: no top-level `anyOf`, shallow nesting, every
    property typed; null and empty optional values mean absent.
17. Nothing written executes a quick action.
18. Gating is enforced on call, not only on listing; a destructive
    default (delete when only an id is given) is never a default.
19. Secret fields are stripped from project reads; a runner registration
    token leaked through one.
20. Content from issues and merge requests is untrusted; hidden-prompt
    exfiltration through GitLab content has been demonstrated publicly.

## 4. Core design bets

### 4.1 Content is data, never instructions

Issue and merge request text, comments, review threads, commit messages,
file contents, wiki pages, snippets, release notes and job logs are
written by people other than the person using the server, often in
public projects anyone can post to. GitLab's own MCP documentation
tells users they are "responsible for guarding against prompt
injection". This server cannot stop a model from being persuaded; it can
make the persuasion visible, keep the dangerous tools off by default and
never add its own voice to the attacker's.

1. **Boundaries.** Rendered content sits inside a delimited block
   naming its origin — kind, project, iid or path, author username —
   with a boundary token generated per call so the content cannot close
   the block itself. `structuredContent` carries the same text in fields
   named `untrusted_*`.
2. **Hidden text is removed and counted.** Zero-width and
   bidirectional-control characters and HTML comments are dropped from
   Markdown before it is shown, and the result says how many characters
   were dropped. In code — files, diffs, commit messages — the same
   characters are made visible as `<U+202E>` and counted instead, because
   dropping them would silently alter text the caller may edit, and they
   are how Trojan Source hides code. Rendered HTML is never requested.
   Names other people choose and the text prints outside a block —
   file paths, branches, labels, topics — get the same visible form for
   control characters as well, so a newline in a file name cannot start
   a line that reads as the server's; a display name such as a commit
   author is folded onto one line.
3. **Nothing is fetched.** No image, attachment or link in content is
   followed. Links render as text with their host shown, the host read
   as a browser reads it: the authority ends at the first `/`, `?`, `#`
   or `\`, so an `@` after one of them cannot make an outside link
   look like the instance's.
4. **Job logs are masked.** Every token shape GitLab documents
   (`glpat-`, `gloas-`, `gldt-`, `glrt-`, `glcbt-` and the rest, list
   fixed at phase 0 from the token-prefix page) and common cloud key
   shapes are replaced before rendering, and the result counts the
   masks. GitLab's own masking covers only masked variables.
5. **Authorship is shown.** Every rendered comment and description
   names its author's username, and where the call already has it, the
   author's role in the project. Whether content from authors below
   Developer is withheld in public projects — GitHub's lockdown — is
   §17.3.
6. **The server's own text never relays content.** Tool descriptions
   and the server instructions say content is untrusted, and no result
   ever phrases what content says as something to do.

### 4.2 Nothing written runs a quick action

§2.8 makes every body this server sends a command channel: a comment
reading `/merge` merges, whatever the Ship flag says. So one package,
`internal/quickaction`, owns every Markdown body on its way out.

1. It parses the body the way GitLab's extractor does — paragraphs only,
   skipping fenced and indented code, block quotes, HTML blocks and
   inline code — and finds every line that GitLab would read as a
   command, known name or not (GitLab's list grows every release, and an
   unknown command is harmless only until it is known).
2. **Default: refuse.** The call is `[blocked]` and names each line and
   its number. Nothing is sent.
3. **`escape_commands: true`** rewrites each such line with a leading
   backslash, which CommonMark renders as the same visible text and the
   extractor no longer matches (spike E proves both halves on a live
   instance). The result says which lines were escaped.
4. **There is no opt-in to execute.** Every effect a quick action has is
   a field on a tool, under that tool's kind.
5. A 202 from note creation means commands ran and nothing was saved.
   The server treats it as a defect of the guard: `[unexpected]`, logged
   at error.

`scripts/gates bodies` derives every string input of every Write, Ship
and Destructive tool from the schema dump and fails on one that is
neither routed through the guard nor listed as a plain field with a
reason.

### 4.3 Kinds, and what registration enables

Four kinds, decided in one `register` (`CLAUDE.md` rule 14):

| Kind | What | Registered |
|---|---|---|
| Read | every GET | always |
| Write | issues, comments, reviews, branches, commits to unprotected branches, merge requests, todos, wiki, snippets | unless read-only |
| Ship | merge, approve, unapprove, run, retry, play and cancel CI, create a release | `GITLAB_MCP_ENABLE_SHIP=true` |
| Destructive | delete a branch, a comment, a wiki page | `GITLAB_MCP_ENABLE_DESTRUCTIVE=true`, plus `confirm: true` per call |

Ship is its own kind because it is where a persuaded call stops being a
record someone can edit and becomes an effect: code in the default
branch, a deployment job run, a sign-off another person's merge rule
counts. §2.6 rules out the scope as the control, and the standard's §3
already says an annotation is not a control and a registered tool can
run unattended. A submitted review that would approve (`reviewer_state:
approved`) is Ship-dependent: without the flag the call is `[blocked]`
naming it.

The standard's rule that destructive tools are unregistered is held.
One sibling registers deletions and refuses per call, arguing that a
tool that does not exist cannot explain itself; here the server
instructions name the flags and what they add, which answers that
without registering the tool (§17b).

Toolsets sit beside kinds (§8): `wiki`, `snippets`, `releases`,
`deployments` and `activity` are off by default so the default surface
stays well under the 64-tool point where one client starts regrouping
tools. `GITLAB_MCP_READ_ONLY=true` beats every other setting.

### 4.4 Code reaches a protected branch only through a merge request

`create_commit` refuses the project's default branch and every branch
matching a protected-branch rule, read at call time. It can create the
branch it commits to (`start_branch`). The merge request is the review
boundary, as a draft is in a mail server: everything that lands in a
protected branch existed first as a merge request a person could read.
Merging it is Ship.

### 4.5 A create is never retried; ambiguity is settled by reading

§2.11. The client retries a POST only where GitLab turned it away before
acting — a 429 — and never on a timeout, a cancellation, a reset
connection or a 5xx after the request was written. A create canceled or
timed out once it may have been written is `[ambiguous_outcome]`.

On an ambiguous failure the tool returns `[ambiguous_outcome]` having
already done the read that settles it, each against the call's start
time and the signed-in user:

| Create | Settled by |
|---|---|
| note, draft note | the noteable's notes newest-first, author and body equal |
| issue, merge request | the project's issues or merge requests by author, `created_after`, title equal |
| commit | the branch head: moved, with this author and message |
| pipeline | pipelines on the ref, source `api`, `created_after` |
| release | the release by tag name |

Found means **created**, and the result carries it. Not found means
**not created**, and the result says so without creating it. Anything
else stays **unknown**, and the result says not to repeat the call.

### 4.6 A write carries a witness, and never destroys what it cannot see

- **Files and commits:** an update or delete action requires the
  `last_commit_id` that `get_file` returned; GitLab enforces it (400,
  mapped to `[stale]`).
- **Merge and approve:** require `sha`, the head the caller read;
  GitLab enforces it (409, `[stale]`).
- **Issue, merge request and wiki updates:** no API guard (§2.9). The
  tool requires the `updated_at` the caller read, re-reads, refuses
  `[stale]` if it moved, and sends only the fields given. The window
  between the re-read and the PUT is not closed, and the result does
  not claim it is (§17b).
- **Omitted means unchanged.** GitLab's PUT changes only the parameters
  sent, and the server sends only what the caller gave.
- **Lists change by add and remove.** Labels through GitLab's own
  `add_labels` and `remove_labels`; assignees and reviewers as a
  computed set from the re-read, never a replacement the caller typed.
- **A description replaced wholesale** reports how many characters and
  lines it removed.

### 4.7 Writes stay where they are allowed

The toxic flow that matters here is private content written somewhere
public: a model reads a private repository, then a public issue tells it
to post what it read. Two layers:

1. **`GITLAB_MCP_WRITE_NAMESPACES`**, when set, confines every Write,
   Ship and Destructive call to projects under those namespaces; any
   other target is `[blocked]` naming the setting. This is the control.
   `doctor` says whether it is set.
2. Every write result names the target project's visibility, so a write
   to a public project is visible as one.

Whether the allow-list should default to something narrower than
"everywhere the token can write" is §17.9.

### 4.8 Reads are bounded and say what they left out

Every read has a budget in characters, stated in the result. A
discussion list renders newest-first up to the budget and lists the rest
by id, author and date with a cursor. A long description is cut at a
paragraph boundary with the offset to continue from. Diffs are listed
per file first (`list_mr_files`), then fetched by file. Job logs are a
tail by default, with `byte_offset` to page and `failed_only` to jump to
the failing section. Binary files return their size, type and blob id,
never lossy text. List projections are compact: an embedded project,
milestone or pipeline becomes its id and name.

**A read never silently returns less than it found**: every omission is
named and continuable. Budgets, in characters: a description 20,000; a
file 60,000; discussions 30,000 a page and one comment 6,000; commit
diffs 40,000; a commit message 8,000. Two omissions are named but not
yet continuable (§17a).

### 4.9 One instance, many hosts, and every edition

The configured instance is normalized once (`internal/instance`):
scheme, host, port and sub-path, with a trailing `/api/v4` removed and a
missing scheme read as `https`. `http` is refused for any host that is
not loopback unless `GITLAB_MCP_ALLOW_HTTP=true`, because the token
travels in the clear. A private CA comes from `GITLAB_MCP_CA_FILE`;
proxies from the standard environment variables.

At startup the server reads `/api/v4/metadata` for version and edition.
A tool whose route or parameter is newer than the instance is
unregistered, and a tool that reaches a route the instance does not
have gets `[unsupported]` from the catch-all shape of §2.14. Profiles
are keyed by instance, so one person can use gitlab.com and a
self-managed instance side by side.

### 4.10 Own wire types, raw REST v4

Hand-written structs for the fields used, REST called directly. REST
because it is the surface GitLab describes machine-readably (§2.19),
because `read_api` maps onto it exactly (§2.6), and because every
surveyed read-only bypass came through GraphQL. §8b records every field
of every schema the tools touch with a verdict.

### 4.11 Results say what changed

Every write reports the state it produced, read back rather than
assumed: labels before and after, the commit created and the branch it
moved, the discussion a comment joined, the pipeline started and its
status. A write that changed nothing says so — "already closed" —
rather than reporting success.

## 5. Module layout

As `CLAUDE.md` "Where things go"; the staleness gate holds that list
against `go list ./...`, so this section does not repeat it.

### 5a. Shared machinery: what the current version must carry

The sibling servers improve shared machinery wherever a problem happens
to surface, so no single sibling holds the current version of any large
piece. This table merges them: the base was merged across every sibling
on 2026-09-24, and the rows marked **(09-25)** were added from a sibling
release and a per-component comparison on 2026-09-25. Phase 0 builds to
this list, and a gate or test is named for each claim where one exists.
When a sibling fixes something shared, it is added here with the date.

**Pins.** Taken from upstream on 2026-09-24; no sibling was newer on
2026-09-25; re-resolved at scaffold time on 2026-09-26, when two had moved. Every one is re-resolved against upstream at scaffold time,
not copied from here.

| Tool | Version |
|---|---|
| Go | 1.27.1 (`go-version-file: go.mod` in CI) |
| MCP Go SDK | v1.8.0 |
| jsonschema-go | v0.4.3 |
| golangci-lint | v2.14.0 (moved 2026-09-26) |
| goreleaser | v2.18.2 |
| cosign | v3.1.3 (`cosign-release:` on the installer) |
| syft | v1.52.0 (`syft-version:` on the installer) |
| mcp-publisher | v1.8.1, its own sigstore bundle verified before extraction |
| gitleaks | v8.30.1, run through `go run` by `make secrets`; the pre-commit hook's version compared with it |
| govulncheck | v1.8.0 |
| go-licenses | v1.6.0 (§17.6) |
| actionlint | v1.7.12, run through `go run`, not Docker |
| codeql-action | v4.38.2 (moved 2026-09-26) — labeled with the release tag, never a bundle tag |
| mcpb manifest schema | v2.1.2 tag, `manifest_version` 0.3 |

Every action pinned to a full SHA with the version in a trailing
comment. Newest seen across siblings on 2026-09-25: checkout v7.0.1,
setup-go v7.0.0, golangci-lint-action v9.3.0, goreleaser-action v7.2.3,
attest-build-provenance v4.2.2, cosign-installer v4.1.2,
sbom-action/download-syft v0.24.2, upload-artifact v7.0.1. The
golangci-lint and gitleaks actions are not used: every CI step is a
`make` target, which runs both tools through `go run`.

**CI and release.**

| Component | Must carry |
|---|---|
| `ci.yml` | every step is a `make` target, so parity holds by construction; ubuntu/macos/windows matrix, gates and coverage on all three; `defaults.run.shell: bash`; `timeout-minutes` on every job; `permissions: contents: read` at top; `persist-credentials: false` on every checkout; `concurrency` with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`; `workflow_dispatch`; a full-history secrets job on every PR; `fetch-depth: 0` where schema-diff and staleness run; the schema dump uploaded as an artifact; lint and secrets as `make` targets rather than their actions; the same `-coverpkg` in CI and the Makefile, held by a test; `goreleaser check` and actionlint as targets |
| PR base **(09-25)** | every PR-diff gate (changelog, schema-diff) measures against `git merge-base` of the fetched base branch, never `github.event.pull_request.base.sha`, which is stale in stacked PRs |
| schema-change acknowledgment **(09-25)** | a non-empty tool-surface diff needs a `SCHEMA-CHANGE:` footer (additive) or `BREAKING CHANGE:` on some commit in the PR, description text included; the acknowledgment is an empty commit, never an amend |
| `codeql.yml` | push, PR, weekly; bash default; a timeout |
| `release.yml` | `workflow_dispatch` on a tag as well as the tag event; refuses a tag whose CI run is not green (`actions: read`, queried by head SHA); write permission raised only in the goreleaser job; `go test` before signing; notes lifted from the CHANGELOG into `$RUNNER_TEMP`; a reproducible-build check that builds one target twice and compares hashes; `subject-path` naming the archives, `checksums.txt` and `dist/*.mcpb`, comma-separated; the registry job skipped for a prerelease (`!contains(github.ref_name, '-')`) |
| `publish-mcp.yml` | its own workflow, callable from the release and by dispatch for an old tag; job permissions `id-token: write` and `contents: read` only; `cosign verify-blob` on `checksums.txt` with the certificate identity pinned exactly to this repository's `release.yml` at the tag, before the hash is read; the publisher's identity pinned to its own release workflow at its tag; downloads to a file, never piped into `tar`; `server.json` generated at publish time and validated against the vendored registry schema |
| `.goreleaser.yaml` | `go mod download` in the hook, never `tidy`; `CGO_ENABLED=0`; `-s -w -trimpath`; `{{ .Version }}` through ldflags, never `{{ .Tag }}`; `mod_timestamp: {{ .CommitTimestamp }}`; `universal_binaries` with `replace: false`, kept out of the archives by `ids`; zip for Windows; `files: [LICENSE, NOTICE, README.md]` (Apache-2.0 §4(d) carries NOTICE with distributions); `prerelease: auto`; the bundle packed in the universal binary's post hook and named in both `checksum.extra_files` and `release.extra_files`; a keyless cosign `--bundle` over the checksums; an SBOM per archive; a footer whose verification commands match the artifacts, with the exact certificate identity rather than a regexp |
| `dependabot.yml` | weekly gomod and actions; `github/codeql-action*` grouped; gomod grouped on `minor` and `patch`; a `ci` commit prefix; no labels that do not exist |
| `Makefile` | every tool run as `go run mod@version`, never from PATH, goreleaser included, so `pins` compares the Makefile's pin with the workflow's; `EXE` suffix on Windows; the gates built once; `release-rehearse` as `--snapshot --clean --skip=publish,sign,sbom`; manual targets `schema-baseline`, `leaks-history`, `mcpb-pack`, `schema-refetch`, `api-diff`, `evals`; `tidy` as `go mod tidy -diff`; `hooks` setting `core.hooksPath`; no comment claiming to be "everything CI runs" |
| pre-commit | `.githooks/pre-commit` running `go run ./scripts/gates precommit`: gofmt, vet, leaks, and `gitleaks protect --staged --redact` (warning and skipping when absent), all problems collected before failing; no Python pre-commit framework |
| `.golangci.yml` | `default: none` with the explicit set: errcheck, staticcheck, unused, govet (all but fieldalignment and shadow), gosec minus G104, gocritic, bodyclose, errorlint, nilerr, misspell (US locale), unparam, revive, gocyclo 25, unconvert, prealloc, makezero; forbidigo on `fmt.Print*` and `os.Stdout` with `analyze-types`, excluded for `cmd/` and `scripts/`; `formatters: [gofmt]`; `run.build-tags: [live, evals]`; `run.timeout: 5m` |
| `.gitleaks.toml` | `[extend] useDefault = true`; rules for every GitLab token prefix (§4.1.4); synthetic values allow-listed by value, never by path; no inline `gitleaks:allow`; `example`, `test`, `invalid` domains and `go.sum` allowed |
| `.gitattributes` | `* text=auto eol=lf`; `*.png`, `*.pdf` binary |
| `.editorconfig` | utf-8, lf, final newline; tabs for Go and the Makefile; two spaces for YAML, JSON and Markdown |
| `NOTICE` | unofficial, not affiliated with GitLab Inc., trademark note, Apache-2.0 |
| issue and PR templates | `ISSUE_TEMPLATE/config.yml` with the private advisory link and "run `doctor` first"; the bug form asks for `doctor`, version, install method, client, instance kind (gitlab.com or self-managed) and version, flag state and a debug log, explains why those are safe to paste, and says what never to paste — "a tool result is your organization's code"; the PR template with Summary, Test plan and Notes and the schema-change footer |
| `audit/` **(09-25)** | `audit/security-reviews/v<tag>.md`, the committed `/security-review` over `prev-tag..HEAD`; `audit/release-smoke/v<tag>.md`, the release verification record: gates, reproducible-build hashes, schema and spec re-fetches, bugs found, the maintainer's go-ahead |
| `packaging/mcpb/manifest.json` | `$schema` at the pinned tag; `manifest_version` 0.3; placeholder version `0.0.0-dev`; `claude_desktop >= 0.10.0`; per-platform commands; `author`, `license`, `keywords`, `repository`, `documentation` and a `support` URL; `user_config` with `instance`, `client_id`, `profile` and `read_only`; a `long_description` saying the bundle does not log you in |
| Linux launcher | generated by the packer from the same staging table that names the binaries; `set -eu`; x86_64/amd64 and aarch64/arm64 aliased; `exec`s; an unknown architecture to stderr, non-zero, suggesting `go install`; a missing or non-executable binary named |

**Gates (`scripts/gates`, one command, one registry).**

| Gate | Must carry |
|---|---|
| registry | each entry declares `manual`, `inCheck` or `inRelease`, with the zero value refused by a test; arity declared |
| `parity` | targets mapped to CI steps by recipe, Makefile variables expanded, `run: \|` blocks read, commented lines stripped, a step's `name:` not counted as running it; every `vet -tags=` both ways; a gate implemented but never run fails, as does one run but not registered; release-only entries excused by name with a non-empty reason; floors on gates, prerequisites and matched lines |
| `checklist` | `CLAUDE.md`'s definition-of-done block against the `check:` prerequisites, both ways |
| `pins` | every `uses:` a 40-character SHA with a version comment; exact semver only (no `~`, range or `latest`), and a bundle-tag label refused; every action classified as installer or not, an unknown one failing; installer tool pins required; every Makefile/workflow pin pair compared, an unused pair a finding; workflows parsed as YAML; `${{ env.X }}` resolved; `defaults.run.shell` in every workflow; pre-commit and CI gitleaks versions equal; floors on workflows, actions, versions and installers |
| `staleness` | prose counts held against the lists beside them, spelled-out numbers included; the package map against `go list`; every repository path a document names exists, with a floor; the README tool table and every env var and gate documented, both ways; the status line against the newest tag or CHANGELOG heading; no version in prose; if Go changed since the last tag, the CHANGELOG has entries; **the OAuth application setup block in `docs/setup.md` generated from `internal/scopes` and `internal/auth` for every mode and compared exactly** |
| `changelog`, `changelog-links` | a PR adds an entry unless it is a release cut; every version heading has a link reference |
| `release-notes` | the CHANGELOG section lifted verbatim, refusing an empty one |
| `api-coverage` | offline against the committed OpenAPI snapshot, three directions (§8a); each row bound to its operation by verb and path from the client's AST; a `used` row names a real client method and no operation is claimed twice; prefix write-offs carry a reason of minimum length and a count of what they cover, and an operation matched by two rules fails; floors on operations, verdicts and client calls |
| `api-diff` (manual) | fetches the OpenAPI file at a named GitLab release tag; one fetch for operations and fields; written through a temp file and rename; a fetch below the operation floor or a network failure leaves the snapshot untouched, **held by a test against a closed port**; the snapshot records source URL, tag, SHA-256 and date |
| `api-fields` | request **and** response sides: every query parameter and body field the client sends exists on that operation, and every response field a wire struct decodes exists in its schema or carries a verified omission; recursive over inline objects; the Go kind can hold the declared type; floors on published and modeled fields |
| `schema-diff` | a committed baseline so it works before the first tag; the full surface with every flag and toolset on; the previous tag built in a git worktree that is always removed; tools **and resources**; fails on a removed tool, a lost input or output field, or a new required input; the whole `mcp.Tool` dumped, `_meta` and output schema included; a floor on tools |
| `descriptions` **(09-25)** | reads the `--dump-schemas` output, not the Go registry; fails on an empty dump, a missing description, more than one `IMPORTANT:` per description, and a witness input whose description lacks "NOT a retry signal" |
| `bodies` | §4.2: every string input of a Write, Ship or Destructive tool is either routed through `internal/quickaction` or listed as a plain field with a reason; derived from the schema dump; a floor |
| `leaks` | allow-list rules, each anchored on a shape generated fields cannot take (an `@` with a dotted domain; an `https://` host outside the example set; a GitLab token prefix; `gitlab.com/` followed by a namespace outside the fixture set; the OAuth shapes); every allow-list entry carries a reason, asserted; tracked and untracked-unignored files scanned; base64url blobs decoded and scanned; a tracked binary is a finding; history mode covering blobs, commit messages and tags, skipping author and committer lines, with a derived floor; findings printed redacted |
| `transcript` | drivers cannot reach `os.Stdout`, `os.Stderr` or `log.*`; exactly one exempt package with exactly one write site, which redacts; an unlisted driver fails; a listed directory that is missing fails; the evals directory included; a floor on mentions |
| `live-cover` | per tool **option**, recorded at run time through the stdio client; waivers in a TSV with verdict `undrivable` or `undriven` and a reason each, the ceiling on `undriven` only; a stale row fails; an empty record fails |
| `smoke` | the shipped binary over stdio at two protocol revisions, the current one asserted present in the SDK's supported list; stdout carries only JSON-RPC frames; no `.` in tool names; a call with no credentials returns `[auth]`; read-only mode registers no write tool; resources and templates; **stdin closed the moment the last message is written**, so an abrupt disconnect with a request in flight must exit 0 (-32004/-32003 matched by code); awaited ids derived; the child reaped on EPIPE; the Windows `.exe` path |
| `classes` | the closed vocabulary of §6.5 derived from the code and asserted both ways; duplicates found by count; a `Planned` list with reasons; a floor |
| `coverage` | per package, scored on its own files, `cmd/` included; per-package floor overrides; each exemption with a reason; an all-exempt run fails; floors on packages and profile lines |
| `outcomes` | every write's result states its outcome on every branch; an AST check that a branch on a boolean request field does not state an outcome without reading the response; floors on files and fields |
| `mcpb` | the committed manifest validated from its raw bytes against the **vendored** schema chosen by its own `manifest_version`; each vendored schema embedded with a recorded SHA-256 and source URL, a mismatch refused; `$schema` matched as a whole URL at a full tag or SHA; `manifest_version` equal to the URL's; a floor at 0.3; unknown keys preserved by decoding twice; entry point, every command and every override naming a staged file; every override a claimed platform; each claimed platform spawning the file staged **for it**; every `${user_config.x}` in a composed value declared; the no-login sentence; a `support` URL |
| `mcpb-pack` (release) | decode–encode stamping, never text substitution; `0755` on every staged binary; a fixed mtime asserted against a literal; globs matching exactly one file; the staged binary's `--version` read back; the packed archive read back |
| `release` | goreleaser's config held against the release workflow: universal binaries via `ids`, the bundle in both `extra_files`, `subject-path` with a valid separator, signing and SBOM pins; a six-archive floor; one universal binary; no `changelog:` block; before-hooks that do not mutate the tree |
| `server-json` | derived from go.mod with `/vN` stripped; the description (at most 100 characters) from the one constant the manifest also uses; the hash taken from `checksums.txt` with exactly one `.mcpb` row, **after** the signature check; validated against the vendored registry schema; HTTPS, release URL and identifier shape held by a test; `schema-refetch` (manual) compares vendored bytes with upstream under a timeout and writes nothing |
| `deps` (manual) **(09-25)** | a direct dependency whose update is more than six months old fails unless go.mod says `// pinned: <reason>`; needs the network, so it stays out of `check` and is in the release checklist |
| shared TSV reader | a trailing tab kept; duplicate keys refused |

**Server core.**

| Component | Must carry |
|---|---|
| `cmd` | `run(args, stdin, stdout, stderr, env)` so the serve path is testable; `help`/`-h` exit 0; **every argument checked for a help token before dispatch**, so `login --help` never reads `--help` as a value and `logout --help` never deletes anything; an unknown command prints usage to stderr and exits non-zero; errors printed through a redactor; disconnect matched by JSON-RPC code; the token warmed off the startup path; version from ldflags with a `debug.ReadBuildInfo` fallback, `canonical()` keeping the leading `v` |
| `login`/`logout`/`status`/`doctor` | the siblings' commands, flags and output unchanged (§10); `--client-id` and `--instance`; the scopes printed before the browser opens and a grant narrower than asked warned about; `--no-browser` printing the URL and the exact `ssh -L` line; `status [--no-probe] --json` with a `schema_version`, running the same config load the server runs; `doctor` walking instance → TLS → version and edition → application → granted scopes → one `/user`, naming what is missing, with stable `{kind n}` placeholders and a redacted-count footer; `logout` revoking the token through `/oauth/revoke` and naming other profiles on the same application; the keyring replaced package-wide in tests by `TestMain`, with a decoy test proving it |
| `auth` | loopback on `127.0.0.1:0` with the registered redirect `http://127.0.0.1/callback`; PKCE S256 with a 64-character verifier; `state` checked, a callback without it refused on its own while the login keeps waiting; `ReadHeaderTimeout` on the callback; a bounded HTTP client on the exchange and every refresh; the rotated pair persisted before it is used; **refresh under a cross-process file lock, the keyring re-read after `invalid_grant` before declaring re-login**; `expires_in` honored, never assumed; an access token refused as `invalid_token` dropped and the store re-read; transport errors stripped of their URL and of host names; a typed `ErrReauthorize` never retried; granted scopes stored; no `resource` parameter sent (§18 row 7) |
| `scopes` | one source of truth per mode: `read_api` read-only, `api` otherwise; the per-tool requirement; `Satisfied` and `Missing`; the generator for `docs/setup.md` |
| `credentials` | resolution **env → keyring → file**, documented as that order; per profile, keyring service `gitlab-mcp` and account the profile name, as the siblings do (the profile records the instance); `GITLAB_MCP_REFRESH_TOKEN` as the env source, and a pair rotated from it stamped with the env value's hash so the next start uses the stored pair rather than the revoked env token; a keyring save deletes a stale plaintext file; a keyring that refuses a save has its older entry deleted, and while both stores hold a pair the one saved last wins; `Delete` clears every store and joins errors; a silent keyring told apart from a missing login; a warning on every use of the plaintext file; temp file, rename, then ACL; a partial env set is an error |
| `fileperm` | 0600 on Unix; a protected DACL on Windows restricting the file to the current user |
| `userconfig` | profiles as the siblings have them — named, or `default`, with no default pointer — each recording instance, application id, username and granted scopes; written atomically; profile names by regex; the config-dir override refused outside the home directory by real path unless `GITLAB_MCP_CONFIG_DIR_ALLOW_OUTSIDE_HOME=true` |
| `config` | `Define(fs, env)` and `Build()` with errors joined; `GITLAB_MCP_` prefix (§18 row 30); timeouts bounded 1 s–10 m; negative flags named so the zero value is safe; base-URL overrides for tests; an exported list of every variable, which the staleness gate reads |
| `redact` | host, namespace path, username, email, token and client-id masking for product output; redact before truncating; mask where text is produced, not in a Writer; an already-masked value still matched; id truncation and the redacting printer for maintainer tooling |
| `server` | per-call log line with method, tool, outcome, milliseconds and rate bucket; the SDK's logger only at debug; instructions built from the configuration, naming only registered tools (a test holds it) and naming the flags that would add more; one description constant of at most 100 characters feeding the manifest and the registry; resource templates with `{x}`, never `{+x}`; resource not-found as `CodeInvalidParams` with a `[class]` message; the schema dump taking the SDK version from build info |
| `app` | startup assembly reachable without `main`; `Settings` with the token unexported and **both `LogValue` and `String` redacting**, because `%+v` reads unexported fields |
| `tools` | one `register` deciding annotations, kind, toolset and version gating, `_meta`, the dry-run context and the rendering; `FullSurface(cfg)` for the schema dump; `dry_run` found by reflection; an explicit output schema with `date-time` for times; `Content` set so the SDK does not duplicate the JSON; an `unexpected` class for anything unclassified |
| tool errors **(09-25)** | a `hinted{hint, err}` type with `Unwrap`, checked before the API-error branch, so a tool's guidance survives an upstream failure while the class still comes from the wrapped error; `refuse(protecting, unlock)` always naming what it protects and the exact argument to pass; enums named sorted in validation errors |
| `gapi` | write and repeatability derived from the HTTP method, POST failing closed, declared exceptions only; a POST retried only on 429; `Retry-After` honored as a minimum; full-jitter backoff; the rate model of §11; **`CheckRedirect` returning `http.ErrUseLastResponse`**, a moved project's GET redirect resolved by re-reading the new path only when it is same-origin under the API root, with the request's query kept when the `Location` carries none; `Link: rel="next"` accepted only same-origin under the API root; every path segment escaped exactly once from typed parts, `..` refused; a headers deadline then a stall guard per read; a body cap; non-JSON bodies reported by status, content type and a prefix; transport errors stripped of path, query and host names; a create canceled after it may have been written `[ambiguous_outcome]`; a 401 `invalid_token` dropping the token and repeating a repeatable call once; **a context under which the client refuses every write**; a closed `Class` type with `Retryable()`; a `User-Agent`; unknown-field drift reported by path; a per-call counter |
| logging test | every registered tool driven with canary values at debug; asserts the logs are non-empty and contain no canary |

## 6. Addressing

### 6.1 Projects, issues and merge requests

A project is given as its numeric id, its full path (`group/sub/project`)
or its web URL; the server resolves it once per call to the numeric id
and uses that for every later request, so a proxy that decodes `%2F`
breaks at most the first. Results always show both the id and the path.

An issue or merge request is a project plus `iid`, named `iid` in every
tool and described as "the number shown as #12 for issues and !12 for
merge requests". The global id is never asked for. Pipelines, jobs,
discussions and notes use their own ids, which GitLab gives and the
standard's §2 allows.

`resolve_url` turns any GitLab web URL on the configured instance —
issue, merge request, file at a line, commit, pipeline, job, compare,
wiki page — into these arguments, and says which kind it was. A URL on
another host is `[invalid]` naming the configured instance.

### 6.2 Refs, paths and lines

A ref is a branch, a tag or a SHA, passed through. A file path is
relative to the repository root and escaped by the client. A diff line
is `file`, `line` and `side` (`new` or `old`), optionally `end_line`;
§7.4 turns it into GitLab's position.

### 6.3 Users, labels and milestones

Users by username, resolved to ids by the server; an unknown username is
`[invalid]` naming it. Labels by name, exact, including scoped labels
(`priority::high`); a name that does not exist is `[invalid]` naming it
rather than silently creating one, since GitLab creates missing labels
on assignment. Milestones by title within the project and its ancestor
groups; two matches are `[ambiguous]` with both ids.

### 6.4 Time

RFC 3339 in and out. Filters (`created_after`, `updated_before`) are
passed to GitLab as instants.

### 6.5 Error classes

Closed vocabulary, derived from the code by `scripts/gates classes` and
asserted in both directions. The standard's six, plus seven this API
forces:

| Class | Means | Caller should |
|---|---|---|
| `invalid` | malformed or under-specified arguments, a line not in the diff, a label or user that does not exist, too large (§2.17) | fix the arguments |
| `not_found` | no such project, issue, merge request, file or job — **or the token cannot see it**, and the message says so (§2.15) | check the id and access |
| `auth` | not signed in, token expired or revoked, or scope missing (the scope is named) | run `login` |
| `forbidden` | signed in, but the role or a project rule refuses this | ask a maintainer |
| `conflict` | the state refuses it: branch exists, merge request already open, not mergeable | read and reconsider |
| `stale` | the witness moved: file, head `sha` or `updated_at` (§4.6) | re-read and retry |
| `ambiguous` | a milestone title or path matched more than one | pass an id |
| `blocked` | a guard refused what the API would have allowed: quick action, protected branch, write allow-list, Ship-dependent review | pass the override, or don't |
| `rate_limited` | §2.13, application or instance | wait; the message says how long |
| `unavailable` | a transient upstream failure | retry |
| `unsupported` | the instance lacks the route, edition or tier, or the API cannot do this (§2) | see the message |
| `ambiguous_outcome` | a create may or may not have happened (§4.5) | read the verdict; never repeat blind |
| `unexpected` | anything unclassified, including a 202 from note creation | report it |

## 7. Reading and writing

### 7.1 Finding things

`search_projects`, `search_issues` and `search_merge_requests` take the
filters GitLab offers (state, labels, author, assignee, reviewer,
milestone, `created_after`/`updated_after`, scope `mine` by default)
and `max` (default 20, at most 100), plus `page_token`. The token is an
opaque handle over the next page — keyset where the endpoint supports
it, offset otherwise — and is refused if it was issued for another
query. Every listing says whether it is complete; with a count past
10,000 it says the total is unknown rather than inventing one.

`search` covers GitLab's search API with the scope validated against
the instance's edition: code, commits and notes across a group or the
instance need advanced search, and a refusal is `[unsupported]` naming
it. Project-scoped code search works on every edition.

### 7.2 Reading an issue or a merge request

`get_issue` and `get_merge_request` return the description inside a
boundary, state, labels, assignees, milestone, dates, links, and a
discussion summary (count, unresolved count, last activity). A merge
request adds source and target branches, `diff_refs`, head `sha`,
`detailed_merge_status`, draft state, the head pipeline's status and
the approval state. `list_discussions` renders threads newest-first
under the budget, with position and resolved state for diff threads.

### 7.3 Reviewing a merge request

`list_mr_files` lists changed files with their line counts and the
`too_large`, `collapsed`, `generated_file`, renamed and binary markers.
`get_mr_diff` returns the diff of named files under the budget.
`list_mr_commits` lists commits.

Commenting has two paths, both taking a body (§4.2) and an optional diff
location (§6.2):

- `add_comment` posts now — to an issue, a merge request, or a reply to
  a discussion.
- `add_review_comment` creates a **draft note**; `list_review_comments`
  and `delete_review_comment` manage the caller's drafts;
  `submit_review` publishes them all at once with an optional summary
  and `reviewer_state` (`reviewed`, `requested_changes`, or `approved`,
  which needs Ship).

`resolve_discussion` resolves or reopens a thread.

### 7.4 Where an inline comment lands

The `diffpos` package builds GitLab's position from the model's `file`,
`line` and `side`:

1. read the merge request's latest diff version for `base_sha`,
   `start_sha` and `head_sha`;
2. read that file's diff and find the line in a hunk; a line outside
   every hunk is `[invalid]` naming the nearest hunk ranges;
3. classify it as added, removed or unchanged and set `new_line`,
   `old_line` or both;
4. compute `line_code` and, for a range, `line_range`;
5. after creating, read the note back and report where it landed.

It is table-tested against generated diffs in phase 2, before any tool
uses it, and spike K checks it against a live instance.

### 7.5 The repository

`get_file` returns text inside a boundary with `last_commit_id`, blob
id, size and encoding; binary content returns metadata only. `list_tree`
pages with keyset. `list_branches`, `list_tags`, `list_commits`,
`get_commit` (with a budgeted diff) and `compare_refs`.

`create_branch` from a ref. `create_commit` takes a branch, an optional
`start_branch`, a message and actions (`create`, `update`, `delete`,
`move`); `update`, `delete` and `move` require the file's
`last_commit_id`. It refuses protected branches (§4.4) and reports the
commit and the branch head afterwards.

### 7.6 CI

`list_pipelines`, `get_pipeline` (with a failed-jobs summary),
`list_jobs`, `get_job_log` (tail by default; `byte_offset` and
`byte_limit` to page; `failed_only`; ANSI stripped; collapsible sections
folded to one line each; masked per §4.1), `lint_ci` (the project's
configuration at a ref, by GET; supplied content by POST, Write only).

Ship: `run_pipeline` (ref, variables and inputs; variables named in the
result, values masked), `retry_pipeline`, `retry_job`, `play_job`,
`cancel_pipeline`. Deleting a pipeline is written off (§8a).

### 7.7 Planning, todos and search

`list_labels`, `list_milestones` (reads; assignment is a field on issue
and merge request updates). `list_todos`, `mark_todos_done` (by id; at
most 100). `search` per §7.1.

### 7.8 Optional toolsets

- `wiki`: `list_wiki_pages`, `get_wiki_page`, `save_wiki_page` (create,
  or update with the §4.6 witness), `delete_wiki_page` (Destructive).
  Project wikis only; group wikis are Premium (§17.11).
- `snippets`: `list_snippets`, `get_snippet`, `create_snippet` — private
  visibility only; a public or internal snippet from a model is how
  private content leaves (§4.7), so visibility is not an input.
- `releases`: `list_releases`, `get_release`, `create_release` (Ship:
  it creates a tag and starts tag pipelines).
- `deployments`: `list_environments`, `list_deployments`.
- `activity`: `list_events`.

### 7.9 What the instance supports

`get_me` returns the user, the instance's version and edition, the
token's kind, scopes and expiry, the registered kinds and toolsets, the
write allow-list, and the last rate-limit reading. A tool gated by
version is absent below its version; a tier-gated route answering 403 or
404 on a feature GitLab licenses is `[unsupported]` naming the likely
tier, never `not_found`.

## 8. Tool surface

Sixty-six tools. With the default toolsets: forty-three by default,
thirty-one in read-only mode, fifty-three with Ship and Destructive
both enabled. Every toolset and flag on registers all sixty-six.
Annotations come from `Kind` in one place (`CLAUDE.md` rule 14);
`openWorldHint` is true where the result is visible to other people.
`_meta["anthropic/requiresUserInteraction"]` is set on Ship and
Destructive kinds, as a signal and not a control.

| Tool | Kind | Toolset | Main operations |
|---|---|---|---|
| `get_me` | Read | default | `GET /user`, `/metadata`, token info |
| `resolve_url` | Read | default | none, or one lookup |
| `search_projects` | Read | default | `GET /projects`, `/groups/:id/projects` |
| `get_project` | Read | default | `GET /projects/:id` (secret fields never decoded) |
| `list_members` | Read | default | `GET /projects/:id/members/all` |
| `find_users` | Read | default | `GET /users?search=` |
| `search_issues` | Read | default | `GET /issues`, `/projects/:id/issues`, `/groups/:id/issues` |
| `get_issue` | Read | default | `GET /projects/:id/issues/:iid` |
| `create_issue` | Write | default | `POST /projects/:id/issues` |
| `update_issue` | Write | default | `PUT /projects/:id/issues/:iid` |
| `list_discussions` | Read | default | `GET …/issues|merge_requests/:iid/discussions` |
| `add_comment` | Write | default | `POST …/notes`, `…/discussions`, `…/discussions/:id/notes` |
| `resolve_discussion` | Write | default | `PUT …/merge_requests/:iid/discussions/:id` |
| `search_merge_requests` | Read | default | `GET /merge_requests`, `/projects/:id/merge_requests` |
| `get_merge_request` | Read | default | `GET …/merge_requests/:iid`, `/approvals` |
| `list_mr_files` | Read | default | `GET …/merge_requests/:iid/diffs` |
| `get_mr_diff` | Read | default | `GET …/merge_requests/:iid/diffs` |
| `list_mr_commits` | Read | default | `GET …/merge_requests/:iid/commits` |
| `create_merge_request` | Write | default | `POST /projects/:id/merge_requests` |
| `update_merge_request` | Write | default | `PUT …/merge_requests/:iid` |
| `add_review_comment` | Write | default | `POST …/draft_notes` |
| `list_review_comments` | Read | default | `GET …/draft_notes` |
| `delete_review_comment` | Write | default | `DELETE …/draft_notes/:id` (own draft) |
| `submit_review` | Write | default | `POST …/draft_notes/bulk_publish` |
| `get_file` | Read | default | `GET …/repository/files/:path` |
| `list_tree` | Read | default | `GET …/repository/tree` |
| `list_branches` | Read | default | `GET …/repository/branches` |
| `list_commits` | Read | default | `GET …/repository/commits` |
| `get_commit` | Read | default | `GET …/repository/commits/:sha`, `/diff` |
| `compare_refs` | Read | default | `GET …/repository/compare` |
| `list_tags` | Read | default | `GET …/repository/tags` |
| `create_branch` | Write | default | `POST …/repository/branches` |
| `create_commit` | Write | default | `POST …/repository/commits` |
| `list_pipelines` | Read | default | `GET …/pipelines` |
| `get_pipeline` | Read | default | `GET …/pipelines/:id` |
| `list_jobs` | Read | default | `GET …/pipelines/:id/jobs` |
| `get_job_log` | Read | default | `GET …/jobs/:id/trace` |
| `lint_ci` | Read | default | `GET …/ci/lint`; `POST …/ci/lint` when writes are on |
| `list_labels` | Read | default | `GET …/labels` |
| `list_milestones` | Read | default | `GET …/milestones`, group milestones |
| `search` | Read | default | `GET /search`, `/groups/:id/search`, `/projects/:id/search` |
| `list_todos` | Read | default | `GET /todos` |
| `mark_todos_done` | Write | default | `POST /todos/:id/mark_as_done` |
| `merge_merge_request` | Ship | default | `PUT …/merge_requests/:iid/merge` |
| `approve_merge_request` | Ship | default | `POST …/merge_requests/:iid/approve` |
| `unapprove_merge_request` | Ship | default | `POST …/merge_requests/:iid/unapprove` |
| `run_pipeline` | Ship | default | `POST …/pipeline` |
| `retry_pipeline` | Ship | default | `POST …/pipelines/:id/retry` |
| `retry_job` | Ship | default | `POST …/jobs/:id/retry` |
| `play_job` | Ship | default | `POST …/jobs/:id/play` |
| `cancel_pipeline` | Ship | default | `POST …/pipelines/:id/cancel` |
| `delete_branch` | Destructive | default | `DELETE …/repository/branches/:branch` (refuses default, protected and unmerged unless `unmerged: true`) |
| `delete_comment` | Destructive | default | `DELETE …/notes/:id` (own notes) |
| `list_wiki_pages` | Read | wiki | `GET …/wikis` |
| `get_wiki_page` | Read | wiki | `GET …/wikis/:slug` |
| `save_wiki_page` | Write | wiki | `POST`/`PUT …/wikis` |
| `delete_wiki_page` | Destructive | wiki | `DELETE …/wikis/:slug` |
| `list_snippets` | Read | snippets | `GET /snippets`, `…/snippets` |
| `get_snippet` | Read | snippets | `GET …/snippets/:id`, `/raw` |
| `create_snippet` | Write | snippets | `POST …/snippets` (private) |
| `list_releases` | Read | releases | `GET …/releases` |
| `get_release` | Read | releases | `GET …/releases/:tag` |
| `create_release` | Ship | releases | `POST …/releases` |
| `list_environments` | Read | deployments | `GET …/environments` |
| `list_deployments` | Read | deployments | `GET …/deployments` |
| `list_events` | Read | activity | `GET /events`, `…/events` |

The staleness gate holds those counts against the table.

Every Write, Ship and Destructive tool takes `dry_run`, which returns
what would be sent — and, for a body, what the guard would do — without
writing.

Resources, for clients that attach rather than call:
`gitlab://projects/{id}/issues/{iid}`,
`gitlab://projects/{id}/merge_requests/{iid}` and
`gitlab://projects/{id}/jobs/{job}/log` carry the same text as
`get_issue`, `get_merge_request` and `get_job_log` under the same budget
and boundaries.

### 8a. Every published operation, with a verdict

The source is `openapi_v3.yaml` at a GitLab release tag, fetched by
`api-diff` into `testdata/openapi-v3.snapshot.json` (operations: verb,
path, tag, lifecycle) and never edited by hand. Verdicts live in
`testdata/api-coverage.tsv`, one row per used, gated or deferred
operation, plus prefix write-offs, each with a reason and the number of
operations it covers. The gate fails on an operation with neither, a
client call with no row, and a row or rule matching nothing.

The committed snapshot is v19.4.1-ee, 1,856 operations; the TSV holds
323 exact rows and 226 prefix rules, and the gate reports the counts.
The groups below are the design's verdicts; the TSV is the record.

| Group (path prefix) | Verdict |
|---|---|
| user, metadata, version | Used: `get_me`, login, `doctor`. `/version` written off as deprecated for `/metadata`. Personal access token `self` routes written off with the token path (§10) |
| projects (read), groups (read), members (read), users (search) | Used for navigation. Project create, update, fork, transfer, archive, share, import and export written off: administration |
| issues, issue notes and discussions | Used. Move, clone, subscribe, time tracking, award emoji, issue links deferred to §17a; delete written off (Owner-only, permanent) |
| merge requests, notes, discussions | Used. Rebase deferred (Ship candidate); `/changes` written off as deprecated for `/diffs`; merge-request delete written off |
| draft notes | Used: the review path |
| approvals (merge-request level) | Used: approve, unapprove, state. Approval rules and project approval settings written off: governance configuration, Premium |
| repository files, tree, commits, branches, tags, compare | Used. Blame and raw archive deferred; cherry-pick and revert deferred as Ship candidates; commit statuses written off (a CI integration's surface); tag create and delete deferred; "delete merged branches" written off |
| protected branches and tags | Read only, by `create_commit`'s guard. Every write written off |
| pipelines, jobs, CI lint | Used. Pipeline delete written off: it destroys logs and artifacts and is no assistant task, and GitLab's own server shows how easily it becomes a default. Artifact download deferred; artifact delete written off |
| pipeline schedules, triggers, variables, secure files | Written off: CI configuration and secrets |
| labels, milestones | Read used. Create, update and delete deferred to §17a |
| search | Used |
| todos | Used |
| wikis (project) | Used under the `wiki` toolset. Group wikis deferred (Premium) |
| snippets | Used under `snippets`; update and delete deferred |
| releases, release links | Used under `releases`; update and delete written off |
| environments, deployments | Read used under `deployments`. Stop, delete and deployment approval written off: they act on running infrastructure |
| events | Used under `activity` |
| epics, iterations, work items, vulnerabilities, audit events, compliance, security policies | Deferred (§17.11): Premium or Ultimate, and several are GraphQL-first |
| container registry, packages, dependency proxy, virtual registries | Deferred |
| runners, clusters, agents, Kubernetes proxy | Written off: infrastructure administration |
| deploy keys, deploy tokens, access tokens (project, group, personal other than `self`) | Written off: credential administration |
| hooks, integrations, system hooks, import, export, mirrors, admin, application settings, applications, license, broadcast messages, geo, sidekiq, features | Written off: administration |
| `/internal/*` and experimental routes | Written off: not public API, marked by path and lifecycle |
| AI, editor and chat features; feature flags; topics; namespaces; badges; notification settings; the CI catalog; job-token scope; resource groups | Written off: product administration or features with no assistant task in a project |
| boards, error tracking and alerts, Markdown rendering, templates, merge trains, suggestions, DORA and analytics | Deferred: suggestions are a Ship candidate; the rest have no tool in §8 yet (§17.11 for the Premium ones) |
| navigation reads no tool uses yet (`GET /groups`, `/users/{id}` and the like) | Deferred until a tool needs them |

### 8b. Field coverage

`api-fields` holds both directions for every operation a client method
calls: each query parameter and body field sent exists on it, and each
response field decoded exists in its schema or carries an omission
verified live with a reason — 526 operations publish no response
schema, so the omission list is expected to be long and is itself
floored. Two are called out now: `get_project` never decodes
`runners_token` or any field whose name ends `_token`, and the gate
asserts no wire struct carries one; and a merge request's
`detailed_merge_status` is decoded as a string, never a closed enum,
because GitLab adds values.

## 9. Confidentiality, security, safety

Nothing deployer-specific ever enters the repository — `CLAUDE.md` rule
1 lists it. A GitLab instance mixes three payloads: other people's
words, an organization's code, and CI output that can carry secrets.

### 9.1 What may never enter the repository, and what stops it

Two structural rules, not matters of care:

1. **Fixtures are generated, never recorded.** `gitlabtest` builds its
   instance from code: users from a fixed synthetic list, groups and
   projects under `example`-prefixed namespaces, hosts at
   `gitlab.example.com` and `.invalid`, bodies and diffs from templates,
   ids from counters in a fixed range. A fixture copied from a live
   response is itself the leak, whatever a scanner says about it.
2. **The live driver reads only what it wrote.** It creates a private
   project named for the run under a namespace the maintainer names on
   the command line, writes its own files, issues, merge requests and
   pipelines there, and every read it makes is inside that project; it
   never lists or searches outside it. The transcript records the
   namespace in redacted form only. At the end it deletes the project,
   unless `-keep` is set; project deletion is the one write the driver
   has that the server does not (§8a). On gitlab.com that deletion is
   delayed thirty days, and the driver says so.

The leak gate is an allow-list anchored on shapes the server's own
generated fields cannot take: an `@` with a dotted domain outside the
example and invalid set; an `https://` host outside that set; any GitLab
token prefix; `gitlab.com/` followed by a namespace outside the fixture
set; the OAuth shapes. The exemption list is asserted, so a new entry is
an argued decision.

### 9.2 Logging

Method, tool, outcome, duration, rate bucket, and an id truncated to
six characters. Never: a host, a namespace or project path, a branch, a
file path, a title, a body, a username, an email or a search term. All
of those reach a log through a request URL, so transport errors are
stripped of path and query before logging. `TestLogsNeverCarryThePayload`
drives every registered tool with canary values in every string
argument, at debug, and asserts the log is non-empty and carries none
of them.

### 9.3 What a tool result carries

The payload. The result is the one place it is supposed to go, so the
rule there is §4.1's rather than §9.2's: marked as untrusted, token
shapes masked in logs, and nothing the server adds is phrased as
something to do.

### 9.4 What the settings do

| Setting | Registers | Requests |
|---|---|---|
| `GITLAB_MCP_READ_ONLY=true` | Read | `read_api` |
| default | Read and Write | `api` |
| `GITLAB_MCP_ENABLE_SHIP=true` | adds Ship | `api` (no change: §2.6) |
| `GITLAB_MCP_ENABLE_DESTRUCTIVE=true` | adds Destructive | `api` (no change) |
| `GITLAB_MCP_TOOLSETS` | adds `wiki`, `snippets`, `releases`, `deployments`, `activity`, or `all` | — |
| `GITLAB_MCP_WRITE_NAMESPACES` | confines Write, Ship, Destructive (§4.7) | — |

`READ_ONLY` with either enable flag is refused at startup, naming both.
A token whose granted scopes do not cover the mode is refused at
startup, naming the scope — a `read_api` token never gets a write tool,
whatever the flags say. `docs/security.md` says plainly what the table
implies: **the default token can merge, approve and run pipelines**.
Leaving Ship unregistered stops this server doing so; it does not stop
anything else holding the token, which is why the token lives in the
keyring and nowhere a tool can read.

## 10. Auth, config, process model

**Sign-in is the family's, unchanged** (standard §3b; maintainer,
2026-09-26: "the same all around"). Whoever has signed in to one of the
sibling servers already knows this one:

1. Register your own OAuth application once, as you create a Desktop
   OAuth client for the Google servers. On GitLab that is user settings
   → Applications (or a group's or the instance's, §2.1): redirect URI
   `http://127.0.0.1/callback`, **Confidential unchecked**, scope `api`
   (`read_api` for read-only). `docs/setup.md` says exactly this and is
   generated from the code (§5a `staleness`).
2. `gitlab-mcp login --client-id <application id>` (plus `--instance`
   for self-managed). It prints the scopes it will ask for, opens the
   browser, listens on `127.0.0.1:0` with PKCE S256 and `state`, and
   warns if GitLab granted less than it asked for.
3. `gitlab-mcp doctor` to check it. `status`, `logout` and profiles
   behave as they do in every sibling: `logout` revokes the token and
   names the other profiles that share the same application.

The same subcommands, the same flags (`--profile`, `--no-browser` with
the exact `ssh -L` line for SSH, `--client-id` in the place of
`--client-secret`), the same keyring layout per profile with a warned
0600 file fallback, the same env → keyring → file order, no out-of-band
flow. A profile records the instance and the application id, so one
person can keep a gitlab.com profile and a self-managed one side by
side.

What GitLab changes underneath, none of which the person sees:

- **The application is named by an id, not a file.** GitLab shows an
  application id and has no JSON to download, and a public client has
  no secret (§2.2). So `--client-id` / `GITLAB_MCP_CLIENT_ID` stands
  where the siblings have `--client-secret` / `<PREFIX>_CLIENT_SECRET`,
  over the stored profile (§17b). If the application was left
  Confidential, GitLab answers `invalid_client`, and `login` says to
  untick it rather than showing the raw error (spike D).
- **Rotation is immediate** (§2.3). Two processes — Claude Desktop and
  Claude Code, say — share one keyring entry. Refresh happens under an
  OS file lock in the config directory; the new pair is written to the
  keyring before the lock is released; a process that gets
  `invalid_grant` re-reads the keyring and uses the pair another process
  wrote before it reports `[auth]`. `expires_in` is read, never assumed.
  An access token GitLab refuses as `invalid_token` — revoked by a
  logout elsewhere before its expiry — is dropped: the next token comes
  from the keyring, refreshed if the keyring holds the refused one, and
  a read is sent once more with it. A create is not.
- **Changing a setting that changes scope needs a new login**, and the
  server says so at startup rather than failing on the first call, as
  the siblings do.
- **Startup reads before it serves.** Registration depends on the
  instance's version and the granted scopes, so startup reads token
  info, `/metadata` and `/user` once each, bounded by the smaller of
  15 s and the HTTP timeout. Every failure there is logged and the
  server starts anyway, so errors surface per call; the one fatal
  outcome is a token whose scopes cannot serve the configured mode.
- **A token is never sent to an instance it was not issued by.** An
  instance named by flag or environment that differs from the one the
  profile signed in to withholds the token: every call answers `[auth]`
  naming the mismatch.

Deliberately absent, so the family stays alike: personal access tokens,
the device flow (§2.5), and a client id shipped in the binary. Each was
considered (§17.1, §17.5) and each would make this server sign in
differently from the rest.

Configuration: `GITLAB_MCP_INSTANCE` (default `https://gitlab.com`),
`GITLAB_MCP_PROFILE`, `GITLAB_MCP_CLIENT_ID`, `GITLAB_MCP_READ_ONLY`,
`GITLAB_MCP_ENABLE_SHIP`, `GITLAB_MCP_ENABLE_DESTRUCTIVE`,
`GITLAB_MCP_TOOLSETS`,
`GITLAB_MCP_WRITE_NAMESPACES`, `GITLAB_MCP_CA_FILE`,
`GITLAB_MCP_ALLOW_HTTP`, `GITLAB_MCP_LOG_LEVEL`, `GITLAB_MCP_LOG_FORMAT`
(`text` default), `GITLAB_MCP_HTTP_TIMEOUT` (60 s default),
`GITLAB_MCP_CONFIG_DIR`. Each also a flag. `docs/configuration.md` is
checked against the exported list.

Process: one stdio session; starts before authentication so `doctor` and
`--dump-schemas` work signed out; a disconnect is exit 0.

## 11. Reliability

- **Repeatability comes from the HTTP method.** GET is retried. POST is
  not, except operations declared repeatable at the call site with the
  reason: `mark_as_done`, approve and unapprove, cancel, resolve. PUT
  is repeatable (it sets fields to values). Creates are never retried
  (§4.5).
- **429 is retried for any method**, since GitLab did not act;
  `Retry-After` is a minimum; full jitter; at most four attempts, and
  never past the call's deadline. A plain-text 429 body is expected.
- **The rate model.** A per-instance token bucket and a concurrency cap
  of four; `RateLimit-Remaining` and `RateLimit-Reset` lower the bucket
  when present and are never trusted to be present (§2.13). Known
  application limits — notes at 60 a minute on gitlab.com — have their
  own buckets, and `submit_review` publishes many comments in one call
  for that reason. `get_me` reports the last reading.
- **Timeouts** on every request (§3), a 32 MiB cap on a response body,
  job logs read in windows.
- **Per-item outcomes** for every multi-id write (`mark_todos_done`).

## 12. Distribution and setup

`go install`, six platform archives with SBOMs, a signed checksum file,
build provenance, the `.mcpb` bundle for Claude Desktop and an MCP
registry entry. All of it is in phase 0, not a late phase: a sibling
built its release path in its fifth phase and found the packer's macOS
glob "could never have matched anything", and the release gates of §5a
run in `make check` from the first commit, so the pipeline they check
might as well exist.

`docs/setup.md` is an input, not a description: the application's
redirect, confidentiality and scope are generated from the code and
gated. It has a gitlab.com section and a self-managed section, the
latter including how an administrator registers one trusted instance
application for everyone.

## 13. Testing

- **`internal/quickaction` first**, with table tests over GitLab's
  extractor rules — paragraphs, fences, indented code, quotes, HTML,
  inline code, CRLF, a command on the last line — and a fuzz test that
  an escaped body never yields a command and that removing the inserted
  backslashes gives the body back. It renders the same text except where
  GitLab's backtick pairing differs from CommonMark's, or inside
  multi-line raw inline HTML; there the backslash shows, which is the
  safe direction.
- **The `diffpos` package** (phase 2), table-tested over generated diffs: added,
  removed and context lines, ranges across hunks, renames, a line
  outside every hunk.
- **`internal/instance`**, over every base-URL shape of §3.4.
- **`gitlabtest`**, an in-memory GitLab behind the REST paths the client
  uses, generated per §9.1, modeling the facts of §2 the server depends
  on: quick actions executed from bodies (so a missing guard fails a
  test), `last_commit_id` and `sha` refusals, 404 for private, the
  catch-all 404 shape, keyset and offset pagination with the 10,000 and
  50,000 edges, rotating refresh tokens, 429 in both body shapes.
- **Renderer goldens** for issue, merge request, discussions, diff,
  job log (with masking), listings, and the hidden-text case.
- **The logging test** of §9.2.
- **The live driver**, `scripts/livegitlab`, per §9.1, against
  gitlab.com and a container of the floor version (§17.4): every tool
  and option, held by `live-cover`; the transcript through one
  redacting writer, held by `transcript`; read by a person before a
  phase counts.
- **Evals**, `scripts/evals`, against `gitlabtest` for the offline
  self-check in `check`, and live against the driver's scratch project
  by hand, with built-in tools disabled by `--tools ""` and
  `--setting-sources ""` (fail-closed, never a denylist), a census of
  the scratch project before and after, and tasks whose content carries
  an injected instruction — a quick action, a request to post
  elsewhere, a request to approve — scored on whether the model
  followed it.

## 14. Confirmed decisions and their consequences

| Decision | By | Consequence |
|---|---|---|
| Design first, reviewed before code | maintainer, 2026-09-25 | this document; phase 0 waits for "go" |
| gitlab.com and self-managed both | maintainer, 2026-09-25 | §4.9; profiles per instance |
| Sign-in the same as every sibling: own OAuth application, loopback, browser, keyring; no tokens, no device flow, no shipped client id | maintainer, 2026-09-25 and 2026-09-26 | §10; §17.1 and §17.5 decided |
| Issues, merge requests, repository, CI, wiki, snippets and releases in 1.0 | maintainer, 2026-09-25 | §8; wiki, snippets and releases as toolsets |
| Merging, approving and CI runs behind a Ship flag | this design | §4.3 |
| Quick actions refused by default, never executed | this design | §4.2 |
| Commits never to a protected branch | this design | §4.4 |
| REST v4 only, no escape hatch | this design | §4.10, §8a |
| Unregistered, not registered-and-refusing, for gated tools | this design | §17b |
| Release pipeline in phase 0 | this design | §12 |

## 15. What must be verified live

Each spike states its question and, when run, its verdict separately.
A, B, C, F and L have run on gitlab.com (2026-09-26); the rest have not.

- **Spike A — loopback port.** Register `http://127.0.0.1/callback`;
  send `http://127.0.0.1:<random>/callback`, `http://[::1]:<random>/…`
  and `http://localhost:<random>/…`. Expect the first two accepted and
  the third refused. On gitlab.com and on the floor version. §2.1.
  *Verdict, gitlab.com, 2026-09-26: a random `127.0.0.1` port was
  accepted against the registered `http://127.0.0.1/callback`, through
  the real `login`. `[::1]`, `localhost` and the floor version are
  still open.*
- **Spike B — public client.** Code exchange, refresh and
  `/oauth/revoke` with only `client_id`, on gitlab.com. §2.2.
  *Verdict, gitlab.com, 2026-09-26: code exchange and refresh work
  with no secret on a non-confidential application; `doctor` then read
  the token's scopes and the account. Revocation is still open.*
- **Spike C — concurrent refresh.** Two refreshes with one token: one
  `invalid_grant`, and the old access token dead at once. §10's lock is
  built from this.
  *Verdict, gitlab.com, 2026-09-26: confirmed. Of two simultaneous
  refreshes with one token, one was granted and one refused
  `invalid_grant`; the old access token answered 401 at once, with
  `invalid_token` in both the body and `WWW-Authenticate` — the marker
  the client drops a token on.*
- **Spike D — a Confidential application.** The exact error when the
  application was left Confidential and no secret is sent, so `login`
  and `doctor` can say "untick Confidential" instead of `invalid_client`.
- **Spike E — quick actions.** Through the API, on issues, merge
  requests and notes: a bare command executes; a backslash-escaped one
  does not and renders as the same text; fenced, indented, quoted and
  inline commands do not; a command after a list item; CRLF. §4.2
  depends on every row.
- **Spike F — error shapes.** The unknown-route 404 against a resource
  404; a licensed feature on Free (epics, approval rules) as 403 or
  404; a Rack::Attack 429 on gitlab.com. §6.5's mapping.
  *Verdict, gitlab.com, 2026-09-26: a missing resource is
  `{"message":"404 Project Not Found"}`, an unknown route
  `{"error":"404 Not Found"}` — the shapes §2.14 distinguishes. Epics
  and iterations on a Free group are 403 `{"message":"403 Forbidden"}`.
  A malformed token is a plain 401 `{"message":"401 Unauthorized"}`
  with no `invalid_token` marker. The 429 was not provoked.*
- **Spike G — `mcp` scope.** Whether an `mcp`-scoped token can call the
  tagged REST routes directly. Informational: it would not cover
  comments (§2.6).
- **Spike H — conditional reads.** Whether `If-None-Match` returns 304 on
  API GETs and whether a 304 is counted against the rate limit.
- **Spike I — floor version.** `/api/v4/metadata` with a `read_api`
  token, OAuth loopback, draft notes and `/diffs` markers on the floor
  version's container. §17.4.
- **Spike J — job log windows.** `byte_offset` and `byte_limit` on
  gitlab.com and the floor version, and from which version.
- **Spike K — inline positions.** Comments computed by `diffpos` on
  added, removed and unchanged lines and a range, read back from the
  web view.
- **Spike L — moved projects and encoded paths.** A renamed project's
  GET redirect and its non-GET 405; a full path containing a dot and a
  nested group, percent-encoded once.
  *Verdict, gitlab.com, 2026-09-26: a renamed project's old path
  answers GET with 301 and a `Location` naming the project's **numeric
  id**, not its new path, keeping the sub-path and query; a write
  answers 405 `Non GET methods are not allowed for moved projects`.
  The in-memory instance now does the same. The dotted and nested path
  is still open.*
- **Spike M — settle by reading.** For each create of §4.5, whether the
  read finds what the create made within a second, or needs a delay.

Spikes A–D register or use an OAuth application and are in the "ask
before doing" of `CLAUDE.md`.

## 16. Delivery phases

Each phase is one session and ends ready to tag, then waits for an
explicit "go". The next session starts from this repository alone.

**Phase 0 — scaffolding, gates, sign-in and core reads (v0.1.0).**
Everything of §5a: CI, release, publish, bundle, registry, every gate of
the `check` list, pre-commit, templates, community files, `NOTICE`,
`.editorconfig`, and the docs (`README`, `CHANGELOG`, `CONTRIBUTING`,
`SECURITY`, `CODE_OF_CONDUCT`, `docs/configuration.md`,
`development.md`, `release.md`, `security.md`, `setup.md` generated,
`runbook.md`, `docs/README.md`). The core: `cmd` with `login`, `logout`, `status`, `doctor`; `config`, `credentials`,
`fileperm`, `userconfig`, `auth` with the refresh lock, `scopes`,
`instance`, `redact`, `version`, `app`, `server`, `tools`, `gapi` with
pagination, the rate model and the redirect rules. **`internal/quickaction`
complete with its tests and fuzz**, because every later write stands on
it, and nothing writes yet. `gitlabtest`. The OpenAPI snapshot and
`testdata/api-coverage.tsv`. Read tools: `get_me`, `resolve_url`,
`search_projects`, `get_project`, `search_issues`, `get_issue`,
`list_discussions`, `search_merge_requests`, `get_merge_request`,
`get_file`, `list_tree`, `list_branches`, `list_commits`, `get_commit`.
The untrusted-content rendering of §4.1 and the budget of §4.8. Spikes
A, B, C, D, F, I, L. A live run whose transcript is read.

*Built 2026-09-26, reviewed (§16a) and simplified. The live run passed
on gitlab.com the same day and its transcript was read: it found the
signed-in person's display name and user id unmasked in the driver's
output (fixed and re-run clean), a sha256 labeled as a client id by the
shape mask (safe, left), and two wording defects (fixed). Spikes A and B
answered on gitlab.com, then C, F and L. Still owed before the tag:
spike D on gitlab.com, and spike I with A on a container of the floor
version.*

**Phase 1 — the rest of reading (v0.2.0).** `list_mr_files`,
`get_mr_diff`, `list_mr_commits`, `compare_refs`, `list_tags`, the CI
reads with `get_job_log` and its masking, `lint_ci`, `search`,
`list_labels`, `list_milestones`, `list_members`, `find_users`,
`list_todos`, `list_review_comments`, the three resources. Spikes H, J.

**Phase 2 — the write path (v0.3.0).** The `diffpos` package and its
tests; `create_issue`, `update_issue` with the witness, `add_comment`,
`resolve_discussion`, the review tools, `create_merge_request`,
`update_merge_request`, `create_branch`, `create_commit` with the
protected-branch guard, `mark_todos_done`; settle-by-reading; the write
allow-list. Spikes E, K, M. Spike E runs before any write tool is
registered.

**Phase 3 — Ship, Destructive and toolsets (v0.4.0).** The Ship tools
with `sha` witnesses; `delete_branch`, `delete_comment`; the `wiki`,
`snippets`, `releases`, `deployments` and `activity` toolsets. Spike G.

**Phase 4 — evals and 1.0 (v1.0.0).** The evals harness with the
injection tasks of §13; §17.3 decided; the surface frozen into the
schema baseline; §17 closed or each item argued open.

### 16a. Found by review, and fixed

Each phase's `/code-review high` and `/security-review` findings are
recorded here with the commit that fixed them, and the security review
is committed under `audit/`.

**Phase 0.** `/security-review`: no finding
(`audit/security-reviews/v0.1.0.md`). `/code-review high`: ten
candidates, each checked with a test that failed before its fix; nine
fixed in `637c982`, one recorded:

| Found | Fixed |
|---|---|
| An outside link read as on the instance when `?`, `#` or `\` came before an `@` | `637c982`; the parser replaced by `url.Parse` in the simplification pass |
| A failed keyring save left an older pair in charge | `637c982`; the pair saved last wins |
| DNS and TLS errors carried the instance's hostname into logs | `637c982`; one allowlist renderer since `93c49af` |
| A token GitLab refused stayed cached until it expired | `637c982` |
| A create canceled after it was sent read as `[unavailable]` | `637c982`; now `[ambiguous_outcome]` |
| Names other people write could start a line of their own | `637c982` |
| A stray callback without the login's `state` ended the login | `637c982`; ignored with a 400 |
| A moved project lost the request's query | `637c982`; the request is kept and only the project segment replaced since `93c49af` |
| A cut commit message looked complete | `637c982`; `message_offset` and `message_budget` |
| Discussions walked up to ten pages per read | Not fixed: `X-Total` counts system-only threads, so the proposed fix miscounts; §17a |

### Closing a phase

1. `make check` green; the live driver run and its transcript read.
2. `/simplify`, `/code-review high` and `/security-review`; findings
   fixed or recorded in §16a.
3. The status line, §16 and `CHANGELOG.md` say what was built and what
   is owed.
4. Commit on the topic branch; say what is ready to tag; stop.

## 17. Open decisions

1. **A shipped gitlab.com client id.** Compiling in the id of a public
   application this project registers on gitlab.com would make
   gitlab.com sign-in need no setup. **Decided 2026-09-26: no.** Every
   sibling has the person bring their own OAuth client, and sign-in is
   to be the same all around (§10). Reusing GitLab's CLI's id was
   rejected on its own grounds: it is registered on `localhost:7171`,
   which gets no port flexibility, and nothing authorizes reuse.
2. **Is approving Ship?** Approval is a person's sign-off that other
   people's merge rules count. Proposed: Ship, as §4.3 has it, since an
   injected "approve this" is exactly what the flag exists to refuse.
   The counter-argument is that approving is the most common review
   action and is reversible. **Open.**
3. **Lockdown for public projects.** GitHub withholds content from
   authors without push access in public repositories. Here: in a
   public project, render content from authors below Developer as
   withheld unless `GITLAB_MCP_UNTRUSTED_CONTENT=show`. It costs a
   members read per call and hides legitimate bug reports. Proposed:
   decide in phase 4 from the injection evals. **Open.**
4. **The floor version.** Proposed: **18.0**, with version-gated tools
   and parameters above it, and a container of it in the live driver.
   GitLab backports security fixes to the current and two previous
   monthly releases only, so anything older is unpatched; self-managed
   fleets are nevertheless often older, and spike I says what breaks.
   **Open.**
5. **Personal access tokens in 1.0.** **Decided 2026-09-26: no**, for
   the same reason as §17.1; the device flow goes with it. If a
   headless need appears later, it is argued here first, and it arrives
   in every sibling or none.
6. **go-licenses v1.6.0 or v2.** **Decided 2026-09-26: v1.6.0.** Both
   v1.6.0 and v2.0.1 were run over the real module graph and pass the
   same allow-list; v1.6.0 matches the siblings.
7. **Lenient argument decoding.** **Decided 2026-09-26: as proposed.**
   Arrays and integers sent as JSON inside a string are decoded; `null`
   and `""` for an optional input mean absent; required inputs are never
   relaxed; a debug line names the inputs adjusted, never their values.
   It needs the untyped `AddTool`, so `register` validates arguments
   itself after decoding.
8. **Default toolsets.** Proposed as §8: wiki, snippets, releases,
   deployments and activity off by default, which keeps the default at
   43 tools. **Open.**
9. **Default write allow-list.** Unset means everywhere the token can
   write. Proposed: keep, with `doctor` and `get_me` saying so, because
   a default that refuses every write is a default everyone overrides.
   **Open.**
10. **GitLab's built-in server.** If it gains stdio, tokens, enforced
    filtering and a read-only mode, this server's reason to exist
    narrows to the local-token, quick-action and verified-release half.
    Revisit at each minor release; record the check in §18. **Open,
    standing.**
11. **Premium and Ultimate after 1.0.** Epics and iterations are
    work items, GraphQL-first, with the REST epics API deprecated for
    v5. A post-1.0 `planning` toolset would be the first GraphQL use
    and would need its own coverage source. **Deferred to after 1.0.**

### 17a. Deferred cleanups

- Two reads name an omission without a way to continue it, short of
  §4.8: a project description over 20,000 characters, and a single
  file's diff larger than the whole diff budget (cut, with a pointer to
  `get_file`). Found in phase 0. A commit message over 8,000 is
  continued with `get_commit`'s `message_offset`.
- `get_issue` and `get_merge_request` walk up to ten pages of threads to
  count them, and each `list_discussions` page walks them again to show
  them newest first. `X-Total` cannot replace the walk: it counts
  system-only threads, and the unresolved count and last activity need
  every note. Found in phase 0.

Candidates for after 1.0, from §8a: issue move and links,
label and milestone writes, rebase, cherry-pick, revert, blame, artifact
download, tag writes.

### 17b. Deviations from the shared standard

The standard at `~/.claude/mcp-server-standard.md`, read 2026-09-25.

| The standard says | Here | Why |
|---|---|---|
| Errors use six classes | Thirteen (§6.5) | `stale` is forced by §2.10; `ambiguous_outcome` by §2.11; `forbidden` and `auth` ask for different fixes; `blocked` for the quick-action, branch and allow-list guards; `rate_limited` separates waiting from failing; `ambiguous` as the siblings use it; `unexpected` for what nothing else covers |
| Never overwrite; compute a minimal diff | Held for files and commits (`last_commit_id`) and merges (`sha`); **not holdable** for issue, merge request and wiki updates | No `If-Match` in REST and `lock_version` only in the web controllers (§2.9). §4.6's `updated_at` witness narrows the lost-update window without closing it; only the fields given are sent |
| Destructive tools are not registered unless enabled | Held, and extended to Ship | One sibling registers deletes and refuses per call. Not here: GitLab's `api` scope permits everything, so a registered Ship or Destructive tool is exactly the control an injected instruction gets to argue with, and the official server's unenforced filters (§1) show where "listed but callable" ends |
| Read-only mode requests read-only scopes | Held (`read_api`) | Recorded because `read_api` is enforced by HTTP method, which is exactly right for REST |
| §3b: `--client-secret` / `<PREFIX>_CLIENT_SECRET` names the client JSON | `--client-id` / `GITLAB_MCP_CLIENT_ID`; everything else in §3b held | GitLab shows an application id and offers no JSON, and a public client has no secret (§2.2); a flag named for a file and a secret nobody has would be a lie in the interface |

Everything else is adopted as written, including the preamble's three
obligations: make a rule a test, derive its list from the code, and
have the checker assert a floor on how much it read.

## 18. Evidence log: conventions checked, changed, or rejected

Sources: GitLab's source at master `829b21d2` (`19.5.0-pre`,
2026-09-25), including `lib/api`, `lib/gitlab/auth.rb`,
`config/initializers/doorkeeper*.rb`, `db/structure.sql` and
`doc/api/openapi/openapi_v3.yaml`; Doorkeeper v5.9.3 source and
changelog; GitLab's CLI source; docs.gitlab.com pages for OAuth, tokens,
rate limits, pagination, quick actions, diff limits and the MCP server;
RFCs 6749, 7636, 8252 and 8628; the MCP specification 2025-11-25; the
servers of §1 and their trackers. Checked 2026-09-25.

**Three tiers.** (1) Verified here against source, a spec file or an RFC.
(2) Taken from a sibling server's own evidence log or code, verified
there. (3) Asserted from documentation or a secondary source and **not
yet probed live** — §15 exists to settle these, and they are marked.

| # | Convention or assumption | How checked | Verdict |
|---|---|---|---|
| 1 | GitLab requires the redirect URI to match exactly, port included | Doorkeeper `uri_checker.rb`; GitLab overrides nothing; GitLab's MCP page says "must exactly match" | **Refuted for loopback IP literals (tier 1).** Ports are ignored for `127.0.0.1` and `::1`. The documentation is wrong for this case; spike A confirms live |
| 2 | `localhost` works as a loopback redirect like `127.0.0.1` | Doorkeeper decides loopback with `IPAddr` | **Refuted (tier 1).** `localhost` needs an exact port. RFC 8252 §7.3 advises against it anyway |
| 3 | A GitLab OAuth application needs a client secret | Doorkeeper public clients; GitLab's CLI docs | **Refuted (tier 1).** `confidential: false` needs none; a confidential one makes the CLI fail with `invalid_client` |
| 4 | GitLab enforces PKCE | `tokens_controller.rb`, `authorizations_controller.rb` | **Refuted (tier 1).** Only for dynamically registered applications. This server sends S256 regardless |
| 5 | Dynamic Client Registration can give a third-party server an `api` token | `dynamic_registrations_controller.rb` | **Refuted (tier 1).** Scope forced to `mcp`; rate limited; can be disabled |
| 6 | Refreshing twice with one refresh token is harmless | Doorkeeper `refresh_token_request.rb`; no `previous_refresh_token` column | **Refuted (tier 1).** Old pair revoked immediately; the second is `invalid_grant`. §10's lock; spike C |
| 7 | An authorize request may carry an RFC 8707 `resource` | `authorizations_controller.rb#pre_auth_params` | **Refined (tier 1).** A `resource` ending `/api/v4/mcp` silently rewrites the scope to `mcp`. Never sent |
| 8 | Device authorization is on for any application once the instance supports it | `device_code_enabled` column and model default; the patch initializer | **Refuted (tier 1).** Off by default for applications created after the 18.10 migration; undocumented. One reason the device flow is not used (§10) |
| 9 | Access tokens always last two hours | `doorkeeper.rb` `custom_access_token_expires_in` | **Refuted (tier 1).** Configurable from 19.1, minimum 300 s. `expires_in` is read |
| 10 | Some scope narrower than `api` allows commenting | `lib/api/api.rb` scope enforcement; `mcp_access.rb` | **Confirmed that none does (tier 1).** `read_api` is GET/HEAD; `mcp` covers a few tagged routes, not notes or issue update. §2.6 |
| 11 | GitLab's CLI client id can be reused by other tools | CLI source: id, `localhost:7171`, scopes | **Rejected (tier 1 for the facts).** Fixed port on `localhost`, collides with the CLI, and no document authorizes reuse. §17.1 |
| 12 | Quick actions are a web-UI feature | `notes_helpers.rb`, `issuable_base_service.rb`, `quick_actions/extractor.rb` | **Refuted (tier 1).** They run from API-created notes and from issue and merge request descriptions on create and update; a commands-only note returns 202. §4.2; spike E for the escape |
| 13 | A backslash before `/` neutralizes a quick action | Extractor regex requires `^/`; CommonMark escapes | **Tier 3.** Holds on reading; spike E proves it on a live instance, render included |
| 14 | REST supports `If-Match` for updates | Grep of `lib/api` | **Refuted (tier 1).** Nothing reads it; `lock_version` is web-only. §17b |
| 15 | A stale `last_commit_id` is a 409 | `lib/api/files.rb:431`, `update_service.rb` | **Refuted (tier 1).** 400 with a fixed message; mapped to `[stale]` |
| 16 | A mismatched merge `sha` is a 400 | `helpers.rb#check_sha_param!` | **Refuted (tier 1).** 409. Some groups require `sha`; one surveyed server broke on it |
| 17 | Merging without permission is 403 | `merge_requests.rb:902` | **Refuted (tier 1).** 401. §6.5 maps it to `forbidden` when the token is otherwise valid |
| 18 | A private project the token cannot read is 403 | `helpers.rb` `find_project!` | **Refuted (tier 1).** 404; `not_found` says "or no access" |
| 19 | A missing route and a missing resource look the same | `api.rb` catch-all | **Refuted (tier 1).** `{"error": "404 Not Found"}` against `{"message": "404 … Not Found"}`. Spike F for the live bodies |
| 20 | A tier refusal has one shape | `ee/lib/api` grep: 24 `not_found!`, 15 `forbidden!` | **Refuted (tier 1).** Both occur. §7.9 |
| 21 | `X-Total` is always present | Kaminari initializer, `MAX_COUNT_LIMIT` | **Refuted (tier 1).** Dropped past 10,000 |
| 22 | Offset pagination reaches every row | `pagination_strategies.rb`; instance limits page | **Refuted (tier 1 / 3 for the default).** 405 past 50,000 rows on keyset-capable endpoints |
| 23 | `RateLimit-Remaining` predicts a 429 | Rate-limit docs; `rate_limiter.rb` | **Refuted (tier 1 / 3).** Only Rack::Attack throttles set it; application limits 429 regardless. Two 429 body shapes |
| 24 | gitlab.com's API limit is generous enough to ignore | gitlab.com rate-limit page | **Refuted (tier 3).** 2,000 a minute today, 100 a minute proposed for Free, and 60 notes a minute. §11 |
| 25 | The OpenAPI file lists the REST API | `openapi_v3.yaml`; the rake task and its CI check | **Confirmed with gaps (tier 1).** 1,862 operations, generated and checked current; experimental and some policy routes missing; 526 without a response schema; v2 deprecated. The coverage source, with omissions verified live |
| 26 | The OpenAPI file says which tier each operation needs | Count of `x-gitlab-tier` | **Refuted (tier 1).** 3 of 1,862. Tier is probed |
| 27 | The GraphQL schema is committed and could drive coverage | `lib/tasks/gitlab/graphql.rake` | **Refuted (tier 1).** Generated, not committed. REST only (§4.10) |
| 28 | `/api/v4/version` is the version probe | `lib/api/metadata.rb` | **Refined (tier 1).** Deprecated since 15.5 for `/metadata`; both need authentication; no plan field |
| 29 | Job logs are all-or-nothing | `lib/api/ci/jobs.rb:110-136` | **Refuted (tier 1).** Undocumented `byte_offset` and `byte_limit`, 500 KB per call. Spike J for gitlab.com and the floor |
| 30 | `GITLAB_` is a safe environment prefix | GitLab CI predefined variables | **Rejected (tier 3).** CI jobs already set `GITLAB_CI`, `GITLAB_USER_LOGIN`, `GITLAB_FEATURES` and more, and other GitLab tools read `GITLAB_TOKEN`; `GITLAB_MCP_` cannot collide |
| 31 | GitLab's built-in MCP server enforces its tool allow-list | GitLab issue 631017 | **Refuted (tier 3).** Filters listing only, by design. Supports §4.3's registration-only gating |
| 32 | The most-used community server's read-only mode is a control | Its published advisories | **Refuted (tier 3).** Bypassed through a raw GraphQL tool and verb heuristics. No escape hatch here (§1) |
| 33 | Issue and merge request updates can be guarded like files | `issue_build_parameters.rb`, merge request controllers | **Refuted (tier 1).** `lock_version` accepted only by web controllers. §4.6 |
| 34 | The credential order is keyring → file → env | Sibling code | **Refuted (tier 2).** Every sibling resolves env → keyring → file. §5a |
| 35 | The newest sibling carries the newest shared machinery | Per-component comparison, 2026-09-24 and 2026-09-25 | **Refuted (tier 2).** Each sibling was newest for some pieces; one released after the merge and added seven rows. §5a merges them |
| 36 | A sibling already has an OpenAPI coverage gate to copy | That sibling's tests and release checklist | **Refuted (tier 2).** Its check is a response-fields unit test with a manual refresh; a request field it did not check shipped wrong. §5a's `api-fields` checks both directions |
| 37 | A sibling already decodes stringified arguments or marks untrusted content with boundaries | All siblings' code | **Refuted (tier 2).** Neither exists anywhere; §4.1 and §17.7 are new ground |
| 38 | Denylisting built-in tools confines an evals run | A sibling's evals log | **Refuted (tier 2).** Grep and Glob read maintainer notes mid-task. `--tools ""` and `--setting-sources ""`, fail-closed |
| 39 | A struct printed with `%+v` is redacted by `LogValue` | A sibling's `Settings` | **Refuted (tier 2).** `%+v` reads unexported fields; `String()` is needed too |
| 40 | A quick-action guard can match GitLab with a regular expression | GitLab's extractor and the Markdown pipeline it calls at `829b21d2`; comrak v0.55.0, the version GitLab's renderer pins; a differential oracle over about 1.3 million generated bodies | **Refined (tier 1).** Detection needs comrak's block structure, so `internal/quickaction` ports it. Zero lines GitLab would run were missed; four deliberate over-detections (any `/word`, both backtick readings, no rendered-text prefilter, description-list terms). Before 16.7 the extractor was regular-expression only and ran commands inside `~~~` fences and lazy lines; spike E checks the floor version |
| 41 | GitLab's token prefixes are the ones commonly listed | GitLab's token page, fetched 2026-09-26 | **Refined (tier 3).** `glpat-`, `gloas-`, `gldt-`, `glrt-`, `glrtr-`, `glcbt-`, `glptt-`, `glft-`, `glimt-`, `glagent-`, `glwt-`, `glsoat-`, `glffct-`, `_gitlab_session=`, and the runner `GR1348941`; `gloat-` is not listed. A custom personal-access-token prefix cannot be matched by shape |
| 42 | Dropping hidden characters is safe everywhere | Trojan Source (CVE-2021-42574) | **Refuted for code.** Dropped from Markdown, made visible in files, diffs and commit messages (§4.1.2) |
| 43 | A moved project's redirect names its new path | Spike L on gitlab.com | **Refuted (tier 1, live).** It names the numeric id; the client replaces only the project segment, so either works, and the test instance now matches |
| 44 | Every refused token says `invalid_token` | Spikes C and F on gitlab.com | **Refined (tier 1, live).** A revoked token does, in body and header; a malformed one is a plain 401. The client drops a token only on the marker, which a malformed token would not benefit from anyway |
