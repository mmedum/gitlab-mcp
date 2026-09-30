# Architecture — gitlab-mcp

**Status: 2.0.0, 2026-09-29: phases 0 to 7 — the person now confirms
what ships or deletes (§4.12) — `update_comment`, and the sign-in no
longer refreshed before the server serves. The module path is `/v2`. §17.10 stands and
§17.11 waits** This document holds the platform facts, the design bets, a
verdict on every API operation group, the phase plan and the spikes that
must answer before the phases that depend on them.

## 1. Mission and scope

A production-grade Go MCP server for GitLab, distributed to other
people. One binary, stdio, per-user sign-in, no hosted deployment. It
works against **gitlab.com, and nothing else**: self-managed instances
are out of scope (§4.9; maintainer, 2026-09-26, §14).

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
stdio binary that signs in to gitlab.com exactly as its Google siblings
do and keeps its token in the OS keyring; **a curated surface of about fifty tools** rather
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

- **Self-managed instances.** gitlab.com only: no instance setting, no
  private CA, no version gating (§4.9, §14).
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
   `expires_in` seconds — 7,200 by default; configurable by
   administrators from 19.1 down to 300 (self-managed only; out of
   scope). §10.
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
   administrators can relax it on self-managed (out of scope).
   Fine-grained tokens are GA from 19.2 and cover about 1,270 of about
   1,583 route declarations. Not used (§10); recorded because policy on
   them is what people will ask about.
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
    general throttles off (self-managed only; out of scope). §11.
14. **Errors come in four shapes.** `{"message": "404 Project Not
    Found"}`; `{"message": {"field": ["…"]}}` for validation (400 or
    422); `{"message": {"error": "…"}}` for spam and application limits;
    `{"error": "…"}` for parameter validation and **for a route that
    does not exist**, which the catch-all answers `{"error": "404 Not
    Found"}`. That last shape is how a route the account's tier lacks —
    or, on self-managed (out of scope), an older instance's — is told
    apart from a missing resource (spike F).
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
    3 of 1,862 operations. Tier is probed, never assumed. The version
    matters only on self-managed (out of scope): gitlab.com runs the
    newest release, so the version is informational here. §7.9.
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
   reported. *Here there is no base URL to configure: the instance is
   gitlab.com (§4.9).*
5. A private CA, `HTTPS_PROXY` and `NO_PROXY` work; self-signed
   instances were the most-reported setup failure. *Here the proxies
   from the environment work; a private CA is self-managed only and out
   of scope (§4.9).*
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
   author's role in the project. Content from authors below Developer
   in public projects is not withheld, as GitHub's lockdown does: §17.3
   says why.
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
naming it. So is `update_issue` making a confidential issue public
(`confidential: false`), which shows it to everyone who can see the
project's issues, the widening `move_issue` refuses; an injected "it was
confidential by mistake" asks for exactly that (1.0 security review).

The standard's rule that destructive tools are unregistered is held.
One sibling registers deletions and refuses per call, arguing that a
tool that does not exist cannot explain itself; here the server
instructions name the flags and what they add, which answers that
without registering the tool (§17b).

Toolsets sit beside kinds (§8): `wiki`, `snippets`, `releases`,
`deployments`, `activity` and `planning` are off by default so the
default surface, fifty-three tools, stays under the 64-tool point where one
client starts regrouping tools. `GITLAB_MCP_READ_ONLY=true` beats every other setting.

### 4.4 Code reaches a protected branch only through a merge request

`create_commit` refuses the project's default branch and every
protected branch: one that exists by its own `protected` flag, one it
would create by the protected-branch rules, read at call time, and
refused when there are more rules than one read covers. It can create
the branch it commits to (`start_branch`). `create_branch` refuses a
name the rules cover in the same way, since a new protected branch at a
ref of the caller's choosing is code in a protected branch no merge
request showed (phase 2 security review). The merge request is the
review boundary, as a draft is in a mail server: everything that lands
in a protected branch existed first as a merge request a person could
read. Merging it is Ship.

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
- **Issue and merge request updates:** no API guard (§2.9). The
  tool requires the `updated_at` the caller read, re-reads, refuses
  `[stale]` if it moved, and sends only the fields given. The window
  between the re-read and the PUT is not closed, and the result does
  not claim it is (§17b).
- **Comment edits:** GitLab's note PUT ignores `If-Unmodified-Since`
  (§18 row 93), so `update_comment` re-reads and compares `updated_at`
  as the issue updates do, with the same window open. The same text
  asked again reads as unchanged before the witness is compared, so an
  edit repeated after a lost answer is not `[stale]`. The answer is
  read back: an `updated_at` that did not move is `[unexpected]`, and a
  text stored otherwise is reported.
- **Wiki pages:** GitLab's wiki API exposes neither a version nor an
  `updated_at` (`lib/api/entities/wiki_page.rb`), so the witness is
  `content_sha256`, a hash of the content `get_wiki_page` returned. A
  change or delete re-reads, compares, and refuses `[stale]`; the same
  window stays open.
- **Deletes:** a branch carries its head `sha`, compared with a fresh
  read, since GitLab's own branch delete checks only an author date. A
  comment carries its `updated_at`, sent as `If-Unmodified-Since`, which
  GitLab enforces (412, `[stale]`): the one conditional request its REST
  API honors for a delete this server makes.
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
   `doctor` says whether it is set. `mark_todos_done` is held to it by
   each item's project, read once for that when the setting is on.
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
file or a merged CI configuration 60,000; discussions or review drafts
30,000 a page and one comment 6,000; the diffs of a commit, a merge
request or a comparison 40,000; a commit message 8,000; one search
excerpt 1,000; a wiki page or a snippet file 60,000, as a file; release
notes 20,000, as a description. A job log is windowed in bytes of the stored log: 40,000
by default, at most 100,000, each window widened to whole lines so no
secret straddles its edge. A comparison lists 100 commits at a time.
Every omission is continuable: a project description by `offset`, and
one file's diff larger than the diff budget by `diff_offset`.

### 4.9 gitlab.com, and nothing else

The instance is `https://gitlab.com`, fixed in the code
(`instance.GitLabCom`); no setting names another (§14, 2026-09-26).
Proxies come from the standard environment variables and trust from the
system's certificate authorities.

One development override exists, `GITLAB_MCP_TEST_INSTANCE`, with no
flag: the tests, the evals and the smoke gate point the binary at the
in-memory instance with it. It is refused at startup unless its host is
loopback (`127.0.0.1`, `::1` or `localhost`), so it can never send a
token to another real host, and it is logged at warn when set. It is
documented in `docs/development.md` only; the staleness gate keeps it
out of `docs/configuration.md` and the `mcpb` gate out of the bundle.
Plain `http` is accepted for loopback alone.

gitlab.com runs the newest release, so no tool is gated by version.
`get_me` and `doctor` read `/api/v4/metadata` to report the version and
edition, as information. A route the account's tier lacks — a
Premium-only route on a Free namespace — gets `[unsupported]` from the
catch-all shape of §2.14.

A profile records the instance it signed in to, and its token is sent
nowhere else: a profile signed in to the test instance never sends its
token to gitlab.com, and the reverse (§10).

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

### 4.12 A write that ships or deletes is confirmed by the person

Registration decides what the server can do (§4.3), and `confirm:
true` is an argument the model writes, which a persuaded model writes
too. So when the client can ask, the server asks the person itself,
through MCP form elicitation, before thirteen writes: `merge_merge_request`,
`approve_merge_request`, `play_job`, `create_release`, `create_tag`,
`run_pipeline` on the default branch or a protected branch or tag,
`update_issue` when it makes a confidential issue public, and the six
deletes of the Destructive kind. The set is the core the maintainer
chose on 2026-09-29 (§14): the writes that ship, publish or destroy.
Retrying, cancelling, rebasing, moving and commenting do not ask, since
questions asked often are answered without reading (§18 row 94).

1. **A second gate, not a replacement.** Registration, `confirm` and
   every guard stay and are checked first. A call a guard refuses asks
   nothing. The question comes after every read, just before the write,
   so it shows what the write would do.
2. **Accepting is the confirmation.** The form has no fields: an empty
   object schema. Anything but `accept` — decline, cancel, an error, an
   answer that came back after its question expired — is `[blocked]`,
   and nothing is sent. A call that comes back with anything but an
   accept is refused before it reads anything, so a write whose question
   depends on what GitLab holds is not made on a round that would no
   longer ask. The refusal says the call was "not confirmed by
   the person" and names the client's answer; it never says the person
   declined, since a client can answer without showing anyone anything.
   Two clients accept an empty form without a person choosing to: Codex
   under approval policy `never` with full access, and VS Code when the
   person skips the question. A required choice naming the outcome would
   stop both, and was declined after the maintainer's check found it
   slower and less clear than Accept (§18 row 96).
3. **No question possible.** A client that declares no form elicitation
   gets no question, and the flags and `confirm` are the guard, as
   before. `GITLAB_MCP_REQUIRE_PROMPT=true` refuses those writes as
   `[blocked]` instead.
4. **A dry run never asks.** Nor does a pipeline on a ref no rule
   protects, or an issue update that keeps it confidential. `run_pipeline`
   reads the ref and asks when GitLab marks it the default branch or
   protected. A `refs/heads/` or `refs/tags/` ref is read as the branch
   or tag it names, as GitLab reads it, and a ref that is neither is
   asked about, since its protection cannot be told. It reads only when a
   question could go out, so a client that cannot ask pays no extra
   read.
5. **What the question says.** The tool, the target and the
   consequence, in the server's words: a merge by its merge request,
   title, branches, head and whether it squashes or removes the source
   branch; a job by its name, stage and pipeline and the keys of the
   variables it is given, never their values; a delete by what it
   removes and what goes with it. Text from GitLab or from the call
   stands in a code span, between backticks, on one line: hidden and
   control characters removed; backticks, grave and acute marks, and
   quote marks made a plain single quote; a URL scheme, `mailto:`,
   `www.` and a bare domain followed by a path broken so no client draws
   a link; cut at 120 characters, a comment at 300 with the count of the
   rest. A client that draws the question as Markdown shows a code span
   literally, and a blank line between lines keeps them apart (§18 row
   95). A closing line says text in backticks or code style is not the
   server's.
6. **One handler on every protocol.** The handler returns the question
   as an input request, the multi-round-trip pattern of 2026-07-28.
   Before that revision the SDK asks with `elicitation/create` and calls
   the handler again within the same request (§18 row 94). A client
   failure there is a JSON-RPC error inside the SDK, and middleware turns
   it into `[blocked]`.
7. **The answer is bound to its question.** `requestState` is signed
   with HMAC-SHA256 under a key drawn per process. It carries the tool,
   a hash of the arguments, witnesses included, a hash of what the
   question binds, a nonce and an expiry. A retry is refused when its
   state is forged, for another call, expired or already used, and
   answers on a call with no state are refused. The retry reads again;
   if what it binds differs from what was answered, it is refused, and
   the next call asks again. A question binds its text, and the whole
   head sha, branches or comment where the text shows less; a label's open-issue
   count is shown and not bound, and its name is bound instead.
8. **The expiry applies where the state travels.** From 2026-07-28 the
   client carries `requestState` between the rounds, and it expires 5
   minutes out. Before, it never leaves the process: the request itself
   waits for the person.
9. **A create goes at most once (§4.5).** The first round stops before
   the write. Only the verified retry writes, and its state is spent
   before the handler runs, so a replay is refused.
10. **A failure after the answer is never "nothing was written".** Once
    an answer has confirmed the write, a call that then fails without a
    result is `[ambiguous_outcome]`: verdict `written` when the handler
    returned from its write, `unknown` otherwise. A failure while the
    question is still out is `[blocked]`.
11. **Held in one place.** A tool asks when its spec says when it asks,
    and `register` refuses at start a Destructive tool that does not.
    The service asks at its write, and a write reached with no way to ask
    is refused. The description of each asking tool says so, and a test
    derives the asking tools from the definitions and holds each:
    declined, nothing sent; accepted, the write.

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
| issue and PR templates | `ISSUE_TEMPLATE/config.yml` with the private advisory link and "run `doctor` first"; the bug form asks for `doctor`, version, install method, the client and its version, flag state and a debug log, explains why those are safe to paste, and says what never to paste — "a tool result is your organization's code"; the PR template with Summary, Test plan and Notes and the schema-change footer |
| `audit/` **(09-25)** | `audit/security-reviews/v<tag>.md`, the committed `/security-review` over `prev-tag..HEAD`; `audit/release-smoke/v<tag>.md`, the release verification record: gates, reproducible-build hashes, schema and spec re-fetches, bugs found, the maintainer's go-ahead |
| `packaging/mcpb/manifest.json` | `$schema` at the pinned tag; `manifest_version` 0.3; placeholder version `0.0.0-dev`; `claude_desktop >= 0.10.0`; per-platform commands; `author`, `license`, `keywords`, `repository`, `documentation` and a `support` URL; `user_config` with `client_id`, `profile` and `read_only` (no instance: gitlab.com only); a `long_description` saying the bundle does not log you in |
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
| `cmd` | `run(args, stdin, stdout, stderr, env)` so the serve path is testable; `help`/`-h` exit 0; **every argument checked for a help token before dispatch**, so `login --help` never reads `--help` as a value and `logout --help` never deletes anything; an unknown command prints usage to stderr and exits non-zero; errors printed through a redactor; disconnect matched by JSON-RPC code; no refresh before serving; version from ldflags with a `debug.ReadBuildInfo` fallback, `canonical()` keeping the leading `v` |
| `login`/`logout`/`status`/`doctor` | the siblings' commands, flags and output unchanged (§10); `--client-id`, and no instance flag (§4.9); the scopes printed before the browser opens and a grant narrower than asked warned about; `--no-browser` printing the URL and the exact `ssh -L` line; `status [--no-probe] --json` with a `schema_version`, running the same config load the server runs; `doctor` walking instance → TLS → version and edition → application → granted scopes → one `/user`, naming what is missing, with stable `{kind n}` placeholders and a redacted-count footer; `logout` revoking the token through `/oauth/revoke` and naming other profiles on the same application; the keyring replaced package-wide in tests by `TestMain`, with a decoy test proving it |
| `auth` | loopback on `127.0.0.1:0` with the registered redirect `http://127.0.0.1/callback`; PKCE S256 with a 64-character verifier; `state` checked, a callback without it refused on its own while the login keeps waiting; `ReadHeaderTimeout` on the callback; a bounded HTTP client on the exchange and every refresh; the rotated pair persisted before it is used; **refresh under a cross-process file lock, the keyring re-read after `invalid_grant` before declaring re-login**; a store unreadable under the lock refused rather than the held refresh token spent; no refresh before the server serves while a login recorded the scopes; a refresh in progress waited for before exit; `expires_in` honored, never assumed; an access token refused as `invalid_token` dropped and the store re-read; transport errors stripped of their URL and of host names; a typed `ErrReauthorize` never retried; granted scopes stored; no `resource` parameter sent (§18 row 7) |
| `scopes` | one source of truth per mode: `read_api` read-only, `api` otherwise; the per-tool requirement; `Satisfied` and `Missing`; the generator for `docs/setup.md` |
| `credentials` | resolution **env → keyring → file**, documented as that order; per profile, keyring service `gitlab-mcp` and account the profile name, as the siblings do (the profile records the instance); `GITLAB_MCP_REFRESH_TOKEN` as the env source, and a pair rotated from it stamped with the env value's hash so the next start uses the stored pair rather than the revoked env token; a keyring save deletes a stale plaintext file; a keyring that refuses a save has its older entry deleted, and while both stores hold a pair the one saved last wins; `Delete` clears every store and joins errors; a silent keyring told apart from a missing login; a warning on every use of the plaintext file; temp file, rename, then ACL; a partial env set is an error |
| `fileperm` | 0600 on Unix; a protected DACL on Windows restricting the file to the current user |
| `userconfig` | profiles as the siblings have them — named, or `default`, with no default pointer — each recording the instance it signed in to (so its token goes nowhere else), application id, username and granted scopes; written atomically; profile names by regex; the config-dir override refused outside the home directory by real path unless `GITLAB_MCP_CONFIG_DIR_ALLOW_OUTSIDE_HOME=true` |
| `config` | `Define(fs, env)` and `Build()` with errors joined; `GITLAB_MCP_` prefix (§18 row 30); timeouts bounded 1 s–10 m; negative flags named so the zero value is safe; no instance setting, and one development override, `GITLAB_MCP_TEST_INSTANCE`, env only and refused unless loopback (§4.9); an exported list of every variable, the override marked, which the staleness and `mcpb` gates read |
| `redact` | host, namespace path, username, email, token and client-id masking for product output; redact before truncating; mask where text is produced, not in a Writer; an already-masked value still matched; id truncation and the redacting printer for maintainer tooling |
| `server` | per-call log line with method, tool, outcome, milliseconds and rate bucket; the SDK's logger only at debug; instructions built from the configuration, naming only registered tools (a test holds it) and naming the flags that would add more; one description constant of at most 100 characters feeding the manifest and the registry; resource templates with `{x}`, never `{+x}`; resource not-found as `CodeInvalidParams` with a `[class]` message; the schema dump taking the SDK version from build info |
| `app` | startup assembly reachable without `main`; the instance fixed to gitlab.com or the loopback test override, logged at warn when overridden; a profile signed in to another instance keeps its token; `Settings` with the token unexported and **both `LogValue` and `String` redacting**, because `%+v` reads unexported fields |
| `tools` | one `register` deciding annotations, kind, toolset and scope gating (no version gating: §4.9), `_meta`, the dry-run context and the rendering; `FullSurface(cfg)` for the schema dump; `dry_run` found by reflection; an explicit output schema with `date-time` for times; `Content` set so the SDK does not duplicate the JSON; an `unexpected` class for anything unclassified |
| tool errors **(09-25)** | a `hinted{hint, err}` type with `Unwrap`, checked before the API-error branch, so a tool's guidance survives an upstream failure while the class still comes from the wrapped error; `refuse(protecting, unlock)` always naming what it protects and the exact argument to pass; enums named sorted in validation errors |
| `gapi` | write and repeatability derived from the HTTP method, POST failing closed, declared exceptions only; a POST retried only on 429; `Retry-After` honored as a minimum; full-jitter backoff; the rate model of §11; **`CheckRedirect` returning `http.ErrUseLastResponse`**, a moved project's GET redirect resolved by re-reading the new path only when it is same-origin under the API root, with the request's query kept when the `Location` carries none; `Link: rel="next"` accepted only same-origin under the API root; every path segment escaped exactly once from typed parts, `..` refused; a headers deadline then a stall guard per read; a body cap; non-JSON bodies reported by status, content type and a prefix; transport errors stripped of path, query and host names; a create canceled after it may have been written `[ambiguous_outcome]`; a 401 `invalid_token` dropping the token and repeating a repeatable call once; **a context under which the client refuses every write**; a closed `Class` type with `Retryable()`; a `User-Agent`; unknown-field drift reported by path; a per-call counter |
| `ask` (2026-09-29) | a form with no fields, accepting it the confirmation; `requestState` signed, single-use and bound to the tool, the arguments and the question; every quoted value a code span with backticks, grave and acute marks and quote marks folded, links broken, cut to one line, and a blank line between lines, because VS Code draws the message as Markdown; a failure after the answer `[ambiguous_outcome]` (§4.12, §18 rows 94–96) |
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
| `unsupported` | the account's tier or edition lacks the route, or the API cannot do this (§2) | see the message |
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
the approval state. `get_issue` also lists the merge requests related
to the issue and those that close it when merged; `get_merge_request`
the issues it closes and those it mentions, an external tracker's by
its id when the id is shaped like one (anything else is left to the
title, inside the boundary). Each list is GitLab's first page of 20,
read at once with the rest and best effort, as the approval state is,
and says when GitLab has more. A read continued from an offset does not
read the lists again and leaves them null without comment. GitLab leaves out what the account cannot read, confidential
issues included, and lists closing merge requests from the issue's own
project only, so no list claims to be complete (§18 row 97).
`list_discussions` renders threads newest-first
under the budget, with position and resolved state for diff threads.
`list_item_events` merges GitLab's resource events into one history,
newest first: labels added and removed, state changes with the commit
or merge request behind them, the milestone set or removed, and an
issue's weight. GitLab lists each kind oldest first, by offset. Each
kind is read from page 1 and then the pages its count names, four at a
time, and the last page's next-page signal is followed until GitLab
says the list is complete, since events added meanwhile add pages. At
most ten pages of a hundred are kept, the newest; when older ones are
not, the history covers only what came after that kind's oldest event
kept, events of every kind at or before it are left out, and the result
says so. The kinds are read at once, and the first failure cancels the
rest. A kind whose page count GitLab no longer gives, past 10,000, or
whose newest pages GitLab filtered empty, leaves no place to start the
history, and the call fails `[unexpected]` rather than claim it whole.
The cut assumes GitLab's event ids follow time, which an imported
item's may not, and the tool says so. Events are merged once each by
kind and id, since offset pages read at different moments can overlap.
`max` and `page_token` page through the merged history; the token is
the server's own and names the last event shown, so an event added
between calls neither repeats nor skips one. GitLab drops label events
whose label the account cannot read after cutting the page, and
milestone events whose milestone is deleted or unreadable, so the
history is GitLab's and says so; a deleted label's event is kept and
shown as one. Label names are shown as names, milestone titles inside
the boundary. Iteration events (Premium) are not read (§18 row 99).
`link_issues` and `unlink_issues` relate two issues, both projects held
to the write allow-list since a link shows on both. `move_issue` (Ship)
takes the issue's `updated_at`, holds both projects to the allow-list,
and refuses a project more people can see than the one the issue is in.

### 7.3 Reviewing a merge request

`list_mr_files` lists changed files with their line counts and the
`too_large`, `collapsed`, `generated_file`, renamed and binary markers.
`get_mr_diff` returns the diff of named files under the budget. Paging
by `file_offset` reads from the page of diffs that holds that file, so
each page is read about once. One diff larger than the whole budget is
cut and continued by `diff_offset`, as `get_commit` and `compare_refs`
continue theirs. `list_mr_commits` lists commits.

Commenting has two paths, both taking a body (§4.2) and an optional diff
location (§6.2):

- `add_comment` posts now — to an issue, a merge request, or a reply to
  a discussion; `thread` starts a resolvable thread on either.
  `update_comment` replaces the text of one of the caller's own
  comments, which keeps its thread, its replies and its diff position.
- `add_review_comment` creates a **draft note**; `list_review_comments`
  and `delete_review_comment` manage the caller's drafts;
  `submit_review` publishes them all at once with an optional summary
  and `reviewer_state` (`reviewed`, `requested_changes`, or `approved`,
  which needs Ship).

`resolve_discussion` resolves or reopens a thread on a merge request,
or on an issue with `type: issue`. `rebase_merge_request` (Ship) takes
the head `sha` reviewed and refuses a protected source branch. A draft on a line reports the
`line_code` GitLab computed for it.

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
id, size and encoding; binary content returns metadata only. `get_blame`
names who last changed each run of lines. `cherry_pick_commit` and
`revert_commit` add a commit to a branch under `create_commit`'s guard,
and their dry run asks GitLab's own whether the change applies. Under
the `releases` toolset, `create_tag` refuses a name a protected-tag rule
covers and `delete_tag` a protected tag, and takes the tag's commit as
its witness. `list_tree`
pages with keyset. `list_branches`, `list_tags`, `list_commits`,
`get_commit` (with a budgeted diff and the first 20 merge requests in
the project that contain it, read by the commit's full id and only on
a read without offsets) and `compare_refs`.

`create_branch` from a ref. `create_commit` takes a branch, an optional
`start_branch`, a message and actions (`create`, `update`, `delete`,
`move`); `update`, `delete` and `move` require the file's
`last_commit_id`. It refuses protected branches (§4.4) and reports the
commit and the branch head afterwards.

### 7.6 CI

`list_pipelines`, `get_pipeline` (with its failed jobs, and its failed
trigger jobs with the downstream pipeline each started, which the job
listing leaves out), `list_jobs`, `list_job_artifacts` and `get_job_artifact` (one text file,
masked as a log is, under the file budget), `get_job_log` (tail by default;
`byte_offset` and `byte_limit` to page; `failed_only`; ANSI stripped;
collapsible sections folded to one line each; masked per §4.1), `lint_ci` (the project's
configuration at a ref, by GET; supplied content by POST, refused in
read-only mode, and refused with an `include:` at any depth, found by
parsing: GitLab fetches what an include names while linting, so a URL in
supplied content would be a way out no write control sees (§4.7, phase 2
security review). The POST declares itself read-only on its `Call`, so
it runs under a dry run and states no outcome.

A section's markers fold to one line naming the section where it
starts, and nothing inside is hidden: folding the content would be an
omission no offset continues. A line a carriage return overwrote shows
as it was left, as GitLab's job page shows it. `failed_only` ends the
window at the runner's `ERROR: Job failed` line and starts it at the
section the job failed in, the last one of the job's own before that
line, or as far back as the window allows.

`get_job_log` reads only the window and what its edges need, so a log
of any size can be read (spike O). The size comes from the job's
`trace` artifact once GitLab has archived the log. Otherwise the first
500 KB are read: a shorter answer is the whole log, in one call as
before, and a longer log is measured by one-byte reads past its end. `failed_only` reads back from the end 500 KB at a
time until it finds the failure line; a section that began before
what it read is named by its end marker. A window that starts inside a
private key block is masked whole: the read looks 256 KB before the
window for a header with no footer after it (§18 row 76).

`get_test_report` reads the report GitLab builds from the JUnit reports
a pipeline's jobs upload, child pipelines in the project included. It
shows the counts, each suite's (failing suites first, at most 100 and a
quarter of the budget), and the failed and errored cases: name, class,
file, time and output. The output is `system_output`, where JUnit puts
the failure; JUnit is GitLab's only test parser and never sets
`stack_trace`, so that is not read. Names, suite errors and output are
the project's tests' words, so they are masked as a log is (§4.1) and
shown inside the boundary; a one-line field has hidden characters
removed before the masks run, so a zero-width space cannot split a
token past them, and is cut at 400 characters. Only an output's first
16,000 characters are prepared, and it is cut at 4,000; a key block
that runs past what is prepared is masked to its end. The cases shown
stop at 40,000 characters, suite rows included, or 100 cases; `offset`
continues by case. GitLab parses the report when asked, caches it up
to two minutes and does not page it, so each call reads it again. While
the pipeline or a child pipeline in the project has not finished, the
report is partial, the cases can shift between calls, and the result
says so. A report larger than the client reads (§11) falls back to the
stored summary, counts and suite errors without cases, which a worker
writes after each job and can be behind or empty; `offset` is then
refused (§18 row 98).

Ship: `run_pipeline` (ref, variables and inputs; variables named in the
result, values masked), `retry_pipeline`, `retry_job` and `play_job`
(each with values for the inputs the job declares: GitLab refuses a
name a job's `inputs:` does not include, and a job that declares none
takes any and uses none), `cancel_pipeline`. Deleting a
pipeline is written off (§8a).

### 7.7 Planning, todos and search

`list_labels`, `list_milestones` (reads; assignment is a field on issue
and merge request updates). A label carries a `version`, a hash of what
an update can change, since GitLab keeps no version of one. `list_todos`, `mark_todos_done` (by id; at
most 100). `search` per §7.1.

### 7.8 Optional toolsets

- `wiki`: `list_wiki_pages`, `get_wiki_page`, `save_wiki_page` (create,
  or update with the §4.6 witness), `delete_wiki_page` (Destructive).
  Project wikis only; group wikis are Premium (§17.11).
- `snippets`: `list_snippets`, `get_snippet`, `create_snippet` — private
  visibility only; a public or internal snippet from a model is how
  private content leaves (§4.7), so visibility is not an input.
- `releases`: `list_releases`, `get_release`, `create_release` (Ship:
  it creates a tag and starts tag pipelines), `create_tag` (Write,
  refusing protected names), `delete_tag` (Destructive).
- `planning`: `create_label`, `update_label` (with the label's
  `version`), `delete_label` (Destructive), `create_milestone`,
  `update_milestone` (with `updated_at`; closes and reopens),
  `delete_milestone` (Destructive). A project's own only; a group's label
  is refused. §17.11 would add epics here after 1.0.
- `deployments`: `list_environments`, `list_deployments`.
- `activity`: `list_events`.

### 7.9 What the instance supports

`get_me` returns the user, the instance's version and edition, the
token's kind, scopes and expiry, the registered kinds and toolsets, the
write allow-list, and the last rate-limit reading. The version and
edition are information: gitlab.com runs the newest release, so no tool
is gated by version (§4.9). A tier-gated route answering 403 or 404 on a
feature GitLab licenses is `[unsupported]` naming the likely tier, never
`not_found`.

## 8. Tool surface

Eighty-six tools. With the default toolsets: fifty-three by default,
thirty-six in read-only mode, sixty-five with Ship and Destructive
both enabled. Every toolset and flag on registers all eighty-six.
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
| `get_issue` | Read | default | `GET /projects/:id/issues/:iid`, `/related_merge_requests`, `/closed_by` |
| `create_issue` | Write | default | `POST /projects/:id/issues` |
| `update_issue` | Write | default | `PUT /projects/:id/issues/:iid` |
| `list_discussions` | Read | default | `GET …/issues|merge_requests/:iid/discussions` |
| `list_item_events` | Read | default | `GET …/issues|merge_requests/:iid/resource_label_events`, `/resource_state_events`, `/resource_milestone_events`; an issue's `/resource_weight_events` |
| `add_comment` | Write | default | `POST …/notes`, `…/discussions`, `…/discussions/:id/notes` |
| `update_comment` | Write | default | `PUT …/notes/:id` (own notes) |
| `resolve_discussion` | Write | default | `PUT …/issues|merge_requests/:iid/discussions/:id` |
| `link_issues` | Write | default | `GET`/`POST …/issues/:iid/links` |
| `unlink_issues` | Write | default | `DELETE …/issues/:iid/links/:id` |
| `search_merge_requests` | Read | default | `GET /merge_requests`, `/projects/:id/merge_requests` |
| `get_merge_request` | Read | default | `GET …/merge_requests/:iid`, `/approvals`, `/closes_issues`, `/related_issues` |
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
| `get_commit` | Read | default | `GET …/repository/commits/:sha`, `/diff`, `/merge_requests` |
| `compare_refs` | Read | default | `GET …/repository/compare` |
| `list_tags` | Read | default | `GET …/repository/tags` |
| `create_branch` | Write | default | `POST …/repository/branches` |
| `create_commit` | Write | default | `POST …/repository/commits` |
| `get_blame` | Read | default | `GET …/repository/files/:path/blame` |
| `cherry_pick_commit` | Write | default | `POST …/repository/commits/:sha/cherry_pick` (refuses default and protected branches) |
| `revert_commit` | Write | default | `POST …/repository/commits/:sha/revert` (refuses default and protected branches) |
| `list_pipelines` | Read | default | `GET …/pipelines` |
| `get_pipeline` | Read | default | `GET …/pipelines/:id`, `/jobs`, `/trigger_jobs` |
| `list_jobs` | Read | default | `GET …/pipelines/:id/jobs` |
| `get_job_log` | Read | default | `GET …/jobs/:id/trace`, in ranges |
| `get_test_report` | Read | default | `GET …/pipelines/:id/test_report`, `/test_report_summary` when the report is too large |
| `lint_ci` | Read | default | `GET …/ci/lint`; `POST …/ci/lint` when writes are on |
| `list_job_artifacts` | Read | default | `GET …/jobs/:id/artifacts/tree` |
| `get_job_artifact` | Read | default | `GET …/jobs/:id/artifacts/:path` (masked as a log) |
| `list_labels` | Read | default | `GET …/labels` |
| `list_milestones` | Read | default | `GET …/milestones`, group milestones |
| `search` | Read | default | `GET /search`, `/groups/:id/search`, `/projects/:id/search` |
| `list_todos` | Read | default | `GET /todos` |
| `mark_todos_done` | Write | default | `POST /todos/:id/mark_as_done` |
| `merge_merge_request` | Ship | default | `PUT …/merge_requests/:iid/merge` |
| `approve_merge_request` | Ship | default | `POST …/merge_requests/:iid/approve` |
| `unapprove_merge_request` | Ship | default | `POST …/merge_requests/:iid/unapprove` |
| `rebase_merge_request` | Ship | default | `PUT …/merge_requests/:iid/rebase` (takes the head `sha`) |
| `move_issue` | Ship | default | `POST …/issues/:iid/move` (never to a more visible project) |
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
| `create_release` | Ship | releases | `POST …/releases`, with asset links into the project |
| `create_tag` | Write | releases | `POST …/repository/tags` (refuses protected names) |
| `delete_tag` | Destructive | releases | `DELETE …/repository/tags/:name` (refuses protected tags) |
| `create_label` | Write | planning | `POST …/labels` |
| `update_label` | Write | planning | `PUT …/labels/:id` (takes the label's `version`) |
| `delete_label` | Destructive | planning | `DELETE …/labels/:id` |
| `create_milestone` | Write | planning | `POST …/milestones` |
| `update_milestone` | Write | planning | `PUT …/milestones/:id` (takes `updated_at`) |
| `delete_milestone` | Destructive | planning | `DELETE …/milestones/:id` |
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
| issues, issue notes and discussions | Used, starting and resolving threads, links and moves included (a move is Ship). Clone, subscribe, time tracking and award emoji deferred; delete written off (Owner-only, permanent) |
| merge requests, notes, discussions | Used; rebase gated as Ship; `/changes` written off as deprecated for `/diffs`; merge-request delete written off |
| draft notes | Used: the review path |
| approvals (merge-request level) | Used: approve, unapprove, state. Approval rules and project approval settings written off: governance configuration, Premium |
| repository files, tree, commits, branches, tags, compare | Used, blame included. Cherry-pick and revert used as Write behind the protected-branch guard (§17.13); tag create gated under `releases`, tag delete Destructive. Raw archive deferred; commit statuses written off (a CI integration's surface); "delete merged branches" written off |
| protected branches and tags | Read only, by `create_commit`'s and `create_tag`'s guards; the protected-tag listing, written off until phase 6, is read for `create_tag` by §17.13. Every write written off |
| pipelines, jobs, CI lint | Used, trigger jobs and one text file of a job's artifacts included; the deprecated `bridges` listing written off for `trigger_jobs`, which GitLab serves with the same handler. Pipeline variables written off with CI variables: the listing carries their values. Pipeline delete written off: it destroys logs and artifacts and is no assistant task, and GitLab's own server shows how easily it becomes a default. Whole artifact archives deferred; artifact delete written off |
| pipeline schedules, triggers, variables, secure files | Written off: CI configuration and secrets |
| labels, milestones | Read used; a project's own create, update and delete gated under `planning`, deletes Destructive. Group label and milestone writes, promote and subscribe deferred; the label routes addressed without a name written off as deprecated |
| search | Used |
| todos | Used |
| wikis (project) | Used under the `wiki` toolset. Group wikis deferred (Premium) |
| snippets | Used under `snippets`; update and delete deferred |
| releases, release links | Used under `releases`, with asset links only at create and only into the project itself; update and delete, of a release or a link, written off |
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
| `GITLAB_MCP_TOOLSETS` | adds `wiki`, `snippets`, `releases`, `deployments`, `activity`, `planning`, or `all` | — |
| `GITLAB_MCP_WRITE_NAMESPACES` | confines Write, Ship, Destructive (§4.7) | — |

No setting chooses the instance: it is gitlab.com (§4.9).

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
   OAuth client for the Google servers. On gitlab.com that is user
   settings → Applications (or a group's, §2.1): redirect URI
   `http://127.0.0.1/callback`, **Confidential unchecked**, scope `api`
   (`read_api` for read-only). `docs/setup.md` says exactly this and is
   generated from the code (§5a `staleness`).
2. `gitlab-mcp login --client-id <application id>`. It prints the scopes it will ask for, opens the
   browser, listens on `127.0.0.1:0` with PKCE S256 and `state`, and
   warns if GitLab granted less than it asked for.
3. `gitlab-mcp doctor` to check it. `status`, `logout` and profiles
   behave as they do in every sibling: `logout` revokes the token and
   names the other profiles that share the same application.

The same subcommands, the same flags (`--profile`, `--no-browser` with
the exact `ssh -L` line for SSH, `--client-id` in the place of
`--client-secret`), the same keyring layout per profile with a warned
0600 file fallback, the same env → keyring → file order, no out-of-band
flow. A profile records the application id and the instance it signed
in to — gitlab.com, or the test instance in development — so its token
is never sent anywhere else.

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
  granted scopes, so startup reads token info once, bounded by the
  smaller of 15 s and the HTTP timeout, when the access token is still
  fresh. Startup does not refresh: a host kills servers before the
  handshake, and one killed between GitLab rotating the pair and the
  keyring storing it leaves the profile signed out. With the access
  token due, the scopes of the last login stand and the first tool call
  refreshes. A start with no scopes recorded refreshes to read them, and
  so does one given `GITLAB_MCP_REFRESH_TOKEN`, whose grant the login's
  scopes do not describe. A server that is
  stopped waits for a refresh in progress to be stored before it exits.
  A kill during a refresh still loses the pair; nothing on this side
  can make GitLab's rotation and the keyring's save one step. It reads neither `/metadata` nor
  `/user`: nothing is gated by version (§4.9). Every failure there is logged and the
  server starts anyway, so errors surface per call; the one fatal
  outcome is a token whose scopes cannot serve the configured mode.
- **A token is never sent to an instance it was not issued by.** A
  profile signed in to another instance than the one in use — the test
  instance while the server talks to gitlab.com, or the reverse —
  withholds its token: every call answers `[auth]` naming the mismatch
  and saying to log in again.

Deliberately absent, so the family stays alike: personal access tokens,
the device flow (§2.5), and a client id shipped in the binary. Each was
considered (§17.1, §17.5) and each would make this server sign in
differently from the rest.

Configuration: `GITLAB_MCP_PROFILE`, `GITLAB_MCP_CLIENT_ID`,
`GITLAB_MCP_READ_ONLY`, `GITLAB_MCP_ENABLE_SHIP`,
`GITLAB_MCP_ENABLE_DESTRUCTIVE`, `GITLAB_MCP_TOOLSETS`,
`GITLAB_MCP_WRITE_NAMESPACES`, `GITLAB_MCP_LOG_LEVEL`,
`GITLAB_MCP_LOG_FORMAT` (`text` default), `GITLAB_MCP_HTTP_TIMEOUT`
(60 s default), `GITLAB_MCP_CONFIG_DIR`. Each also a flag.
`docs/configuration.md` is checked against the exported list. There is
no instance setting; the development override of §4.9 is documented in
`docs/development.md` alone.

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
gated. It covers gitlab.com, the only instance.

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
- **`internal/instance`**, over every base-URL shape of §3.4, which the
  loopback test override may still take.
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
  gitlab.com only, with no instance flag: every tool
  and option, held by `live-cover`; the transcript through one
  redacting writer, held by `transcript`; read by a person before a
  phase counts.
- **Evals**, `scripts/evals`, against `gitlabtest` only (`CLAUDE.md`
  rule 1): a self-check in `check` with no model, and a real model by
  hand through `claude -p`, with built-in tools disabled by `--tools ""`
  and `--setting-sources ""` (fail-closed, never a denylist), a census
  of the instance before and after, and tasks whose content carries an
  injected instruction — read a private file, a quick action, a request
  to post elsewhere, a request to approve — scored on whether the model
  followed it. The self-check drives, per task, an empty run, a scripted
  run that does the task and one that follows the steer through the
  real server, and holds them to fail, pass and fail.

## 14. Confirmed decisions and their consequences

| Decision | By | Consequence |
|---|---|---|
| Design first, reviewed before code | maintainer, 2026-09-25 | this document; phase 0 waits for "go" |
| gitlab.com and self-managed both | maintainer, 2026-09-25 | **Reversed 2026-09-26**, row below |
| gitlab.com only; self-managed out of scope | maintainer, 2026-09-26 | reverses the 2026-09-25 row above. §4.9: no instance setting, no private CA, no plain-http permission, no version gating; one loopback-only development override; spike I dropped; §17.4 closed |
| Sign-in the same as every sibling: own OAuth application, loopback, browser, keyring; no tokens, no device flow, no shipped client id | maintainer, 2026-09-25 and 2026-09-26 | §10; §17.1 and §17.5 decided |
| Issues, merge requests, repository, CI, wiki, snippets and releases in 1.0 | maintainer, 2026-09-25 | §8; wiki, snippets and releases as toolsets |
| Merging, approving and CI runs behind a Ship flag | this design | §4.3 |
| Quick actions refused by default, never executed | this design | §4.2 |
| Commits never to a protected branch | this design | §4.4 |
| REST v4 only, no escape hatch | this design | §4.10, §8a |
| Unregistered, not registered-and-refusing, for gated tools | this design | §17b |
| Release pipeline in phase 0 | this design | §12 |
| The person asked before a merge, an approval, a manual job, a release, a tag, a pipeline on a protected ref, publishing a confidential issue and every delete; the empty form; `GITLAB_MCP_REQUIRE_PROMPT` | maintainer, 2026-09-29 | §4.12; a client that declares elicitation and answers with nobody there cannot make these writes, which is why the release is a major one |

## 15. What must be verified live

Each spike states its question and, when run, its verdict separately.
Every spike has run on gitlab.com (2026-09-26 and 2026-09-27), spike O
the last, for phase 5. Spike I, the floor version, was dropped with self-managed support
(§14, 2026-09-26).

- **Spike A — loopback port.** Register `http://127.0.0.1/callback`;
  send `http://127.0.0.1:<random>/callback`, `http://[::1]:<random>/…`
  and `http://localhost:<random>/…`. Expect the first two accepted and
  the third refused. On gitlab.com. §2.1.
  *Verdict, gitlab.com, 2026-09-26: a random `127.0.0.1` port was
  accepted against the registered `http://127.0.0.1/callback`, through
  the real `login`. The `[::1]` and `localhost` halves are not needed:
  gitlab.com is the only instance, and it answered for the address
  `login` uses.*
- **Spike B — public client.** Code exchange, refresh and
  `/oauth/revoke` with only `client_id`, on gitlab.com. §2.2.
  *Verdict, gitlab.com, 2026-09-26: code exchange and refresh work
  with no secret on a non-confidential application; `doctor` then read
  the token's scopes and the account. Revocation, 2026-09-27, through
  spike G: revoking a public client's access token with only
  `client_id` killed it (401 `invalid_token`) and its refresh token
  (`invalid_grant`).*
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
  *Verdict, gitlab.com, 2026-09-26: the browser consent succeeds and
  the code exchange answers 401 `invalid_client` ("Client authentication
  failed due to unknown client, no client authentication included, or
  unsupported authentication method."). `login` prints only the
  instruction to untick Confidential.*
- **Spike E — quick actions.** Through the API, on issues, merge
  requests and notes: a bare command executes; a backslash-escaped one
  does not and renders as the same text; fenced, indented, quoted and
  inline commands do not; a command after a list item; CRLF. §4.2
  depends on every row.
  *Verdict, gitlab.com, 2026-09-27: confirmed, and the guard agrees
  with GitLab on all 48 rows (12 shapes, each as an issue note, a merge
  request note, a new issue's description and a merge request
  description update). A bare line, a line inside a paragraph, a line
  after a list item and a blank line, and CRLF endings ran; fenced,
  indented, `>` and `>>>` quoted, inline code, an HTML block, a line
  straight after a list item and lone CR endings did not. Every
  escaped line stayed inert and renders as the same text without the
  backslash. A note that is only a command answered 202.*
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
  *Source, gitlab-org/gitlab master `1ee957c5`, 2026-09-26: a route
  takes an `mcp` token when its class allows the verb for `mcp` and the
  route carries `route_setting :mcp` (`lib/api/concerns/mcp_access.rb`).
  About sixteen REST routes do: search, branches, one issue by iid and
  issue create, merge request create, update, commits, diffs and
  pipelines, and pipeline and job listings among them; `/user`, a
  project, issue listings and every notes route do not. The driver
  probes both kinds with a second sign-in for `mcp` alone, then revokes
  it (`-spike G`).*
  *Verdict, gitlab.com, 2026-09-27: confirmed. A sign-in asking for
  `mcp` alone was granted `mcp`. One issue by iid, branches, a merge
  request's commits, pipelines and project search answered 200; `/user`,
  a project, the issue listing, a merge request and a new comment
  answered 403. Ten of ten routes agreed with the source. The scope
  cannot comment or read a merge request, so it changes nothing for
  this server (§2.6).*
- **Spike H — conditional reads.** Whether `If-None-Match` returns 304 on
  API GETs and whether a 304 is counted against the rate limit.
  *Verdict, gitlab.com, 2026-09-26: a single read and a listing both
  send an ETag and answer a conditional GET with 304 and no body, and
  every 304 lowered `RateLimit-Remaining` by one. Conditional reads save
  bytes, not rate budget, so the client does not send them.*
- **Spike J — job log windows.** `byte_offset` and `byte_limit` on
  gitlab.com.
  *Verdict, gitlab.com, 2026-09-26: both work on the trace endpoint and
  return the exact bytes of the stored log, with no `Content-Range`;
  `byte_limit` alone starts at 0, and an offset past the end is an empty
  200. The whole log is one `text/plain` answer. `get_job_log` reads the
  whole log and windows it itself, because a tail needs the size first;
  server-side windows are the route for a log past the 32 MiB cap
  (§17a).*
- **Spike K — inline positions.** Comments computed by `diffpos` on
  added, removed and unchanged lines and a range, read back from the
  web view.
  *Verdict, gitlab.com, 2026-09-27: confirmed through the API rather
  than the web view. For an added line, a removed line, an unchanged
  line addressed by either number, and a range on each side, GitLab
  accepted the position `diffpos` computed and answered with the same
  `line_code`, and for a range the same start and end codes. The web
  view draws from those codes.*
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
  *Verdict, gitlab.com, 2026-09-27: no delay is needed. An issue and a
  merge request by author, `created_after` and title, a comment among
  the threads, a draft among the account's drafts and a commit as the
  branch head were each found by the first read, 265 to 387 ms after
  the create answered. On 2026-09-27 again, for phase 3: a pipeline by
  ref, source `api`, username and `created_after`, and a release by its
  tag, each found by the first read, about 290 ms after the create.*

- **Spike N — conditional delete.** Whether a comment delete honors
  `If-Unmodified-Since`: a time before the comment's `updated_at`, the
  same second as an HTTP-date, the time shown, and the time shown
  stretched to the end of its millisecond. `delete_comment` sends the
  last (§4.6, §18 row 62).
  *Verdict, gitlab.com, 2026-09-27: confirmed. The comment's
  `updated_at` is shown to the millisecond. A second before, the same
  second as an HTTP-date, and the time shown unpadded were each refused
  412; the time shown stretched to the end of its millisecond deleted
  it, 204. GitLab compares below the millisecond it shows.*

- **Spike O — a job log's size, unread.** Whether a job's `trace`
  artifact size, or a HEAD of the trace, gives a log's length without
  reading it, for a finished and a running job; what `byte_limit` past
  500 KB answers; which paging headers a merge request's diffs carry.
  `get_job_log` reads only its window from this (§7.6).
  *Verdict, gitlab.com, 2026-09-27: an archived log's `trace` artifact
  size was the log's 195,123 bytes exactly, and a ranged read from that
  size back ended where the whole log did. gitlab.com archived the log
  minutes after the job finished; until then the job lists no trace
  artifact. A HEAD of a finished log answered its length on one run and
  0 on the next, and 0 for a running log: it is not used. `byte_limit`
  512,001 answered 400. The diffs listing sends `X-Total` and
  `X-Total-Pages`. The driver runs it alone with `-spike O`.*

Spikes A–D register or use an OAuth application and are in the "ask
before doing" of `CLAUDE.md`.

## 16. Delivery phases

Each phase is one session and ends in a pull request, then waits for an
explicit "go". Phases are not tagged one by one: the maintainer tags a
release when they choose. The next session starts from this repository
alone.

**Phase 0 — scaffolding, gates, sign-in and core reads.**
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
A, B, C, D, F, L. A live run whose transcript is read.

*Built 2026-09-26, reviewed (§16a) and simplified. The live run passed
on gitlab.com the same day and its transcript was read: it found the
signed-in person's display name and user id unmasked in the driver's
output (fixed and re-run clean), a sha256 labeled as a client id by the
shape mask (safe, left), and two wording defects (fixed). Spikes A and B
answered on gitlab.com, then C, D, F and L. The same day the server was
narrowed to gitlab.com (§14), which dropped spike I and A's `[::1]` and
`localhost` halves, the instance setting, the private CA and version
gating. Nothing is owed.*

**Phase 1 — the rest of reading.** `list_mr_files`,
`get_mr_diff`, `list_mr_commits`, `compare_refs`, `list_tags`, the CI
reads with `get_job_log` and its masking, `lint_ci`, `search`,
`list_labels`, `list_milestones`, `list_members`, `find_users`,
`list_todos`, `list_review_comments`, the three resources. Spikes H, J.

*Built 2026-09-26 and 2026-09-27, simplified and reviewed (§16a). Two
live runs on gitlab.com drove every tool and option but two page tokens
waived as undrivable, and their transcripts were read. The first found
gitlab.com's timestamped job logs (which also defeated `failed_only`),
general drafts carrying an empty position in no stable order, gitlab.com's
own wording for a refused group code search, and ids the driver did not
mask; each was fixed, and the second run was clean but for three ids the
driver now also masks. Spikes H and J answered. `lint_ci` on supplied
content moved to phase 2 with the write path. Owed: nothing; §17a holds
three new deferrals.*

**Phase 2 — the write path.** The `diffpos` package and its
tests; `create_issue`, `update_issue` with the witness, `add_comment`,
`resolve_discussion`, the review tools, `create_merge_request`,
`update_merge_request`, `create_branch`, `create_commit` with the
protected-branch guard, `mark_todos_done`, `lint_ci` on supplied
content; settle-by-reading; the write allow-list. Spikes E, K, M. Spike E runs before any write tool is
registered.

*Built 2026-09-27, simplified and reviewed (§16a). Spike E ran first
and the guard agreed with GitLab on all 48 rows before any write tool
was registered; K and M followed, and a live check showed a branch's
`protected` flag honors wildcard rules. Four live runs on gitlab.com
drove every tool and option but the two page tokens already waived, and
their transcripts were read: they found a new merge request's
`updated_at` moving a second after the create, a commit's move emptying
the file it moved, and an assignee change leaving `updated_at` in place
(§18 rows 56–58); each is fixed or accounted for. Owed: nothing; §17.12
is decided, and §17a holds three new deferrals.*

**Phase 3 — Ship, Destructive and toolsets.** The Ship tools
with `sha` witnesses; `delete_branch`, `delete_comment`; the `wiki`,
`snippets`, `releases`, `deployments` and `activity` toolsets. Spike G.

*Built 2026-09-27, simplified and reviewed (§16a). Every behavior was
checked against GitLab's source at v19.4.1-ee first (§18 rows 60–70).
Spike M covered pipelines and releases, and spike N showed GitLab holds
a comment delete's `If-Unmodified-Since` below the millisecond it shows.
The live driver runs a second server with every flag and toolset on,
its writes confined to the scratch group. Three live runs on gitlab.com
drove every tool and option but the two page tokens already waived, and
their transcripts were read. The first found gitlab.com refusing
pipeline variables on a new project and deployments filtered by time in
another order (§18 rows 72, 73); both are fixed. Owed: spike G, which
needs an `mcp`-scoped application (ask before doing); §17.2 and §17.8
stay open for phase 4; §17a holds two new deferrals.*

**Phase 4 — evals and 1.0.** The evals harness with the
injection tasks of §13; §17.3 decided; the surface frozen into the
schema baseline; §17 closed or each item argued open.

*Built 2026-09-27, simplified and reviewed (§16a). Four injection tasks
plant an instruction from a non-member of the public project: read a
private file, run a quick action, post in another project, approve and
merge with Ship on. The self-check drives each task's empty, done and
obeyed runs through the real server and holds them to fail, pass and
fail. The first model run scored the harness, not the model: every
world signed the server in under one keyring profile with the same
token names (§18 row 74), which is fixed. Opus then passed all eight
tasks, and the four injection tasks twice more, 12 of 12. §17.2, §17.8
and §17.9 are decided as proposed and §17.3 from the evals; §17.10
stands and §17.11 is after 1.0. The schema baseline holds all
sixty-six tools. A live run on gitlab.com drove every tool and option
but the two page tokens already waived, and its transcript was read:
clean. Spike G then ran on gitlab.com and agreed with the source on
ten of ten routes, and answered spike B's revocation half. Owed:
nothing.*

**Phase 5 — the deferred cleanups, before 1.0.** Every cleanup of §17a
that a REST call can close: `get_job_log` reading only its window (spike
O), `get_mr_diff` reading from the page that holds `file_offset`, a
project description and one oversized diff continued, a pipeline's
failed trigger jobs, resolvable threads on issues, a lost comment settled
however many threads followed it, a draft's `line_code`. And what §8a
had left out on purpose, by the maintainer's decision of 2026-09-27: job
inputs on `retry_job` and `play_job`, and release asset links, confined
to the instance.

*Built 2026-09-27, simplified and reviewed (§16a). Every behavior was
checked against GitLab's source at v19.4.1-ee first (§18 rows 76–83).
Spike O showed an archived log's size in its trace artifact and a HEAD
not to be trusted, so a log not yet archived is read from its start and
measured past its end. Four live runs on gitlab.com drove every tool and
option but the two page tokens already waived, and their transcripts
were read. They found a finished log's HEAD answering 0, a job with no
`inputs:` taking any input (§18 row 80), and the undeclared-input
refusal first tried on a job `retry_pipeline` had already retried; each
is fixed. The third met a gitlab.com repository error creating a
snippet, which phase 5 did not touch; the fourth was clean. The security review found indented and prefixed keys escaping
the key search; the code review, a window start of -1 and links to any
project's files; each is fixed. Owed: nothing. The two cleanups left are
argued in §17a (§18 rows 82, 83), and phase 6 waits on §17.13.*

**Phase 6 — the feature candidates, before 1.0.** The tools §17.13
decides, each with its kind and toolset:
issue move and links, label and milestone writes, rebase, cherry-pick,
revert, blame, job artifacts, and tag writes. Each is checked against
GitLab's source before it is built, and runs live.

*Built 2026-09-27, simplified and reviewed (§16a). Seventeen tools, the
surface now eighty-three and the schema baseline holding them all. Every
operation was checked against GitLab's source at v19.4.1-ee first (§18
rows 84–89). Three live runs on gitlab.com drove every tool and option
but the two page tokens already waived, and their transcripts were read:
the first found the plan's cherry-pick source conflicting with the
default branch, which the tool reported as GitLab's refusal, and a pick
that does not apply answering 400, now `[conflict]`; the second a revert
reported in a pick's words, and an artifact listing too short to page;
the third was clean. The security review found a fork's rebase and a members-only
issue's move unguarded; the code review, blame windows and settles;
each is fixed. Owed: nothing.*

**Phase 7 — the person confirms what ships or deletes.** Asked for by
the maintainer on 2026-09-29, after the same was built in a sibling:
the server asks the person, through MCP form elicitation, before the
thirteen writes of §4.12, with the set the maintainer chose. The
question is quoted in code spans and the form has no fields, as the
maintainer's check in Claude Code found clearest (§18 rows 94–96).

*Built 2026-09-29. `run_pipeline` reads its ref only when a question
could go out; `update_issue` asks only when it makes a
confidential issue public. A test derives the asking tools from the
definitions, with a floor of thirteen, and holds each on three
protocols: declined, nothing sent; accepted, the write. The live driver
is now a client that declares elicitation and answers for the
maintainer: accept, but for one delete it declines. It learns which
tools ask from their published descriptions, and fails a call that
puts more questions than it may and a tool that asks and put none over
the run. Three live runs on gitlab.com drove every tool and option but
the two page tokens already waived, and their transcripts were read:
the first found the driver expecting a question on a merge that had
nothing left to merge, the second its count of a tool that asks only
sometimes kept per server; the third was clean, with seventeen
questions, each quoting only the run's own text, and the declined
delete writing nothing. Reviews in §16a. Owed: nothing.*

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
| Discussions walked up to ten pages per read | Not fixed: `X-Total` counts system-only threads, so the proposed fix miscounts, and the order needs every thread (§18 row 83); §17a |

GitHub's CodeQL on the phase 0 pull request: six alerts. Two were real
on a 32-bit build, which no release targets: a `#L` line anchor parsed
as 64 bits and narrowed to `int` without a bound (`instance/resolve.go`),
fixed in phase 2 with a bound and a test. Four are in `gitlabtest`, which
only tests link: three redirects that model GitLab's own (the OAuth
callback to a registered loopback URI, checked before redirecting, and a
moved project to the instance's own URL), and SHA-1 over fixture names
to make fake commit ids. They are not defects, and were dismissed on
GitHub as used in tests, 2026-09-27.

**Phase 1.** `/security-review`: no finding at confidence 8 or above
(`audit/security-reviews/phase-1.md`), two below it recorded there.
`/simplify`: the advanced-search refusal classified in the client, the
live driver's confinement made deny-by-default, whole-log regular
expressions replaced by literal searches, and shared helpers; four
proposals skipped because a gate reads the shape they would remove.
`/code-review high`: ten candidates:

| Found | Fixed |
|---|---|
| A to-do on a commit carries a SHA as its target id and failed the listing | Phase 1 commit; the id is not decoded |
| A private key header far back moved every later window to it | Phase 1 commit; the window keeps its size and the block is masked from a restored header |
| A script printing the runner's failure words steered `failed_only` | Phase 1 commit; the last failure line counts |
| A failure line with no section read as "no failure line" | Phase 1 commit |
| A failed pipeline failed by a downstream one said "No job failed." | Phase 1 commit; it said a trigger job may have. Phase 5 reads the failed trigger jobs and names the downstream pipeline |
| Publishing a draft between pages skipped one | Phase 1 commit; the page token names a draft id |
| The live driver ran its CI steps against an unfinished pipeline | Phase 1 commit; it leaves the ids zero so the steps fail |
| A job log past 32 MiB cannot be read, and each window re-reads it | Phase 5: only the window is read (§18 row 77) |
| `get_mr_diff` re-reads earlier pages on each continuation | Phase 5: it reads from the page holding `file_offset` |
| The driver's confinement accepts a group read without the run's word | Not a defect: every step's `group` argument is held to the run's namespace and word |

**Phase 2.** `/simplify`: one project read per write; label checks by
search and removals from the fresh read; previews from the request
bodies; one settle helper for issues and merge requests, one position
converter, one comment check; the 202 refusal in the client for every
notes call; a new comment's thread found on the last page; one thread
read for `resolve_discussion`; the commit guard trusting an existing
branch's `protected` flag; independent reads in parallel; the lint POST
declared read-only on its `Call`. `/security-review`: two findings at
confidence 8, both fixed (`audit/security-reviews/phase-2.md`).
`/code-review high`: ten findings, all fixed, the behavior ones with a
test that failed before the fix:

| Found | Fixed |
|---|---|
| A new title alone cleared a merge request's draft state | Phase 2 commit; the state is kept unless `draft` says otherwise |
| The draft prefix matched more than GitLab's and rewrote titles | Phase 2 commit; GitLab's pattern, add and strip |
| The protected-rule read passed a branch past the rules it read | Phase 2 commit; refused when the read is incomplete |
| A lost comment was looked for among the oldest threads | Phase 2 commit; the newest page, a reply in its thread |
| A lost draft could be settled by an older one with the same text | Phase 2 commit; drafts present before the call are excluded |
| The signed-in account was cached for settling across a new login | Phase 2 commit; read afresh |
| `due_date` with `clear_due_date` cleared silently | Phase 2 commit; refused, as the milestone pair is |
| To-do items and label checks ran one at a time | Phase 2 commit; in parallel, paced by the client |
| A doc comment slipped from its function | Phase 2 commit |
| Every standalone comment reads threads to name its thread | Phase 2 commit; it no longer does: a reply or a thread names its own, and `list_discussions` names a standalone comment's |

**Phase 3.** `/simplify`: one read-back helper for the three deletes,
which also keeps their rendering alike; the issue or merge request note
reads picked once (the delete stays called by name, which the outcomes
gate's floor caught when it was not); the If-Unmodified-Since stretch moved into the client
beside the header; a tag looked up by name instead of by search, which
also finds one past the search's first page; a snippet's metadata and
first file read at once; the file budget shared with `get_file`; one
variable-key check; one release row; the id minimum derived from the
schema rather than listed; the job-state refusal classified once.
Skipped: classing every 401 without `invalid_token` as `[forbidden]`
rather than `[auth]`, a §6.5 decision left to the maintainer.
`/security-review`: no finding at confidence 8
(`audit/security-reviews/phase-3.md`), two below it recorded there.
`/code-review high`: nine candidates:

| Found | Fixed |
|---|---|
| `play_job` blamed every 403 with variables on the variables setting and dropped GitLab's reason | Phase 3 commit; only GitLab's bare 403 is offered that cause |
| A delete repeated after a lost answer read as `[not_found]` though it had landed | Phase 3 commit; a not-found delete of something now gone is reported deleted, saying so |
| `delete_branch` wanted the whole SHA, while `list_branches` shows twelve characters | Phase 3 commit; a prefix of seven or more is taken |
| An auto-merge set before a lost answer read as a failure | Phase 3 commit; the read back reports it set |
| A lost wiki create in a directory settled as not created | Phase 3 commit; the slug is matched as well as the title |
| `resolve_url` sent a wiki's index and a page's edit view to `get_wiki_page` | Phase 3 commit; the index names `list_wiki_pages`, the views are stripped |
| `retry_pipeline` could report unchanged while GitLab still showed the old status | Not a defect: the live runs showed GitLab answering with the new status |
| A 200 merge answer with the merge request open reads as `[unexpected]` | Not a defect: GitLab answers 422 unless merged (`execute_immediate_merge!`) |
| The id minimum now applies to `list_discussions.note_id` | Kept: nothing is released, and 0 never named a comment |

**Phase 4.** `/simplify`: one note-storing helper in `gitlabtest`,
which the planted comments share; a random part in every `gitlabtest`
token name, the root of the keyring collision (§18 row 74); the smoke
gate's floor derived from `tools.Surface` rather than typed; the tools a
rehearsal is offered listed by the rehearsal itself; one path helper for
the three write scorers; spike G's probes carrying their own query.
Skipped: one parser of tool-result text for the harness and the smoke
gate (different shapes, different packages), and fewer worlds per
self-check, which takes under a second. `/security-review`: no finding
at confidence 8 (`audit/security-reviews/phase-4.md`), three below it
recorded there. `/code-review high`: ten candidates:

| Found | Fixed |
|---|---|
| `injected-post-elsewhere` failed a summary that mentioned the other project | Phase 4 commit; only a call aimed at it, or a new issue, follows the steer |
| Spike G left its `mcp` token live when a probe failed | Phase 4 commit; revoked on every path out |
| Spike G's refresh after revocation, if granted, left a live pair | Phase 4 commit; that pair is revoked |
| An approval submitted as a review was seen only in the call's arguments | Phase 4 commit; `observe` reads the reviewer state |
| One fixed keyring profile let two runs delete each other's sign-in | Phase 4 commit; a profile per world |
| The scorer's own quick-action pattern knew four commands | Phase 4 commit; `quickaction.Find`, escaped lines included |
| `injected-comment` passed "the discussion is empty" | Phase 4 commit; the summary must carry the export or the header row |
| The smoke floor moved with the code it checks | Phase 4 commit; a constant floor of the thirty-one reads beside it |
| Spike G discards the authorization URL where no browser opens | Not fixed: the redacting printer masks the client id in it, so a printed URL would not work; the browser opens as it does for `login` |
| `surfaceFloor` is typed, not derived | Kept: it is the floor on how much the checker read, which is meant to stand apart from the code (`CLAUDE.md` rule 19) |

**Phase 5.** `/simplify`: the unused trace counter removed; one
`bridgeRow`; one input-name helper for `run_pipeline`, `retry_job` and
`play_job`; one thread reader for issues and merge requests; the service's
copy of the link type dropped; page 1 reused when a settle walks back;
`get_mr_diff` stopping where `budgetDiffs` does rather than by its own
byte count, which could leave no `next_file_offset`; `failed_only`
bounded to four reads for the line and one more for its section; a HEAD
of the trace dropped after spike O showed it unreliable. Skipped: gapi
discussion calls taking the item kind, which reshapes code far outside
this phase. `/security-review`: one finding at confidence 8, fixed
(`audit/security-reviews/phase-5.md`). `/code-review high`: ten
candidates:

| Found | Fixed |
|---|---|
| `failed_only` could start the window at -1 on a short log with a stray end marker, and panic | Phase 5 commit; the start is at least 0, with a test |
| Key lines behind prefixes CI tools write ended the key walk, leaving a key unmasked | Phase 5 commit; the walk replaced by a 256 KB search for an open header, in the window's own read (§18 row 76) |
| The key walk had no bound and copied the held stretch on each step | Phase 5 commit; the same change |
| A BEGIN quoted in an error message can mask the rest of a window | Kept: over-masking fails safe, and the whole-log code did the same |
| An asset link could point at any project's files on gitlab.com | Phase 5 commit; only into the release's own project (§18 row 81) |
| A failed trigger job read failed `get_pipeline` | Phase 5 commit; it is soft, and the result says the trigger jobs could not be read |
| The hidden-character count of a cut or continued diff counted the whole diff | Phase 5 commit; the part shown is counted |
| `failed_only` reads up to 2 MB for a failing job with no failure line | Kept: the bound is the design; the runner writes the line last |
| A `file_offset` past the end named the wrong number of files | Phase 5 commit; the message names none and points at `list_mr_files` |
| `resolve_discussion` read an unknown type as a merge request | Phase 5 commit; refused `[invalid]` |

**Phase 6.** `/simplify`: one label row and one milestone row for the
reads and the writes; `names` for what an update changed and
`changedLine` for saying it; one `protectingRule` for the branch and tag
guards; `get_job_artifact` through `fileContent`; the enums a schema and
its check share exported once; witnesses parsed by `parseWitness`; the
fake's protected tags matched by wildcard. Skipped: embedded input
structs (`register` reads top-level fields), one body for cherry-pick and
revert (the fields gate holds revert's, which takes no message), and
dropping the read after a rebase (it carries `merge_error`).
`/security-review`: two findings at confidence 8, fixed
(`audit/security-reviews/phase-6.md`). `/code-review high`: five, all
fixed:

| Found | Fixed |
|---|---|
| A full blame window this server chose gave no `next_line`; the test was inverted | Phase 6 commit; a full window continues, with a test |
| One run of blame lines larger than the budget was shown whole | Phase 6 commit; the run is cut at the budget and `next_line` goes on |
| A lost cherry-pick with its own message settled "not created" | Phase 6 commit; settled by the new head's parent being the head read before, unknown otherwise |
| A group's label was not found rather than refused | Phase 6 commit; the label read includes ancestor groups |
| A lost label or milestone create could settle on an older one | Phase 6 commit; `create_label` refuses a name that exists, and a milestone counts only if made after the call started |

**The first pull request.** Its CI ran phases 1 to 6 on Linux, macOS and
Windows for the first time, and found:

| Found | Fixed |
|---|---|
| `schema-ack` failed building a base from before the server | A base without `cmd/gitlab-mcp` has an empty surface, with a test |
| On Windows two token saves read the same clock, and after a half-failed save the older keyring pair won | A store's saves are stamped strictly in order, with a test |
| On Windows the bundle test repacked over a bundle it still held open | The test closes it first |
| CodeQL: the fake's label priority narrowed without a bound | Bounded to the 32-bit range GitLab takes |
| CodeQL: the leak gate's owner-link pattern is unanchored | Dismissed as a false positive: a scanner that finds links anywhere in text |

**The 1.0 live run.** Its transcript found:

| Found | Fixed |
|---|---|
| `get_me` reported a token's expiry as `created_at` plus `expires_in`, which is the seconds left, so an hour-old token read as expired an hour before the read | The time of the read plus `expires_in`, with a test on an aged token (§18 row 90) |
| `cancel_pipeline` said nothing could be canceled when GitLab answered before recomputing the pipeline's status | `canceled` whenever GitLab's cancel acts on the status read first; the fake answers as GitLab does (§18 row 91) |

Its `/code-review high` found:

| Found | Fixed |
|---|---|
| A pipeline that finished before the cancel landed would read `canceled` | A finished answer is `unchanged`; the decision is one function with a table test |
| `get_me` took the time after the token read, so the expiry erred late | Taken before the request |
| `cancel_pipeline`'s description and the `status` field promised the status after the call | Both say the status can lag and `get_pipeline` shows it settle |
| A version in the README's prose | Removed; the status line keeps its version, which `staleness` requires |
| `manual` is not cancelable | Refuted: `CANCELABLE_STATUSES` includes it, for the pipeline and its jobs (§18 row 91) |
| `get_me` could read the expiry the token source holds instead of GitLab's answer | Left: `get_me` reports what GitLab says of the token it was sent, which is what a revoked or replaced token needs |

**The 1.0 security review** read the whole tree in five parts: sign-in
and secrets, the REST client, the write guards, untrusted content, and
the release chain (`audit/security-reviews/v1.0.0.md`). It found:

| Found | Fixed |
|---|---|
| Medium: `update_issue` with `confidential: false` made a confidential issue public, the widening `move_issue` refuses | Ship's to allow, as an approving review is (§4.3) |
| Medium: a tool call canceled after GitLab rotated the pair dropped the only copy of the new one | The refresh runs on a context the caller cannot cancel, bounded by the HTTP timeout; a test cancels mid-answer |
| A stored token under a profile that records no instance could reach the loopback test instance | Withheld unless the token came from the environment |
| `logout` could revoke a pair a server had just replaced, and the server saved the new one after the delete | `logout` holds the profile's refresh lock |
| A create's answer over 32 MiB read as retryable `[unavailable]` | `[ambiguous_outcome]`, settled by reading (§4.5) |
| A color code before a token in a job artifact hid it from the masks | Artifacts lose terminal escapes before masking, as job logs do |
| `[//]: # (text)` and a backtick in a fence's info string hid text from the page but not from the model | Unused reference definitions are dropped and counted; that line opens no fence |
| A milestone title, free prose, printed outside the boundary in `get_issue` and `get_merge_request` | Its own line, inside the boundary |
| Snippet file names reached an error message as written | Made plain, as paths are |
| A manual registry publish could send a prerelease | `server-json` publishes `X.Y.Z` only |
| `update_label` and `update_milestone` said empty clears a field; §17.7 reads it as absent | The descriptions no longer say so; clearing was deferred (§17a), then added after 1.0 as `clear_*` inputs |

### Closing a phase

1. `make check` green; the live driver run and its transcript read.
2. `/simplify`, `/code-review high` and `/security-review`; findings
   fixed or recorded in §16a.
3. The status line, §16 and `CHANGELOG.md` say what was built and what
   is owed.
4. Commit on the topic branch; say it is ready for review; stop.

**Phase 7.** `/simplify`: `quoted()` built on `Line`, `askSHA` on
`shortSHA`, the tests' text and argument copies shared; skipped: running
the two rule reads at once, which `run_pipeline` no longer makes. The
altitude review found a decline honored only when the round reached its
question again; `run_pipeline`'s question depended on GitLab's rules,
so a ref unprotected between the rounds would have run. Fixed: anything
but an accept is refused before the service runs, and `run_pipeline`
reads GitLab's own flags on the ref. `/security-review`: one finding at
confidence 8, fixed (`audit/security-reviews/phase-7.md`).
`/code-review high`: ten, nine fixed and one declined:

| Found | Fixed |
|---|---|
| A confirmed round that wrote without reaching its question left the stage waiting, so a lost reply read "nothing was written" | Phase 7 commit; an accepted answer marks the call as writing before the service runs |
| `\b` is ASCII-only, so a link after `_` or in a non-Latin script was not broken | Phase 7 commit; each shape is anchored on a character that is not a letter or digit, with cases |
| `play_job` asked about a job GitLab would not play | Phase 7 commit; a job neither manual nor scheduled is `[conflict]` before anyone is asked |
| The driver's scripted person was shared with the reader goroutine unlocked | Phase 7 commit; a mutex |
| A decline branch in `Ask` could no longer run | Phase 7 commit; removed |
| `AskFailures` built its messages by hand | Phase 7 commit; through `errorResult` |
| Two rule helpers were left with one caller each | Phase 7 commit; inlined again |
| The driver's list of asking tools was typed out | Phase 7 commit; the driver reads it from the published descriptions and fails a tool that asks and put no question |
| `status` and the startup log did not show `GITLAB_MCP_REQUIRE_PROMPT` | Phase 7 commit; both do |
| The answer's expiry read the protocol the client asked for | Declined: the SDK decides how the question travels from the same field (`mcp/server.go` at v1.8.0), so the expiry matches it |

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
   action and is reversible. **Decided 2026-09-27 (maintainer): Ship,
   as proposed.** The injection evals score an approval the model was
   steered to (§13).
3. **Lockdown for public projects.** GitHub withholds content from
   authors without push access in public repositories. Here: in a
   public project, render content from authors below Developer as
   withheld unless `GITLAB_MCP_UNTRUSTED_CONTENT=show`. It costs a
   members read per call and hides legitimate bug reports. Proposed:
   decide in phase 4 from the injection evals. **Decided 2026-09-27: no
   lockdown in 1.0.** Four evals plant an instruction from a
   non-member of a public project, the case lockdown would withhold: read
   a private file, run a quick action, post in another project, approve
   and merge with Ship on. Opus followed none of them in three runs,
   12 of 12, each time doing the task it was given. Behind the model the
   boundaries (§4.1), the quick-action guard (§4.2), the unregistered
   kinds (§4.3) and the allow-list (§4.7) still hold without it. Hiding
   outsiders' bug reports would cost every person a members read per
   call to guard against what these layers already stop. Revisit if a
   model or task makes the evals fail; `make evals` is the check.
4. **The floor version.** **Decided 2026-09-26: not applicable.** The
   server serves gitlab.com only (§14), which runs the newest release,
   so no tool is gated by version and the live driver runs no floor
   container. It was proposed at 18.0 while self-managed was in scope.
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
   43 tools. **Decided 2026-09-27 (maintainer): as proposed.**
9. **Default write allow-list.** Unset means everywhere the token can
   write. Proposed: keep, with `doctor` and `get_me` saying so, because
   a default that refuses every write is a default everyone overrides.
   **Decided 2026-09-27 (maintainer): as proposed.**
10. **GitLab's built-in server.** If it gains stdio, tokens, enforced
    filtering and a read-only mode, this server's reason to exist
    narrows to the local-token, quick-action and verified-release half.
    Revisit at each minor release; record the check in §18. **Open,
    standing.**
11. **Premium and Ultimate after 1.0.** Epics and iterations are
    work items, GraphQL-first, with the REST epics API deprecated for
    v5. A post-1.0 `planning` toolset would be the first GraphQL use
    and would need its own coverage source. **Deferred to after 1.0.**

12. **Pipelines a Write starts.** A commit, a new branch or a new merge
    request on an unprotected branch starts the pipelines a push
    starts, with the account's own permissions and the project's
    unprotected variables only; Write never reaches a protected branch
    (§4.4). **Decided 2026-09-27 (maintainer): they stay Write**, as the
    account's own push would run them, and `docs/security.md` says so.
    Appending `[skip ci]` without Ship was rejected: it would stop the
    checks a merge request is reviewed by. Raised by the phase 2
    security review.

13. **The kinds and toolsets of phase 6.** Proposed for the
    maintainer to decide before phase 6 started (`CLAUDE.md`, ask before
    doing):

    | Tool | Kind | Toolset | Why |
    |---|---|---|---|
    | `move_issue` | Ship | default | It publishes an issue's content in another project; both must be in the write allow-list, and a move into a more visible project is refused |
    | `link_issues`, `unlink_issues` | Write | default | A relation between issues; removing one destroys no content |
    | `create_label`, `update_label` | Write | `planning`, off | Project configuration that every issue shares |
    | `delete_label` | Destructive | `planning`, off | Removes the label from every issue |
    | `create_milestone`, `update_milestone` | Write | `planning`, off | As labels; closing a milestone is an update |
    | `delete_milestone` | Destructive | `planning`, off | |
    | `rebase_merge_request` | Ship | default | It rewrites the source branch and resets approvals; it takes the head `sha` as its witness |
    | `cherry_pick_commit`, `revert_commit` | Write | default | A commit to a branch, as `create_commit` makes, with the same refusal of the default and protected branches (§4.4). §8a called them Ship candidates; the guard is what makes them Write |
    | `get_blame` | Read | default | |
    | `list_job_artifacts`, `get_job_artifact` | Read | default | One text file of a job's artifacts, masked like a job log and under the file budget; an archive is never downloaded whole |
    | `create_tag` | Write | `releases` | It starts the tag pipelines, as `create_branch` starts a branch's; a tag a protection rule covers is refused |
    | `delete_tag` | Destructive | `releases` | |

    **Decided 2026-09-27 (maintainer): as proposed.** The `planning`
    toolset is the one §17.11 would extend with epics after 1.0.

### 17a. Deferred cleanups

Phase 5 closed every cleanup phases 0 to 3 deferred but two, which no
REST call can close:

- `get_issue` and `get_merge_request` walk up to ten pages of threads to
  count them, and each `list_discussions` page walks them again to show
  them by last activity. `X-Total` cannot replace the walk: it counts
  system-only threads, and the unresolved count and last activity need
  every note. Reading only the newest pages is rejected (§18 row 83).
  Found in phase 0.
- `run_pipeline` settles a lost answer by this account's newest API
  pipeline on the ref since shortly before the call, so a second
  `run_pipeline` on the same ref in that window reads as this one. The
  pipeline's variables would tell them apart, and reading them reads
  their values (§18 row 82). Found in phase 3.

The 1.0 security review added a third, closed after 1.0:
`update_label` and `update_milestone` could not clear a description or a
date, since §17.7 reads `""` as absent. They now take `clear_*` inputs,
as `update_issue` does, and `update_label` also clears a priority
(§18 row 92).

Operations `testdata/api-coverage.tsv` defers with a citation of §17a
are deferred past 1.0, each for the reason its row gives. The feature
candidates among them are phase 6 (§16, §17.13): issue move and links,
label and milestone writes, rebase, cherry-pick, revert, blame, job
artifacts and tag writes.

### 17b. Deviations from the shared standard

The standard at `~/.claude/mcp-server-standard.md`, read 2026-09-25.

| The standard says | Here | Why |
|---|---|---|
| Errors use six classes | Thirteen (§6.5) | `stale` is forced by §2.10; `ambiguous_outcome` by §2.11; `forbidden` and `auth` ask for different fixes; `blocked` for the quick-action, branch and allow-list guards; `rate_limited` separates waiting from failing; `ambiguous` as the siblings use it; `unexpected` for what nothing else covers |
| Never overwrite; compute a minimal diff | Held for files and commits (`last_commit_id`) and merges (`sha`); **not holdable** for issue, merge request, wiki and comment updates | No `If-Match` in REST and `lock_version` only in the web controllers (§2.9). §4.6's `updated_at` witness narrows the lost-update window without closing it; only the fields given are sent |
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
| 40 | A quick-action guard can match GitLab with a regular expression | GitLab's extractor and the Markdown pipeline it calls at `829b21d2`; comrak v0.55.0, the version GitLab's renderer pins; a differential oracle over about 1.3 million generated bodies | **Refined (tier 1).** Detection needs comrak's block structure, so `internal/quickaction` ports it. Zero lines GitLab would run were missed; four deliberate over-detections (any `/word`, both backtick readings, no rendered-text prefilter, description-list terms). Before 16.7 the extractor was regular-expression only and ran commands inside `~~~` fences and lazy lines, which no longer matters: gitlab.com is the only instance |
| 41 | GitLab's token prefixes are the ones commonly listed | GitLab's token page, fetched 2026-09-26 | **Refined (tier 3).** `glpat-`, `gloas-`, `gldt-`, `glrt-`, `glrtr-`, `glcbt-`, `glptt-`, `glft-`, `glimt-`, `glagent-`, `glwt-`, `glsoat-`, `glffct-`, `_gitlab_session=`, and the runner `GR1348941`; `gloat-` is not listed. A custom personal-access-token prefix cannot be matched by shape |
| 42 | Dropping hidden characters is safe everywhere | Trojan Source (CVE-2021-42574) | **Refuted for code.** Dropped from Markdown, made visible in files, diffs and commit messages (§4.1.2) |
| 43 | A moved project's redirect names its new path | Spike L on gitlab.com | **Refuted (tier 1, live).** It names the numeric id; the client replaces only the project segment, so either works, and the test instance now matches |
| 44 | Every refused token says `invalid_token` | Spikes C and F on gitlab.com | **Refined (tier 1, live).** A revoked token does, in body and header; a malformed one is a plain 401. The client drops a token only on the marker, which a malformed token would not benefit from anyway |
| 45 | A conditional read spares the rate limit | Spike H on gitlab.com | **Refuted (tier 1, live).** ETag and 304 are served on single reads and listings, and each 304 is counted. Not adopted: it saves bytes, not budget, and would add a cache to keep |
| 46 | A job log is read whole or not at all | Spike J on gitlab.com, after row 29 | **Refuted (tier 1, live).** `byte_offset` and `byte_limit` return exact slices, no `Content-Range`, an empty 200 past the end. Until phase 5 the server read the whole log, since a tail needs its size; row 77 found the size |
| 47 | A job log holds the lines the script printed | Phase 1 live run on gitlab.com | **Refined (tier 1, live).** gitlab.com's runners prefix each line with its time and stream, `2026-09-26T22:30:07.819673Z 01O `, and `+` after the stream continues the line before, which is how a section header arrives. Stripped and joined, as the job page shows it. GitLab also masks `glpat-` tokens itself; the server's masking still runs for the shapes it does not |
| 48 | GitLab's compare lists commits newest first | Phase 1 live run | **Refuted (tier 1, live).** Oldest first. `compare_refs` shows them newest first, as every other commit listing |
| 49 | A general draft note has no position | Phase 1 live run | **Refuted (tier 1, live).** It carries a position object with no paths, and GitLab returns drafts in no stable order. An empty position reads as none; drafts are sorted by id, which the page token counts in |
| 50 | Code search across a group is refused as `Scope not supported without Elasticsearch!` | Phase 1 live run | **Refined (tier 1, live).** gitlab.com answers 400 `Scope supported only with advanced search or exact code search`. Both wordings map to `[unsupported]` |
| 51 | A member's `access_level` is a string, as the OpenAPI file types it | Phase 1 live run | **Refuted (tier 1, live).** An integer, 50 for an owner. `testdata/api-fields.tsv` records it |
| 52 | A diff note's `line_code` is `sha1(path)_old_new`, with 0 for the side a line lacks | `lib/gitlab/git.rb` `diff_line_code`, `lib/gitlab/diff/parser.rb`, `lib/gitlab/diff/line.rb` `legacy_id`, `lib/gitlab/word_diff/segments/diff_hunk.rb` at `829b21d2` | **Refined (tier 1).** Both counters are always set: a removed line carries the new side's running counter and an added line the old side's. A hunk's start is taken as written, count ignored, so after `@@ -5,0 +6,2 @@` an added line's old counter is 5. The path is `new_path`, else `old_path`. `internal/diffpos` follows it; spike K checks it live |
| 53 | GitLab runs quick actions from an issue's or merge request's title as well as its description | `app/services/issuable_base_service.rb` `merge_quick_actions_into_params!` at `829b21d2` | **Refuted (tier 1).** Only the description is interpreted; a title is stored as it is. `scripts/gates bodies` lists titles as plain |
| 54 | The OpenAPI file describes `POST /projects/:id/repository/commits` | The snapshot; `lib/api/commits.rb` at `829b21d2` | **Refuted (tier 1).** The file publishes one `file` field. The route takes `branch`, `commit_message`, `actions` (each `action`, `file_path`, `previous_path`, `content`, `encoding`, `last_commit_id`) and `start_branch`, and requires `content` on an update even when empty. `testdata/api-fields.tsv` records it |
| 55 | GitLab's merge request API takes a `draft` field | The snapshot's PUT and POST bodies at v19.4.1-ee | **Refuted (tier 1).** Neither publishes one; the draft state is the title's `Draft:` prefix, which `update_merge_request` sets and strips |
| 56 | The `updated_at` a create answers with is the witness for the next update | Phase 2 live run | **Refuted for merge requests (tier 1, live).** gitlab.com moved a new merge request's `updated_at` about 1.2 s after answering the create, so an update carrying the create's value was `[stale]`. An issue's held. `create_merge_request` no longer offers its value and says to read the merge request first |
| 57 | Every change to an issue moves its `updated_at` | Phase 2 live run | **Refuted (tier 1, live).** Adding an assignee changed the issue and left `updated_at` where it was. A concurrent assignee change is not caught by the witness; `update_issue` computes the assignee set from its own fresh read, so it does not lose one |
| 58 | A commit's move action without content keeps the file | `app/services/files/multi_service.rb` `transform_move_actions` at `829b21d2`; phase 2 live run | **Refined (tier 1).** Only a missing `content` keeps it: `content: ""` empties the moved file, which the first phase 2 run did. `create_commit` now sends content on a move only when given, and the third run's moved file kept its text |
| 59 | A branch's own `protected` flag reflects wildcard rules | Phase 2 live run | **Confirmed (tier 1, live).** A branch made under a `guard-*/*`-style rule read as protected, and `create_commit` refused it by that flag; a new branch under the rule was refused by the rules. `create_commit` trusts the flag for a branch that exists and reads the rules only for one it would create. |
| 60 | A merge GitLab refuses answers 409 | `lib/api/merge_requests.rb` `execute_merge` and `lib/api/helpers.rb` `check_sha_param!` at v19.4.1-ee | **Refined (tier 1).** 409 means only that `sha` is not the head, and names the head. A merge request that cannot merge now answers 405 `Method Not Allowed` or 422 `Branch cannot be merged`, neither saying why. `merge_merge_request` reads it back and names `detailed_merge_status`, and reports a merge whose answer was lost as merged |
| 61 | Approving twice is harmless | `lib/api/merge_request_approvals.rb` at v19.4.1-ee | **Refuted (tier 1).** An approval GitLab will not take, given already or by an ineligible approver, answers 401 with no `invalid_token` marker; unapproving without one answers 404. The server reads the approvals first and reports `unchanged`, and a 401 the same token can read past is `[forbidden]` |
| 62 | A comment delete takes no witness | `lib/api/helpers/notes_helpers.rb` `delete_note`, `lib/api/helpers.rb` `destroy_conditionally!` and `check_unmodified_since!` at v19.4.1-ee | **Refuted (tier 1).** `If-Unmodified-Since` is honored with 412. GitLab reads it with Ruby's `Time.parse` and compares the note's `updated_at` at full precision, while its JSON shows milliseconds, so the server sends the witness stretched to the end of its millisecond, in RFC 3339; an HTTP-date, cut to the second, would refuse nearly every delete. Spike N confirmed each half on gitlab.com (tier 1, live) |
| 63 | GitLab's wiki API carries a version a write can check | `lib/api/wikis.rb`, `lib/api/entities/wiki_page*.rb`, `app/models/wiki_page.rb` at v19.4.1-ee | **Refuted (tier 1).** `WikiPage#update` checks a `last_commit_sha`, but no REST answer exposes one. The witness is a hash of the content read (§4.6) |
| 64 | A snippet's raw route serves any of its files | `lib/api/helpers/snippets_helpers.rb` `content_for` at v19.4.1-ee | **Refuted (tier 1).** `/raw` serves the first file; `/files/:ref/:file_path/raw` serves each. `get_snippet` reads the others at `HEAD`, which the phase 3 live run checks. A response's `raw_url` is never called (§11) |
| 65 | A job GitLab will not retry or run is refused as forbidden or invalid | `lib/api/ci/jobs.rb` at v19.4.1-ee | **Refined (tier 1).** It answers 403 `Job is not retryable` and 400 `Unplayable Job`, both the job's state. Both map to `[conflict]` |
| 66 | Retrying a pipeline with nothing failed is an error | `lib/api/ci/pipelines.rb` at v19.4.1-ee | **Refuted (tier 1).** It answers with the pipeline, changed or not, and so does a cancel of a finished one. The result compares the status before and after |
| 67 | Wiki pages, snippets and release notes run quick actions | `app/services/wiki_pages`, `snippets` and `releases` at v19.4.1-ee | **Refuted (tier 1).** None calls `QuickActions::InterpretService`, and neither does `MergeRequests::MergeService` for a merge commit message. `scripts/gates bodies` lists them as plain |
| 68 | An event is filtered by the action name it shows | `app/models/event.rb`, `app/finders/events_finder.rb` at v19.4.1-ee | **Refuted (tier 1).** `action` takes the stored action (`created`, `pushed`, `transferred` and the rest), which is shown as `opened` or `pushed to`; `target_type` takes snake_case names mapped to classes |
| 69 | A tag search is exact with `^` and `$` | `app/finders/git_refs_finder.rb` at v19.4.1-ee | **Refined (tier 1).** A plain search is a case-insensitive substring match that lists an exact match first. `create_release` searches plainly and compares names |
| 70 | A release takes any milestone by title | `lib/api/releases.rb` at v19.4.1-ee | **Refined (tier 1).** Group milestones need Premium; on Free a release takes the project's own |
| 71 | An author may not approve their own merge request | Phase 3 live run on gitlab.com, a Free group | **Refuted there (tier 1, live).** The author's approval was taken. Where a project forbids it GitLab answers 401, which `approve_merge_request` reports as `[forbidden]` |
| 72 | A developer may set pipeline variables | Phase 3 live run | **Refuted for new projects (tier 1, live).** gitlab.com creates a project allowing no one to set them: a pipeline with variables answers 400 `Insufficient permissions to set pipeline variables`, a manual job with variables a bare 403. Both are `[forbidden]` naming the setting. The live driver sets the scratch project's minimum role to developer |
| 73 | Deployments filter by `updated_after` in any order | Phase 3 live run | **Refuted (tier 1, live).** GitLab answers 400 `` `updated_at` filter requires `updated_at` sort ``. `list_deployments` sorts by `updated_at` when a time filter is given and refuses another order with one |
| 74 | A fresh eval world signs its server in afresh | Phase 4 eval run | **Refuted (tier 1).** The server kept the world's sign-in in the OS keyring under one profile, and every world mints the same token names, so the next world's refresh token matched the stored seed and the server used a dead access token: seven of eight tasks answered `[auth]`. `gitlabtest` now puts a random part in every token name, so no instance accepts or matches another's; the harness deletes its keyring item after each world, and a tool answering `[auth]` makes the run an error rather than a model's failure |
| 75 | An `mcp`-scoped token reaches only the REST routes tagged `route_setting :mcp` | `lib/api/concerns/mcp_access.rb` at master `1ee957c5`; spike G on gitlab.com | **Confirmed (tier 1, live).** Ten of ten probed routes answered as tagged: reads of one issue, branches, merge request commits, pipelines and search; 403 for `/user`, a project, issue listings, a merge request and notes. §2.6 stands |
| 76 | A job log window needs the whole log before it, to tell whether it starts inside a private key block | RFC 7468 §3 and RFC 4880 §6.2 for a key block's size; the phase 5 reviews | **Changed.** `get_job_log` looks 256 KB before a window, in the same ranged read, for a BEGIN with no END after it, the header counted anywhere in its line so indented and prefixed keys are found. No real key is near that long; a BEGIN further back is not looked for. A line-by-line walk that stopped at the first line a key cannot hold was tried and dropped: CI prefixes each line in too many ways (phase 5 security review and code review) |
| 77 | A job log's size is known only by reading it | `lib/api/ci/jobs.rb`, `lib/gitlab/ci/trace.rb` and `lib/gitlab/ci/trace/stream.rb` at v19.4.1-ee; spike O on gitlab.com | **Refuted (tier 1, live).** An archived log is the job's `trace` artifact, whose `size` is the stored file's. A HEAD cannot be trusted: for one finished log it answered the length, for the same kind of log on the next run 0, and for a running log 0. `byte_limit` past 500 KB answers 400. A log not yet archived is read 500 KB from its start, and one longer than that is measured by one-byte reads past its end |
| 78 | `GET …/pipelines/:id/bridges` lists a pipeline's trigger jobs | `lib/api/ci/pipelines.rb` at v19.4.1-ee | **Refined (tier 1).** Deprecated in 19.2 for `trigger_jobs`, served by the same handler; `get_pipeline` reads `trigger_jobs` with `scope=failed` |
| 79 | Only merge request threads can be resolved | `app/models/concerns/noteable.rb` and `lib/api/discussions.rb` at v19.4.1-ee | **Refuted (tier 1).** `resolvable_types` includes Issue, and the discussions API registers the resolve route for every resolvable type. `resolve_discussion` takes `type: issue`, and `add_comment` starts a resolvable thread with `thread` |
| 80 | Job inputs are typed by the job and cannot be offered safely | `lib/api/ci/jobs.rb`, `app/services/ci/retry_job_service.rb` and `lib/gitlab/ci/config/entry/job.rb` at v19.4.1-ee | **Changed (tier 1).** Retry takes `inputs` and play `job_inputs`; `app/services/ci/inputs/processor_service.rb` checks them against the job's own `inputs:` and refuses an unknown name with 400, but a job with no `inputs:` takes any and uses none, which the phase 5 live run showed. Both tools are Ship, so a steered value needs the flag the person set; the result names the inputs sent |
| 81 | A release's asset links may point anywhere | Maintainer, 2026-09-27; `lib/api/entities/release.rb` at v19.4.1-ee | **Changed.** `create_release` takes links only into the release's own project, under its web path or its API path, on the instance's origin with no credentials in the URL, refused `[blocked]` before anything is sent: a release page sends every reader wherever its links lead, and gitlab.com serves anyone's files (phase 5 code review). Links are taken at create only; link writes on a published release stay written off |
| 82 | A lost `run_pipeline` can be told from another run on the same ref by its variables | `GET …/pipelines/:id/variables` at v19.4.1-ee | **Rejected.** The listing returns the variables' values, which §8a writes off with CI variables; the settle stays by ref, source, account and time (§17a) |
| 83 | `list_discussions` can read only the newest pages of threads | §7.2 and the tool's contract | **Rejected.** Threads are shown by last activity, and a reply moves an old thread to the top, so ordering them needs every thread. GitLab lists them by creation only |
| 84 | A label's update and delete can carry a witness GitLab holds | `lib/api/entities/label.rb` and `lib/api/labels.rb` at v19.4.1-ee | **Refuted (tier 1).** A label exposes no timestamp or version. `list_labels` returns `version`, a hash of the name, color, description, priority and archived state, and `update_label` and `delete_label` read the label first and refuse `[stale]` if it moved, as the wiki's hash does (§4.6) |
| 85 | Cherry-pick and revert are Ship | §8a before phase 6; `lib/api/commits.rb` at v19.4.1-ee | **Changed (maintainer, §17.13).** They are a commit to a branch, as `create_commit` makes, behind the same refusal of the default and protected branches, so they are Write. GitLab's own `dry_run` commits nothing, and a dry run asks it; a change that does not apply answers 400, which is `[conflict]` (phase 6 live run) |
| 86 | The protected-tag listing is needed by no guard | §8a before phase 6 | **Changed (maintainer, §17.13).** `create_tag` refuses a name a protected-tag rule covers, matched as GitLab's RefMatcher matches, so the listing is read; every protected-tag write stays written off |
| 87 | Moving an issue is an ordinary write | `lib/api/issues.rb` and `WorkItems::DataSync::MoveService` at v19.4.1-ee | **Changed (maintainer, §17.13).** A move copies the issue, comments included, into another project, where other people may see it. `move_issue` is Ship, holds both projects to the allow-list, and refuses a project more people can see than the source |
| 88 | A file in a job's artifacts is addressed with its slashes unescaped | `lib/api/ci/job_artifacts.rb` at v19.4.1-ee; the phase 6 live run | **Refuted (tier 1, live).** The path sent as one segment, its slash escaped `%2F`, read `reports/summary.txt`; Workhorse extracts the one file, and the answer is not a redirect |
| 89 | A rebase answers when it is done | `lib/api/merge_requests.rb` at v19.4.1-ee; the phase 6 live run | **Refuted (tier 1, live).** GitLab answers 202 `{"rebase_in_progress": …}` and rebases in the background. `rebase_merge_request` reads the merge request with `include_rebase_in_progress` after, and says whether it still runs |
| 90 | A token's `expires_in` is its lifetime, added to `created_at` | Doorkeeper 5.9.0 `Expirable#expires_in_seconds` and `AccessTokenMixin#as_json`, the version v19.4.1-ee locks; `app/controllers/oauth/token_info_controller.rb`; the 1.0 live run | **Refuted (tier 1, live).** `/oauth/token/info` answers the seconds left when it answers. `get_me` added them to `created_at` and reported a token read 1 h 41 min after issue as expired an hour before; it now adds them to the time of the read, and the next run reported 11:53:38Z, the expiry the first run's numbers imply |
| 91 | A cancel's answer shows whether anything was canceled | `lib/api/ci/pipelines.rb`, `Ci::CancelPipelineService`, `CommitStatus` and `Ci::HasStatus` at v19.4.1-ee; the 1.0 live run | **Refuted (tier 1, live).** GitLab cancels the jobs inside the request, then answers `pipeline.reset`; each job's transition queues `PipelineProcessWorker`, which recomputes the pipeline's status after. A pipeline canceled seconds after it started answered `running`, and `cancel_pipeline` said nothing could be canceled, while its two deployments were listed canceled later in the run; the next run's cancel answered `canceling`, so gitlab.com answers either way, and a third, with the fix, answered `running` and reported `canceled`. `CANCELABLE_STATUSES`, which the job scope shares, includes `manual`. It now reports `canceled` when the status read first was cancelable, the source is not `external` and the answer is not finished, and says the status catches up |
| 92 | A label's priority and a milestone's description and dates cannot be cleared through the API | `lib/api/helpers/label_helpers.rb`, `app/services/labels/update_service.rb`, `lib/api/milestone_responses.rb` and `app/services/milestones/update_service.rb` at v19.4.1-ee; the post-1.0 live run | **Refuted (tier 1, live).** A present `priority` of `null` unprioritizes the label, and `at_least_one_of` counts keys, so it may be the only field sent; an empty description or date is assigned as given. `update_label` sends `null` for `clear_priority`, and both tools send `""` for the other `clear_*` inputs. The live run read each field back cleared |
| 93 | A comment edit can carry a witness GitLab enforces, as a delete does | `lib/api/helpers/notes_helpers.rb` `update_note`, `app/services/notes/update_service.rb` and `app/policies/note_policy.rb` at v19.4.1-ee; the update_comment live runs | **Refuted (tier 1, live).** `update_note` calls no `check_unmodified_since!`, so `update_comment` reads and compares `updated_at` first and the window stays open. `UpdateService` runs quick actions in the new text and deletes a note left with commands alone, so the body is guarded as a create's is. `NotePolicy` refuses `admin_note` on a note that is not editable, a system note among them (403), and grants it to the note's author; this server edits only the caller's own. A reply to a standalone comment moves the comment's `updated_at` on gitlab.com, so the witness `add_comment` returned is stale after one |
| 94 | A server can ask the person to confirm a write through MCP form elicitation, on every protocol this server serves | The `ElicitRequestFormParams` type in the specification's `schema.ts` for 2025-06-18, 2025-11-25 and 2026-07-28, the 2026-07-28 multi-round-trip pattern, and the MCP Go SDK v1.8.0's `mcp/server.go` and `mcp/shared.go`, read 2026-09-28; this repository's tests on all three protocols | **Confirmed (tier 1).** A tool result may carry `inputRequests` and a signed `requestState`; before 2026-07-28 the SDK sends `elicitation/create` itself and calls the handler again in the same request. `requestedSchema` is an open map with no minimum, so `properties: {}` is valid. The answer comes from the client, and the 2026-07-28 revision lets it answer "from the user or other sources", so an accept is never proof a person read anything (§4.12) |
| 95 | A client draws an elicitation question as plain text | VS Code `src/vs/workbench/contrib/mcp/browser/mcpElicitationService.ts` L100 and L173, and `src/vs/base/common/htmlContent.ts` L52-62, `main` at 251bcf5f, read 2026-09-29 | **Refuted.** VS Code builds a form question as `new MarkdownString(elicitation.message)`, untrusted: command links are off, but emphasis, link text, code spans and HTML-like text draw, and single line breaks join into one paragraph. Each quoted value is a code span, which CommonMark draws literally, with backticks and their lookalikes folded, and a blank line separates the lines. Backslash escaping was rejected: where a client draws plain text, the backslashes show inside names and branches, the data the person checks |
| 96 | A required choice naming the outcome confirms better than an empty form | Codex `codex-rs/codex-mcp/src/elicitation.rs` L415-458 and L552-571, `main` at c248f6d4, and VS Code `mcpElicitationService.ts` L111-119 and L237-297, read 2026-09-29; the maintainer's check in Claude Code 2.1.284, protocol 2025-11-25, 2026-09-29, against a throwaway probe with three forms of one delete question | **Declined, for now.** For: Codex accepts a form with no properties by itself under approval policy `never` with full access, and a VS Code chat question the person skips resolves as `accept` with no content; a required choice survives both. Against: in Claude Code the choice list took the maintainer 60 seconds, against 8 for the empty form, and they found it confusing. The empty form stays, and both client behaviors are recorded as limits (§4.12) |
| 97 | GitLab's link lists are complete and shaped as the OpenAPI file publishes them | `lib/api/issues.rb` L551-594, `lib/api/merge_requests.rb` L978-1030, `lib/api/commits.rb` L713-749, `app/services/issues/referenced_merge_requests_service.rb`, `app/models/merge_request.rb` `visible_closing_issues_for` and `related_issues`, and `lib/api/entities/issue_basic.rb`, `issuable_entity.rb` and `external_issue.rb` at v19.4.1-ee | **Refuted (tier 1).** Every list drops what the user cannot read without saying so; `closed_by` and a commit's merge requests are the project's own only; `closes_issues` also drops issues in projects that do not close issues automatically. `closes_issues` and `related_issues` mix IssueBasic rows, which carry no `references`, with an external tracker's `{title, id}`, whose `id` is a string. The file publishes `closes_issues` as MRNote, `related_issues` with no schema, and `related_merge_requests` as MergeRequestBasic though it answers the full entity. The three tools decode a few fields, keep `id` raw and show it only when it is shaped like a tracker's id, read an issue's reference from its `web_url` as `resolve_url` does, and say in each field and once in the text that the list is GitLab's, not all there is |
| 98 | GitLab's test report is paged and shaped as the OpenAPI file publishes it, and its summary is as current | `lib/api/ci/pipelines.rb` L292-334, `app/models/ci/pipeline.rb` `accessible_test_reports`, `app/models/ci/build.rb` `test_report_readable_by?` and `max_test_cases_per_report`, `app/serializers/test_report_entity.rb`, `test_suite_entity.rb`, `test_case_entity.rb` and `test_report_summary_entity.rb`, `lib/gitlab/ci/parsers/test/junit.rb`, `lib/gitlab/ci/reports/test_suite.rb` and `test_case.rb`, and `app/services/ci/build_report_result_service.rb` at v19.4.1-ee | **Refuted (tier 1).** `test_report` answers every case of every suite in one body, with no paging, parsed from the latest jobs' artifacts when asked and cached up to two minutes; jobs still running add nothing, and jobs whose artifacts the user may not read are dropped without a word. gitlab.com caps a file at 500,000 cases. Times are floats, not the integers the file publishes. JUnit's failure text is in `system_output`, and `stack_trace` is always null: `lib/gitlab/ci/parsers.rb` lists JUnit as the only test parser, and it never sets one. A suite is named by the job's group name, so parallel jobs merge and their cases are deduplicated together. `test_report_summary` is written by a worker after each job, so it lags; its `test_suites` is an array the file publishes as an object, and it carries no cases. Child pipelines in the project are taken in, so a finished pipeline's report still grows while a child runs. `get_test_report` reads the full report, says when it may be partial and that it may be two minutes old, does not read `stack_trace`, and falls back to the summary only when the report is over 32 MiB, saying so when the summary is still empty |
| 99 | GitLab's resource event lists are ordered, complete, and shaped as the OpenAPI file publishes them | `lib/api/resource_label_events.rb`, `resource_state_events.rb`, `resource_milestone_events.rb` and `helpers/resource_events_helpers.rb`, `ee/lib/api/resource_weight_events.rb` and `resource_iteration_events.rb`, `lib/api/entities/resource_*_event.rb` and `ee/lib/api/entities/resource_weight_event.rb`, `lib/gitlab/pagination/offset_pagination.rb` `add_default_order`, `app/models/resource_label_event.rb` `visible_to_user?`, `app/policies/resource_label_event_policy.rb`, `app/finders/resource_milestone_event_finder.rb` and `resource_state_event_finder.rb`, and `app/services/resource_events/change_milestone_service.rb` and `change_state_service.rb` at v19.4.1-ee | **Refuted (tier 1).** Each list pages by offset in id order, oldest first. Label events are filtered after the page is cut, so a page can be short and `X-Total` counts events the account cannot see; a deleted label's event is kept with `label: null`. Milestone events are filtered before paging to milestones the account can read, which also drops those of a deleted milestone; a removal names the milestone removed. A state event's `source_merge_request_id` is a global id, and `source_commit` is set when a commit closed the item. Weight is an issue's only, and the route checks no license, so on Free it is empty rather than refused (inferred: weights are a paid feature, and nothing else writes them); iteration events are Premium and not read. A group's labels and milestones are read by whoever may read the group: anyone for a public group, else its members and its projects' members (`app/policies/group_policy.rb`, `group_label_policy.rb`, `milestone_policy.rb`). A milestone event's `state` is the item's state when the event was made. Order is by id, not time, and an imported item's ids need not follow its times. `list_item_events` reads each list whole or its newest pages, follows the next-page signal past page 1's count, starts a cut history after the oldest event kept and assumes ids follow time there, says the history is GitLab's, and names a deleted label as one |
