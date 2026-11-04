# Architecture

How `gitlab-mcp` is put together, and the decisions a contributor
should not undo without a better reason than the one that put them
here. Part 1 is the overview. Part 2, from [Decisions](#decisions), is
one entry per decision, cited elsewhere by its D-ID. What was verified
about GitLab lives in the comment at the code that depends on it, with
the source linked and the live step that checks it named.

## Context

`gitlab-mcp` is a Model Context Protocol server for GitLab. One Go
binary runs as a subprocess of an MCP client and speaks MCP over stdio.
It holds one person's OAuth tokens and calls GitLab's REST API v4, with
wire types of its own (D-01, D-08). Where REST has no route for an
operation, it sends a fixed GraphQL mutation through the same client
path (D-09). Nothing is hosted. It talks to gitlab.com only (D-02).

The parties around the server:

- **The person** registers their own OAuth application on gitlab.com,
  signs in once with `gitlab-mcp login`, and every call acts as them,
  with their own access (D-66).
- **The authors** wrote the issues, merge requests, comments, reviews,
  commit messages, files, wiki pages, snippets, release notes and job
  logs the server reads. Many write in public projects anyone can post
  to, and some write to steer an assistant, so their text is data and
  never instructions (D-13).
- **The operator** decides what the server may do, through
  `GITLAB_MCP_*` settings the client passes at start: read-only mode,
  the Ship and Destructive switches, the toolsets, the write namespaces
  and the upload directories (D-21, D-30, D-32, D-38, D-44). The
  operator is usually the person, setting up their own client.
- **The client** draws the approval prompt for each call from the tool
  annotations and its own rules. The server asks the person nothing
  itself (D-26).
- **The model** chooses the calls and their arguments. It can be wrong,
  or persuaded by content somebody else wrote, so nothing it sends
  widens what the operator registered (D-21, D-28).

The server talks to gitlab.com through:

- the REST API v4, for everything a tool reads or writes;
- the GraphQL endpoint, only for a mutation REST has no route for
  (D-09);
- GitLab's OAuth endpoints, for login, refresh and revocation.

Nothing else is reached, and a token goes only to the instance that
issued it (D-03, D-65).

It works inside projects: issues, merge requests, reviews, the
repository and CI (D-11). Wiki pages, snippets, releases and tags,
deployments, activity and planning writes are toolsets, off by default
(D-32).

It is not an administrator's tool (D-11), a reader of CI variables
(D-95), a webhook receiver (D-97), an API escape hatch (D-94) or a
server for self-managed instances (D-98). It never deletes a project or
force-pushes (D-96).

It exists so that GitLab content is read without steering the
assistant, so that nothing it writes runs a quick action, so that
merging and deleting stay absent unless the operator enables them, and
so that reviews land where a person would put them.

## Quality goals

In priority order:

1. **Content never steers the assistant through the server.** Content
   is marked, hidden text is counted, nothing is fetched, secrets in CI
   output are masked, and the server's own text never repeats an
   instruction (D-13, D-14, D-15, D-16, D-17).
2. **Nothing the server writes runs a command.** Every body passes one
   quick-action guard that refuses by default (D-18, D-19).
3. **A persuaded call cannot ship, delete or widen access unless the
   operator enabled it.** Ship and Destructive tools are unregistered by
   default. Code reaches a protected branch only through a merge
   request. Writes stay in the allowed namespaces, and nothing is made
   more visible except by its own Destructive tool (D-21, D-22, D-33,
   D-38, D-39, D-40, D-41).
4. **A write changes only what was read and named, and is never
   repeated blind.** Writes carry witnesses, omitted means unchanged, a
   lost answer is settled by reading, and every write takes `dry_run`
   (D-29, D-34, D-36, D-37).
5. **The stream, the logs and the repository stay clean, and the
   surface is a contract.** Stdout carries frames only, logs carry no
   payload, the repository holds nothing real, and a released name or
   field keeps working (D-04, D-05, D-06, D-78).

## Codemap

- `cmd/gitlab-mcp` is the program. With no subcommand it serves;
  `login`, `logout`, `status`, `doctor`, `--dump-schemas` and
  `--version` are the rest. It names stdout and stderr once and passes
  them down (D-04). It checks every argument for a help token before
  dispatch, so `logout --help` deletes nothing, and prints errors
  through the redactor (D-05).
- `internal/app` is the startup assembly every entry point shares: the
  profile, the OAuth application, the transport, the token source, the
  client and the server. It reads the token's scopes without refreshing
  (D-69), starts signed out (D-70), and withholds a token from an
  instance that did not issue it (D-03).
- `internal/auth` runs the loopback OAuth flow with PKCE and `state`,
  and holds the token source, the cross-process refresh lock and
  revocation (D-66, D-67, D-68, D-72).
- `internal/config` reads the `GITLAB_MCP_*` environment and the bound
  flags through one reader, validates them once, refuses a removed
  setting, and keeps the registry of every variable the server reads.
  Its one test override is loopback only (D-02, D-73).
- `internal/credentials` stores and finds the token pair: the
  environment, then the OS keyring, then an owner-only file with a
  warning on every use (D-71).
- `internal/diffpos` computes GitLab's position for an inline comment
  from a unified diff and the version's refs, with no network (D-53).
- `internal/fileperm` restricts a file to the account that wrote it,
  with a protected access list on Windows (D-71).
- `internal/gapi` is the raw REST client and the GraphQL mutations. It
  holds the rules every request shares: the token to its instance only,
  no redirect followed, paths escaped once, the rate model, retries by
  method, progress timeouts and the body cap, no write under a dry run,
  and the error classes (D-09, D-29, D-35, D-62, D-65, D-74, D-75,
  D-76).
- `internal/gapi/gitlabtest` is the in-memory GitLab the tests, the
  evals and the smoke gate run against, generated and never recorded,
  modeling what GitLab was seen to do (D-07).
- `internal/gitlab` holds the wire types, written by hand for the
  fields used. No field ends `_token` (D-08, D-10, D-61).
- `internal/instance` holds the base URL, gitlab.com or the loopback
  test override, checks origins against it, reads the version and
  edition, and turns GitLab web URLs into tool arguments for
  `resolve_url` (D-02, D-63).
- `internal/localimage` reads an image from an allowed directory
  through `os.Root`, checks it is a regular file and sniffs its type,
  with no network (D-44, D-45).
- `internal/model` is the server's view of what GitLab returns: the
  shapes each result carries as `structuredContent` under its output
  schema (D-77, D-78).
- `internal/quickaction` finds the lines GitLab would run as quick
  actions, following GitLab's extractor, and escapes them on request,
  with no network (D-18, D-19).
- `internal/redact` masks hosts, paths, accounts, ids and tokens in
  what `status`, `doctor` and errors print, and the token and key
  shapes in job output (D-05, D-16).
- `internal/render` writes the readable half of every result: the
  per-call boundary, hidden-text handling, link hosts, the budgets and
  the job-log rendering, held by golden files (D-13, D-14, D-15, D-54).
- `internal/scopes` holds the scope for each mode and what each kind
  needs, and generates the blocks [setup.md](setup.md) carries (D-30,
  D-31, D-66).
- `internal/server` wires the MCP SDK: the instructions built from the
  configuration, the resources, the per-call log line and the schema
  dump (D-05, D-21, D-81).
- `internal/service` holds orchestration and policy: project
  resolution, the write allow-list, the protected-ref guard, witnesses,
  settle reads, budgets and paging. It is where a GitLab failure
  becomes a `[class]` tool error (D-33, D-34, D-36, D-37, D-38, D-54,
  D-55).
- `internal/tools` defines the MCP tools, each added through
  `register`, whose `Kind` decides the annotations, registration, the
  quick-action guard, the dry-run context, `confirm` and both halves of
  the reply (D-20 to D-29, D-77, D-79).
- `internal/userconfig` stores non-secret per-profile state: the
  instance a profile signed in to, its application, the account and the
  granted scopes. It refuses a config directory outside the home
  directory.
- `internal/version` holds the version the build stamps in, with the
  module's build info as a fallback.

Beside the packages, `scripts/gates` is every check the repository runs
on itself, written in Go behind one registry (D-86, D-87).
`scripts/livegitlab` is the live driver (D-82, D-83). `scripts/evals`
scores a model driving the tools (D-85). `scripts/internal` holds what
the gates and drivers share: the stdio client, the transcript writer,
the redactor, git helpers and the TSV reader (D-84). `packaging/mcpb`
holds the Claude Desktop bundle's manifest (D-91, D-92). `testdata`
holds the released surface, the OpenAPI snapshot and its verdicts, the
field verdicts and the live waivers (D-10, D-78, D-82).

## Request flow

A tool call crosses the layers in one direction:

```
MCP client
   │  JSON-RPC over stdio; stdout carries nothing else
   ▼
internal/server     SDK session, tools, resources, one log line per call
   ▼
internal/tools      decode the input; register applies the Kind:
   │                annotations, gating, the quick-action guard,
   │                confirm, the dry-run context, both halves of the reply
   ▼
internal/service    project resolution, allow-list, protected refs,
   │                witnesses, settle reads, budgets, [class] errors
   ├──► internal/quickaction, internal/diffpos, internal/localimage
   ├──► internal/model   the server's view of GitLab
   └──► internal/render  the readable half and its boundaries
   ▼
internal/gapi       one request: token to its instance only, no redirect,
   │                paths escaped once, rate model, retry by method,
   │                no write under a dry run, classify
   ▼
internal/auth       refresh under a cross-process lock
   ▼
gitlab.com REST v4, its GraphQL mutations, GitLab OAuth
```

### Start

`main` loads the configuration and refuses a bad one before it
announces itself: read-only mode with an enable switch, a removed
setting still on, an upload directory that is relative, a root or the
home directory, or a test override that is not loopback (D-02, D-30,
D-44, D-73). `app` reads the token's scopes while the access token is
fresh, and never refreshes (D-69). With no sign-in the server serves
anyway, and each call answers `[auth]` naming `login` (D-70). A token
whose scopes cannot serve the mode is the one fatal outcome (D-31).
`register` adds every tool. A Ship or Destructive tool without its
switch, a toolset that is off, or a kind the scopes cannot serve is
left out (D-20, D-21, D-32).

### A read

A client calls `get_merge_request` on a merge request in a public
project. The service resolves the project once, to its numeric id
(D-62). It then reads the merge request, its approvals, its reviewers
and its linked lists at once. Each list is best effort and says it is
GitLab's, which leaves out what the account cannot see (D-54).

The description is cut at its budget, at a paragraph boundary, with the
offset to continue from (D-54). Hidden characters are dropped and
counted, and links show their host and are never fetched (D-14, D-15).
The description sits in a block whose token is drawn per call and
which names its origin and author. The structured half carries the same
text in `untrusted_*` fields (D-13, D-77). When GitLab has not finished
working out whether the merge request can merge, the result says to
read again in a few seconds.

`get_job_log` on a failed job reads a byte window from the end of the
stored log. It folds sections, strips ANSI codes, and masks token and
key shapes before anything is shown (D-16, D-56).

### A comment, then a merge

A client calls `add_comment` on a merge request. `register` passes the
body through the quick-action guard. A `/merge` line is `[blocked]`,
naming the line, unless `escape_commands` asks for it to be escaped
(D-18, D-19). The service holds the project to
`GITLAB_MCP_WRITE_NAMESPACES` (D-38). With `dry_run`, the call returns
the request body and the guard's verdict, and the client refuses any
write under that context (D-29).

The POST is sent once. If it times out after it was written, the
service reads the notes newest first for this account's comment with
this body. It reports created, not created or unknown, and never posts
again (D-34, D-35). An inline comment's position is computed by the
server from the diff and the version's refs, never by the model
(D-53).

`merge_merge_request` exists only under `GITLAB_MCP_ENABLE_SHIP` (D-21,
D-22). It takes the head `sha` the caller reviewed. GitLab refuses a
moved head with 409, which is `[stale]` (D-36). A lost answer is
settled by reading the merge request back, and the result says what
changed, read back rather than assumed (D-34, D-42).

### Login and refresh

`gitlab-mcp login --client-id` prints the scopes it will ask for, opens
GitLab's consent page, and listens on a random `127.0.0.1` port with
PKCE S256 and `state`. `--no-browser` prints the URL and the `ssh -L`
line to forward it (D-66).

The token pair goes to the OS keyring, or to an owner-only file with a
warning on every use. The environment overrides both (D-71). GitLab
revokes the old pair the moment it rotates, so a refresh runs under a
file lock, the new pair is stored before it is used, and an
`invalid_grant` re-reads the store before asking for a login (D-67).
An access token refused as `invalid_token` is dropped, and a read is
sent once more (D-68). `logout` revokes at GitLab and names the
profiles that share the application (D-72).

## Invariants

- Stdout carries only MCP JSON-RPC frames (D-04).
- No log line carries a host, a path, a branch, a title, a body, a
  username or a search term (D-05).
- Nothing in the repository identifies a real instance, account or
  person (D-06).
- A token is sent only to the instance that issued it, and never across
  a redirect to another origin (D-03, D-65).
- Content is returned only inside a boundary, and the server's own text
  never relays it (D-13, D-17).
- Nothing content references is fetched (D-15).
- No body the server sends runs a quick action (D-18).
- Ship and Destructive tools exist only under their switches, and every
  Destructive call takes `confirm` (D-21, D-28).
- No write creates or moves a protected branch or tag (D-33).
- Every write stays inside `GITLAB_MCP_WRITE_NAMESPACES` when it is
  set (D-38).
- No write makes content more visible, except `make_issue_public`
  (D-39, D-40, D-41).
- A create is never repeated blind (D-34).
- A write GitLab cannot guard carries a witness, and is refused
  `[stale]` when the target moved (D-36).
- A dry run writes nothing (D-29).
- The server never asks the person a question (D-26).
- `upload_file` reads only from the directories `GITLAB_MCP_UPLOAD_DIRS`
  names (D-44).
- Every reply carries `structuredContent` and a different text
  rendering (D-77).
- A read never silently returns less than it found (D-54).
- A released tool name, output field or resource URI changes only in an
  acknowledged major release (D-78).
- The live driver reads and writes only inside scratch projects it made
  (D-83).

## Cross-cutting concerns

**Configuration.** Every setting is a `GITLAB_MCP_*` variable, and most
have a flag bound to them. A flag overrides the environment, and the
environment overrides the default. Every reader goes through
`config.Lookup`, which drops the spaces around a value. Validation runs
once at start, and a removed setting is refused with its replacement
(D-73). The full list is in [configuration.md](configuration.md).

**Untrusted content.** Content is shown inside a block whose boundary
token is drawn per call, and the structured half names the same text
`untrusted_*` (D-13). Hidden text is removed from Markdown and made
visible in code, and counted either way (D-14). Nothing content links
to is fetched (D-15). Job logs, artifacts and test output are masked
for token and key shapes (D-16). The server's own text never phrases
content as something to do (D-17). [security.md](security.md) holds
the threat model.

**Kinds and approval.** Every tool has one kind. Read is every GET,
always registered. Write changes or removes something, and Adding only
adds; both are registered unless the server is read-only. Ship merges,
approves, rebases, applies suggestions, moves an issue, runs CI or
creates a release, and is registered only under
`GITLAB_MCP_ENABLE_SHIP`. Destructive deletes or makes an issue public,
and is registered only under `GITLAB_MCP_ENABLE_DESTRUCTIVE` (D-20 to
D-24). Registration, `confirm`, the guards, the witnesses and `dry_run`
hold on every client. The annotations and Claude Code's
`requiresUserInteraction` mark shape the client's prompt and hold
nothing on their own (D-25 to D-29). The README lists the tools.

**Errors.** Every tool error is `[class] message`, and the class tells
the caller what to do next. The vocabulary is closed and declared in
`internal/gapi/errors.go`, where each class's comment says what it
means and what the caller should do (D-74).

**Rate and retries.** Requests share a per-instance token bucket and a
concurrency cap, and GitLab's known application limits, such as notes,
have buckets of their own (D-75). A read retries a transient failure. A
POST is retried only when GitLab turned it away with 429, and a create
never otherwise (D-35). Every request is bounded by progress timeouts
and a body cap (D-76).

**Logging.** `slog` to stderr, as text or JSON. A line names the
method, the tool, the outcome, the milliseconds, the rate bucket and a
truncated id, and nothing from GitLab. Transport errors are stripped of
path, query and host before they are logged, since a search term and a
project path travel in the URL (D-05).

**Scopes.** Read-only mode asks for `read_api`, and every other mode
for `api`. GitLab offers nothing between them for these writes, so
registration is the control (D-21, D-30). A changed mode needs a new
`login`, and startup says so (D-31). The application's settings are in
[setup.md](setup.md).

**Testing.** The unit suite runs against `internal/gapi/gitlabtest`,
which encodes what GitLab was seen to do (D-07). A fuzz target holds
that an escaped body never yields a command, and table tests hold the
guard to GitLab's extractor (D-18, D-19). Table tests over generated
diffs hold the inline positions (D-53), and golden files hold the
renderers. `TestLogsNeverCarryThePayload` drives every tool with
canaries (D-05). `make live` drives the shipped binary against
gitlab.com and is the only check that can show a belief wrong, and
`make live-cover` fails on a tool option no live step drives (D-82,
D-83). Evals score planted instructions (D-85). How to run a live check
is in [CONTRIBUTING.md](../CONTRIBUTING.md#the-live-run).

**Documentation.** A decision lives once, in part 2 here. What was
verified about GitLab lives in the comment at the line that depends on
it: the behavior, the source link, where GitLab's documentation is
wrong or silent, and the live step that checks it. A refuted belief
goes at that line or in a decision's `Rejected:` line. Every API
operation's verdict is in `testdata/api-coverage.tsv`, and every
field's in `testdata/api-fields.tsv` (D-10). Why REST cannot do a
GraphQL operation is in the doc comment of the function that sends it
(D-09). The docs gate holds every D-ID citation to an entry, every
"Held by" cell to a gate or test that exists, and the codemap to the
tree. The staleness gate holds the README,
[configuration.md](configuration.md) and [setup.md](setup.md) to the
code.

## Decisions

| ID | Decision | Status | Held by |
| --- | --- | --- | --- |
| D-01 | One stdio binary per person, signed in with their own OAuth application | accepted | smoke, TestServeSignedOutEndsCleanlyOnAClosedStdin, review |
| D-02 | The server talks to gitlab.com only, and its one test override is loopback | accepted | TestNoInstanceSetting, TestTestInstanceIsLoopbackOnly, TestTheTestInstanceIsLoggedAtWarn, TestDoctorRefusesATestInstanceThatIsNotLoopback, staleness, mcpb |
| D-03 | A token goes only to the instance that issued it | accepted | TestATokenStaysWithTheInstanceThatIssuedIt, TestTokenNeverLeavesTheInstance, TestAnEnvironmentTokenReachesTheTestInstance |
| D-04 | Stdout carries only MCP JSON-RPC frames | accepted | forbidigo, smoke, TestSessionReportsStrayStdout |
| D-05 | Logs record the call, never the payload | accepted | TestLogsNeverCarryThePayload, TestTransportErrorsCarryNoPathOrQuery, TestTransportErrorsCarryNoHostName, TestUnclassifiedErrorsCarryNoURL, TestLoggerOnlyAttachedAtDebug, TestSettingsNeverPrintTheHostTheApplicationOrTheToken |
| D-06 | Nothing in the repository identifies a real instance, account or person | accepted | leaks, secrets, leaks-history, TestLeaksAllowListHasReasons, TestLeaksFind, TestLeaksTree, TestLeaksHistory |
| D-07 | Fixtures are generated, and the in-memory GitLab models what GitLab was seen to do | accepted | TestGeneratedDataIsSynthetic, TestQuickActions, TestErrorShapes, TestOffsetHeadersAndTheirEdges, TestMovedProject, TestInstancesMintTheirOwnTokens |
| D-08 | Own wire types over REST, and no code imported from another project | accepted | depguard, api-fields, TestNoTokenFields |
| D-09 | GraphQL is used only where REST has no route, one literal per operation through the REST client's path | accepted | api-coverage, TestGraphQLOperationsReadsMutateCalls, TestRerequestReviewMapsGraphQLErrors |
| D-10 | Every published API operation and field has a written verdict | accepted | api-coverage, api-fields, TestTheRealRecordHolds, TestTheCommittedSnapshotIsComplete, TestAPIDiffLeavesTheSnapshotOnANetworkFailure, TestTheCommittedFieldRecordParses |
| D-11 | The server works inside projects and stops at administration | accepted | api-coverage, review |
| D-12 | A convention is verified against GitLab's source, a specification or a live probe before it is adopted | accepted | review, live |
| D-13 | Content is marked as data inside a per-call boundary that names its origin and author | accepted | TestBoundaryCannotBeClosed, TestNewBoundaryIsFreshPerCall, TestBoundaryCannotBeClosedByContent, evals-check |
| D-14 | Hidden text is removed from Markdown and made visible in code, and counted | accepted | TestMarkdownRemovesHiddenText, TestCodeMakesHiddenTextVisible, TestInvisibleNamesWhatAReaderCannotSee, TestFieldsCannotStartALine, TestDescriptionBudgetAndHiddenText, TestCommitCountsHiddenTextOnce |
| D-15 | Nothing content references is fetched | accepted | TestLinks, review |
| D-16 | Job logs, artifacts and test output are masked for token and key shapes | accepted | TestLog, TestTokensEveryPrefix, TestMaskSecrets, TestGetJobLogMasksAKeyOpenedEarlier, TestGetJobLogMasksAnIndentedKeyOpenedEarlier, TestGetTestReportMasksAndBoundsEveryField, TestJobArtifacts |
| D-17 | The server's own text never relays content | accepted | evals-check, review |
| D-18 | Every body passes one quick-action guard, which refuses by default | accepted | bodies, TestFind, TestCheck, TestTheGuardRefusesAQuickActionBeforeTheHandler, TestTheGuardedInputsAreDeclared, TestA202FromANoteIsADefect, TestPublishingADraftWithAQuickActionIsRefused, TestSubmitReviewRefusesDraftsWithQuickActions, live |
| D-19 | `escape_commands` escapes a command line, and nothing opts in to run one | accepted | TestEscape, FuzzEscapeNeverYieldsCommand, TestTheGuardEscapesWhenAsked, TestUpdateCommentEscapesAQuickAction, live |
| D-20 | Every tool goes through one `register`, whose kind decides its annotations, gating and reply | accepted | TestRegisterRefusesAWriteMissingARule, TestAnnotationsAndMeta, TestFullSurfaceRegistersEverything, TestGate |
| D-21 | Ship and Destructive tools are registered only under their flags | accepted | TestShipAndDestructiveAreRegisteredOnlyByTheirFlags, TestGate, TestEveryKindUnderEveryMode, TestInstructionsNameOnlyRegisteredTools, smoke |
| D-22 | Ship is where a persuaded call becomes an effect | accepted | TestShipAndDestructiveAreRegisteredOnlyByTheirFlags, TestAnApprovingReviewWithShip, review |
| D-23 | A pipeline a Write starts stays Write | accepted | review |
| D-24 | Only a write that adds and starts nothing is annotated not destructive | accepted | TestOnlyToolsThatAddSayTheyAreNotDestructive, TestAnnotationsAndMeta |
| D-25 | `openWorldHint` is false only for a write whose effect only the account sees | accepted | TestOwnOnlyWritesAreNotOpenWorld |
| D-26 | Per-call approval is the client's, and the server asks the person nothing | accepted | TestRemovedRequirePrompt, TestLogsNeverCarryThePayload, review |
| D-27 | `requiresUserInteraction` marks only the writes that reach past one item and cannot be undone | accepted | TestOnlyWritesThatReachBeyondTheItemPrompt, TestAnnotationsAndMeta |
| D-28 | Every Destructive call takes `confirm: true` | accepted | TestDestructiveNeedsConfirm |
| D-29 | Every write takes `dry_run`, and a dry run cannot write | accepted | TestDryRunRefusesEveryWrite, TestDryRunsSendNothing, TestDryRunReachesTheClient, TestADryRunLetsAReadOnlyPostThrough, outcomes |
| D-30 | Read-only mode asks for `read_api` and beats every other setting | accepted | TestReadOnlyWithAnEnableFlagNamesBoth, TestAReadAPITokenCannotServeWrites, TestForMode, TestEveryKindUnderEveryMode, smoke |
| D-31 | A changed scope needs a new login, and startup says so | accepted | TestStatusNamesAScopeTheLastLoginLacks, TestServeRefusesATokenThatCannotServeTheMode, TestMissing, TestSatisfied |
| D-32 | Toolsets hold what lies outside a project's issues, merge requests, repository and CI, off by default | accepted | TestGate, TestToolsetsAll, TestSurfaceCounts |
| D-33 | No write creates or moves a protected branch or tag; code reaches one through a merge request | accepted | TestCodeReachesMainThroughAMergeRequest, TestApplySuggestionsRefusesAProtectedSourceBranch, TestProtectedMatchIsGitLabsWildcard, TestCherryPickAndRevert, TestRebaseMergeRequest, TestTagWrites, TestDeleteBranch |
| D-34 | A create is never repeated, and an ambiguous outcome is settled by reading | accepted | TestServerErrorsRetriedForGetNeverForCreate, TestCanceledCreateIsAmbiguous, TestRepeatedDeleteAnswering404IsAmbiguous, TestALostCreateIsSettledByReading, TestALostTodoIsSettledNotSent, TestALostUploadIsNotSentAgain, TestRunPipelineWhoseAnswerWasLostIsNotRepeated, TestDeleteWhoseAnswerWasLostIsReportedDeleted, live |
| D-35 | Retries follow the HTTP method, and a 429 is retried for any method | accepted | TestServerErrorsRetriedForGetNeverForCreate, TestRateLimitedPostIsRetried, TestRateLimited429IsRetriedHonoringRetryAfter, TestBackoffIsJitteredWithRetryAfterFloor, TestRetryAfterPastDeadlineIsNotSlept, TestOnceContradictionsAreRefused, TestCreateRepeatedOnlyAfterAFailedDial |
| D-36 | A write carries a witness, and is refused `[stale]` when it moved | accepted | TestMergeAfterAPushIsStale, TestUpdateIssue, TestDeleteCommentEditedAfterTheReadIsStale, TestDeleteSnippetChangedAfterTheReadIsStale, TestARepeatedTrackTimeIsUnchangedNotStale, TestUpdateReviewComment, descriptions, live |
| D-37 | Omitted means unchanged, and lists change by add and remove | accepted | TestUpdateIssue, review |
| D-38 | `GITLAB_MCP_WRITE_NAMESPACES` confines every write, and unset means everywhere | accepted | TestTheWriteAllowList, TestTodosAreHeldToTheAllowList, TestSubscriptionsAndNewTodosAreHeldToTheAllowList, TestReactionsAreHeldToTheAllowList, TestSnippetWritesHoldToTheAllowList, TestUploadFileIsHeldToTheAllowList, TestApplySuggestionsHoldsTheForkToTheAllowList, TestDoctorSaysWhereWritesAreConfined |
| D-39 | Snippets are created and written only when private | accepted | TestCreateSnippetIsPrivate, TestUpdateSnippetWritesOnlyPrivateSnippets |
| D-40 | Making a confidential issue public is its own Destructive tool | accepted | TestOnlyMakeIssuePublicMakesAnIssuePublic, TestUpdateIssueNeverSendsPublicForAPublicIssue |
| D-41 | `move_issue` never moves an issue where more people can see it | accepted | TestMoveIssue |
| D-42 | Results say what changed, read back, item by item | accepted | outcomes, TestTheRealServiceStatesNoOutcomeFromTheRequest, TestMarkTodosDone |
| D-43 | A comment, a draft or a snippet is edited or deleted only by its author | accepted | TestUpdateComment, TestReviewCommentsAreYourOwnDrafts, TestSnippetWritesAreYourOwn, TestDeleteComment |
| D-44 | `upload_file` reads only from `GITLAB_MCP_UPLOAD_DIRS`, through `os.Root`, after the project is checked | accepted | TestUploadFileReadsOnlyFromTheSettingsDirectories, TestUploadFileIsHeldToTheAllowList, TestUploadFileSendsNothingUntilItMay, TestRefusals, TestALinkThatStaysInsideIsFollowed, TestAnAllowedDirectoryThatIsALinkWorks, TestTheOpenFileIsTheOneChecked, TestUploadDirsRefuseWhatHoldsEverything, TestUploadDirsRefuseHomeInAnotherCase |
| D-45 | `upload_file` sends only an image, at most 10 MiB, named for its bytes | accepted | TestReadsAnImageAndNamesItByItsType, TestTheSizeCap, TestAnUploadIsNamedForItsBytes, TestRefusals |
| D-46 | `apply_suggestions` carries each suggestion's text as its witness | accepted | TestApplySuggestionsHoldsTheText, TestSuggestionsWithHiddenCharacters, TestApplySuggestionsRefusedBeforeSending, TestApplySuggestionsWhoseAnswerWasLost |
| D-47 | A merge request pipeline for a fork is refused | accepted | TestRunMergeRequestPipelineRefusals |
| D-48 | `lint_ci` refuses supplied content that holds an `include:` | accepted | TestSuppliedLintContentMayNotInclude, TestLintSuppliedContent |
| D-49 | Release asset links point only into the release's own project | accepted | TestCreateReleaseWithAssetLinks |
| D-50 | Variable values are sent and never shown | accepted | TestRunPipeline, TestRetryAndPlayJobWithInputs |
| D-51 | `rerequest_review` refuses a reviewer who approved or has not reviewed | accepted | TestRerequestReviewRefuses, TestALostRerequestIsSettledByReading |
| D-52 | Reviews are drafts, published together or one at a time | accepted | TestAReviewIsDraftedThenSubmitted, TestPublishReviewComment, TestALostPublishIsSettledByReading |
| D-53 | The server computes where an inline comment lands | accepted | TestComputePlacesEachKindOfLine, TestComputeAgreesWithTheAlignmentOnGeneratedDiffs, TestAnInlineCommentLandsWhereAsked, live |
| D-54 | Every read has a budget and names what it left out | accepted | TestDiscussionsOverTheBudget, TestCommitDiffBudget, TestCommitMessageBudget, TestDescriptionBudgetAndHiddenText, TestGetMRDiffContinuesACutDiff, TestGetProjectDescriptionOffset, TestListBoardsBudget |
| D-55 | A listing pages by an opaque token bound to its query and says whether it is complete | accepted | TestPageTokenBoundToItsQuery, TestTotalUnknownPastTheLimit, TestOffsetCapIs405AndKeysetWalksPastIt, TestLinkNext, TestSearchProjectsPagesToCompletion |
| D-56 | `get_job_log` reads a byte window, and only what its edges need | accepted | TestGetJobLogTail, TestGetJobLogReadsOnlyTheWindow, TestGetJobLogOfAShortLogIsOneRead, TestGetJobLogFailedOnlyInALongLog, live |
| D-57 | `compare_mr_versions` compares the versions' heads straight, in the target project | accepted | TestCompareMRVersions, TestCompareMRVersionsAfterRebase, TestCompareMRVersionsContinuesAgainstTheSameVersion |
| D-58 | `list_item_events` merges GitLab's event lists into one bounded history, newest first | accepted | TestListItemEvents, TestListItemEventsCursor, TestListItemEventsPastTheReadLimit, TestListItemEventsWithoutAPageCount, TestListItemEventsDedupes |
| D-59 | `list_boards` gives each list the `search_issues` arguments that return its issues | accepted | TestListBoards, TestBoardListSearchTitles, TestListBoardsPastTheReadCap |
| D-60 | `get_test_report` reads the whole report and says when it may be partial | accepted | TestGetTestReport, TestGetTestReportRunningAndEmpty, TestGetTestReportTooLarge |
| D-61 | `get_project` never decodes a secret field | accepted | TestGetProjectNeverCarriesSecretFields, TestNoTokenFields, TestProjectPathEscapedOnceAndSecretsNeverDecoded, api-fields |
| D-62 | Every path segment is escaped once, and a project resolves once per call | accepted | TestFillPath, TestDotSegmentsRefusedBeforeSending, TestResolveProjectOncePerCall, TestGetFileEscapesPathAndRef, TestProjectPathEscapedOnceAndSecretsNeverDecoded |
| D-63 | An issue or merge request is a project plus its `iid` | accepted | TestResolveURL, TestDescriptionsFollowTheHouseStyle, review |
| D-64 | Labels resolve by exact name and are never created on assignment | accepted | TestCreateIssue, review |
| D-65 | No redirect is followed and no URL from a response is called, but a same-origin next page or moved project | accepted | TestRedirectsElsewhereAreNeverFollowed, TestNextLinkOffInstanceIsNotFollowed, TestMovedProjectFollowedOnceForGet, TestMovedProjectKeepsTheQuery, TestMovedToKeepsTheRequestButTheProject |
| D-66 | Sign-in is the person's own public OAuth application over a loopback redirect with PKCE | accepted | TestLoginRunsPKCEOnALoopbackPortAndReportsTheGrant, TestLoginAcceptsAnyLoopbackPort, TestTheVerifierIs64CharactersAndTheChallengeIsS256, TestLoginIgnoresACallbackWithoutItsState, TestLoginToAConfidentialApplicationSaysToUntickIt, TestRedirectURIHasNoPort, TestSetupBlock, staleness, live |
| D-67 | A refresh token is spent once, under a cross-process lock | accepted | TestTwoProcessesRefreshOnceAndShareTheResult, TestInvalidGrantRereadsTheStoreBeforeGivingUp, TestAStoreUnreadableUnderTheLockSpendsNothing, TestACanceledCallDoesNotLoseTheRotatedPair, TestCloseWaitsForARefreshAndRefusesTheNext, TestTheLockWaitIsBounded, live |
| D-68 | A refused access token is dropped, and a read is sent once more | accepted | TestARefusedTokenIsDroppedOnce, TestARejectedTokenIsDroppedAndTheStoreReread, TestARejectedCreateIsNotRepeated |
| D-69 | Startup reads the token's scopes and never refreshes | accepted | TestProbeNeverRefreshes, TestProbeReadsTheScopes, TestProbeRefreshesWhenNoLoginRecordedTheScopes, TestProbeIgnoresTheLoginsScopesForAnEnvironmentToken |
| D-70 | The server starts and serves signed out, and errors surface per call | accepted | TestSignedOutServerAnswersAuth, TestResolveWithNoProfileStartsSignedOut, TestAnUnreachableInstanceStillGetsAServer, TestDumpSchemasNeedsNoConfigurationOrCredentials, TestServeEndsCleanlyWhenTheClientGoes, smoke |
| D-71 | Credentials resolve environment, then keyring, then an owner-only file | accepted | TestEnvironmentWins, TestFileFallbackWarnsEveryTime, TestFileIsOwnerOnly, TestKeyringSaveDropsAStalePlaintextCopy, TestFailedKeyringSaveDoesNotLeaveAnOlderPairInCharge, TestRestrictToOwnerSetsAProtectedACL, TestDecoyKeyringIsInPlace |
| D-72 | `logout` revokes at GitLab and names the profiles that share the application | accepted | TestLogoutRevokesAndNamesProfilesOnTheSameApplication, TestLogoutWaitsForARefreshInProgress |
| D-73 | Configuration is `GITLAB_MCP_*`, read through one reader and validated once | accepted | TestEveryReadOfAVariableGoesThroughLookup, TestASettingOfSpacesIsUnset, TestEveryVariableTheCodeNamesIsInTheRegistry, TestInvalidValuesReportedTogether, TestRemovedRequirePrompt, staleness |
| D-74 | Every error is `[class] message` from a closed vocabulary | accepted | classes, TestClassesAreTheClosedThirteen, TestErrorText, TestTheRealVocabularyHolds, TestErrors |
| D-75 | Requests are paced by a per-instance rate model, with buckets for GitLab's known application limits | accepted | TestRateModelLowersOnHeaders, TestConcurrencyCap, TestNotesBucketIsSeparate |
| D-76 | Every request is bounded by progress timeouts and a 32 MiB body cap | accepted | TestHeaderTimeout, TestStalledBodyIsCut, TestBodyCap, TestAWriteIsTimedByItsProgress, TestABodyCanBeReplayed, TestHTTPTimeoutBounds |
| D-77 | A reply carries `structuredContent` and a different text rendering | accepted | TestBothHalvesDiffer, TestEveryOutputHasASchema |
| D-78 | The released tool surface is a contract | accepted | schema-diff, schema-ack, TestUndeclaredBreaks, TestSchemaDiffAgainstTheLastTagInAWorktree, TestSchemaAckNeedsEveryItemNamed, TestABreakIsABreakOfARelease |
| D-79 | Input schemas stay flat, and arguments are decoded leniently | accepted | TestLenient, TestLenientArguments, TestEnumErrorsNameTheSetSorted, review |
| D-80 | Every tool and input is described, and a witness says it is not a retry signal | accepted | descriptions, TestDescriptionsFollowTheHouseStyle, TestTheDescriptionsGateFailsEveryWay |
| D-81 | Resources carry the same text as their tools | accepted | TestResourcesCarryTheToolsText, TestResourceErrors, smoke |
| D-82 | Green gates are not done: a live run drives every tool option, and a person reads its transcript | accepted | live, live-cover, TestLiveCoverFailsEveryWay |
| D-83 | The live driver reads only what it wrote, in scratch projects it makes and deletes | accepted | TestEveryStepStaysInTheScratchProject, TestConfinedRefusesAStepOutside, TestWritesNameOnlyTheRunsAccount |
| D-84 | The drivers print only through one redacting writer | accepted | transcript, TestTranscriptRefuses, TestTranscriptCoversTheDrivers, TestPrinterMasksEveryLine, TestIdsAResultCarriesAreMasked |
| D-85 | Evals score planted instructions against the in-memory GitLab, fail-closed | accepted | evals-check, TestTheFenceIsClosedAtTheSource, TestReadEventFailsAToolOutsideTheFence, TestScorers |
| D-86 | Every gate asserts a floor on how much it read | accepted | TestTheClassesFloorsFire, TestSmokeFloorFires, TestPinsFloors, TestTranscriptFloorOnFiles, TestFloor |
| D-87 | Every check is Go in one registry, and `make check` and CI run the same set | accepted | parity, checklist, TestEveryCommandDeclaresAMode, TestParityRefuses, TestChecklist, TestCoverpkgIsTheMakefiles |
| D-88 | Every tool runs at one exact pinned version through `go run` | accepted | pins, TestPinsRefuses, TestPinsPassesWhenTheyAgree |
| D-89 | A release is reproducible, signed and attested, from a tag whose CI is green | accepted | release, goreleaser-check, actionlint, TestReleaseRefuses, review |
| D-90 | The registry entry is published from the signed checksums, never for a prerelease | accepted | server-json, TestServerJSONPublish, TestServerJSONRules |
| D-91 | The bundle asks for every setting, with defaults that start the server as no settings do | accepted | mcpb, TestMcpbUserConfigIsDerivedFromTheSettings, TestMcpbFieldDiffNamesTheFieldsThatDiffer, TestMcpbRefusedCombinationsAreDeclared, TestMcpbACredentialFieldIsSensitive |
| D-92 | The bundle manifest is held to its vendored schema, read as every host reads it | accepted | mcpb, TestMcpbRefusesARepeatedKey, TestMcpbRefusesEachBreak, TestMcpbArgsAreReadAsAList, TestMcpbStampHoldsTheStampedManifestToTheSchema, TestVendorSchemasLoadAndCarryTheirFloor |
| D-93 | The CHANGELOG section is the release note | accepted | changelog, changelog-links, TestReleaseNotes, TestChangelogPR |
| D-94 | A tool sends a raw REST or GraphQL request | rejected | — |
| D-95 | Tools read or write CI variables and secure files | rejected | — |
| D-96 | Deleting projects or groups, force pushes and protected-branch changes sit behind a flag | rejected | — |
| D-97 | Events arrive by webhook | rejected | — |
| D-98 | The server serves self-managed instances | rejected | — |
| D-99 | Content from authors below Developer in public projects is withheld | rejected | — |
| D-100 | A client id ships in the binary | rejected | — |
| D-101 | Personal access tokens or the device flow sign the person in | rejected | — |
| D-102 | The server confirms a write by asking the person through elicitation | rejected | — |

### D-01 One stdio binary per person, signed in with their own OAuth application

The server is one Go binary that runs as a subprocess of the MCP client
and speaks MCP over stdio. It holds one person's OAuth tokens and calls
gitlab.com as that person. Nothing is hosted, and there is no shared
deployment.

Each person registers their own OAuth application on gitlab.com, under
their user settings or a group's, and signs in with
`gitlab-mcp login --client-id`. Nothing deployer-specific is built into
the code, the repository or a release, so a copy of the binary carries
no one's application or token
([OAuth provider](https://docs.gitlab.com/integration/oauth_provider/)).

The reason is reach. A GitLab account can read an organization's code
and other people's words, and an `api` token can merge, approve and
delete. Per-person sign-in keeps every call inside that person's own
access, and keeps their token in their own keyring rather than in a
service someone else runs.

Rejected: a hosted or HTTP transport, which this repository has nothing
to run.
Cost: each person registers an OAuth application on gitlab.com once,
with Confidential unticked, before the first `login`.
[setup.md](setup.md) says exactly what to enter.

### D-02 The server talks to gitlab.com only, and its one test override is loopback

The instance is `https://gitlab.com`, fixed in the code. No setting
names another. Proxies come from `HTTPS_PROXY` and `NO_PROXY`, and trust
from the system's certificate authorities.

One development override exists, `GITLAB_MCP_TEST_INSTANCE`, with no
flag. The tests, the evals and the smoke gate point the binary at the
in-memory GitLab with it. It is refused at startup unless its host is
loopback (`127.0.0.1`, `::1` or `localhost`), so it can never carry a
token to another real host, and it is logged at warn when set. Plain
`http` is accepted for loopback alone. The staleness gate keeps it out
of [configuration.md](configuration.md), and the `mcpb` gate out of the
bundle.

The reason is one place to be right. gitlab.com runs the newest
release, so no tool is gated by version, and nothing needs a private
CA, a plain-`http` permission or a base URL to normalize.

Rejected: self-managed instances, with an instance setting, a private
CA, plain `http` and version gating (D-98).
Cost: the version and edition are information only. `/api/v4/metadata`
gives no plan on gitlab.com, where its `plan` field is null because
plans belong to namespaces
([metadata](https://docs.gitlab.com/api/metadata/)). So a route the
account's tier lacks answers `[unsupported]`, and the tier is never
read in advance.

### D-03 A token goes only to the instance that issued it

A profile records the instance it signed in to: gitlab.com, or the test
instance in development. Its token is sent nowhere else. A profile
signed in to the test instance never sends its token to gitlab.com, and
a gitlab.com profile never sends its token to the test instance.

When the instance in use is not the one the profile records, the
token is withheld before any request is built. Every call answers
`[auth]`, naming the mismatch and saying to log in again. A token from
`GITLAB_MCP_REFRESH_TOKEN` has no profile record, and goes to the
instance the same environment names.

The reason is the override. `GITLAB_MCP_TEST_INSTANCE` points the same
binary, the same profile directory and the same keyring at another
host (D-02). Without the record, setting the override in one shell
would hand a real gitlab.com token to whatever listens on that loopback
port, and a test token would be sent to gitlab.com.

Rejected: a token keyed by profile alone, which would follow whatever
base URL the process was started with.
Cost: a profile signed in to the other instance answers `[auth]` on
every call until `login` runs again for the instance in use.

### D-04 Stdout carries only MCP JSON-RPC frames

MCP's stdio transport says the server "MUST NOT write anything to its
`stdout` that is not a valid MCP message", and "MAY write UTF-8 strings
to its standard error (`stderr`) for logging purposes"
([transports](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)).
A stray print corrupts the JSON-RPC stream, and the client silently
stops working.

So logs go to stderr through `slog`. `main` names the process's streams
once and passes them down as `io.Writer`. forbidigo refuses `fmt.Print*`
and `os.Stdout` everywhere else, with `cmd/` and `scripts/` excluded.
Its rules carry their own messages, so a settings block that fails to
load shows up as forbidigo's default message. The smoke gate drives the
shipped binary over stdio and fails on any line that is not a frame.
`TestSessionReportsStrayStdout` holds the smoke gate's own detection.
`--dump-schemas` writes JSON to stdout because it does not serve.

Rejected: a print to stdout anywhere but `main`, even behind a debug
setting, since one stray line breaks the stream at any level.
Cost: only `main` names the process's streams; everything else takes an
`io.Writer`.

### D-05 Logs record the call, never the payload

A call line carries the method, the tool, the outcome, the
milliseconds, the rate bucket and an id truncated to six characters. It
never carries a host, a namespace or project path, a branch, a file
path, a title, a body, a username, an email or a search term.

A GitLab account reaches an organization's code and its people's
words. A log somebody pastes into a bug report has to be safe by
construction rather than by their care.

The subtle leak is the request URL. A project path, a branch and a
search term all travel in it, and a transport failure renders the whole
URL. So `gapi` and `auth` strip path, query and host names from
transport errors before anything logs them. The SDK's own logger runs
only at debug. `Settings` redacts through both `LogValue` and `String`,
and `status`, `doctor` and errors print through the redactor.

`TestLogsNeverCarryThePayload` drives every registered tool with
canaries in every string argument, at debug, and fails if one appears
in the log or if the log is empty.

Rejected: `LogValue` alone on the settings, since `%+v` reads
unexported fields and prints the token.
Cost: a transport error's text is replaced with the kind of failure it
was, and a failure is diagnosed from the method, the tool, the outcome
and the class alone.

### D-06 Nothing in the repository identifies a real instance, account or person

The repository is public, and the server is built by running it against
a real account. Nothing deployer-specific lands here: no hosts,
namespaces, project paths, usernames, emails, numeric ids, OAuth
application ids, tokens, or text from real issues, merge requests,
comments, code or job logs. That holds for code, docs, fixtures,
goldens, transcripts, commit and tag messages, pull requests and logs.

A GitLab instance mixes three payloads: other people's words, an
organization's code, and CI output that can carry secrets. Any of them
in a public repository is a leak nobody can take back.

The leak gate is an allow-list anchored on shapes the server's generated
fields cannot take: an `@` before a dotted domain outside the example
and invalid set, an `https://` host outside that set, any GitLab token
prefix, `gitlab.com/` followed by a namespace outside the fixture set,
and the OAuth shapes. It decodes base64url blobs, treats a tracked
binary as a finding and prints findings redacted. `make leaks-history`
reads every blob, commit message and tag. gitleaks covers credentials
through `make secrets`, with a rule for every GitLab token prefix.

Rejected: allow-listing by path, or an inline `gitleaks:allow`, either
of which exempts whatever lands there next.
Cost: the exemption list is asserted, so each entry carries a reason
and is an argued decision.

### D-07 Fixtures are generated, and the in-memory GitLab models what GitLab was seen to do

`internal/gapi/gitlabtest` is an in-memory GitLab served at the REST
paths the client calls. Its instance is built from code: users from a
fixed synthetic list, groups and projects under `example`-prefixed
namespaces, hosts at `gitlab.example.com` and `.invalid`, bodies and
diffs from templates, and ids from counters in a fixed range. A fixture
copied from a live response is itself the leak, whatever a scanner says
about it.

The fake behaves as GitLab was seen to behave where the server's logic
depends on it. Quick actions run from note bodies and descriptions, so
a missing guard fails a test. A stale `last_commit_id` answers 400 and
a moved head 409. A private project answers 404, and an unknown route
answers the catch-all shape. Offset and keyset paging carry their
10,000 and 50,000 edges. Refresh tokens rotate at once. A 429 comes in
both body shapes. A moved project's GET redirects to its numeric id.
Each world mints its own tokens, so two runs cannot share a sign-in.

Rejected: a fixture copied from a live response.
Cost: a wrong fake agrees with the code written from it. Only a live
run catches the shared mistake (D-82).

### D-08 Own wire types over REST, and no code imported from another project

`internal/gapi` calls GitLab's REST API v4 directly, and
`internal/gitlab` holds wire types written by hand for the fields the
server uses. Nothing imports a GitLab or GraphQL client library, and
nothing imports another project's server code.

REST is the surface GitLab describes machine-readably, in the OpenAPI
file its CI keeps current
([OpenAPI v3 file](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/doc/api/openapi/openapi_v3.yaml)).
Its `read_api` scope maps onto it exactly, by HTTP method
([REST authentication](https://docs.gitlab.com/api/rest/authentication/)).
A client library's generated types would decide which fields exist,
where this server needs an argued list. Machinery shared with other
servers is ported, verified here and owned here, so a fix elsewhere
arrives as a change someone read.

The client stays small enough to read in full. Each operation it calls
has a verdict, and each field it decodes or sends is held to the
snapshot (D-10). No wire struct carries a field ending `_token`
(D-61). depguard refuses the import of a client library.

Rejected: a GitLab client library; depending on another project's code.
Cost: every field is hand-written and needs a verdict, and a field
GitLab adds stays unread until someone records it. The client reports
such a field as drift rather than failing the call.

### D-09 GraphQL is used only where REST has no route, one literal per operation through the REST client's path

GraphQL is used only for an operation REST has no route for. Each
operation is one string literal in `internal/gapi/graphql.go`, sent
through the same client path as REST: the token and its refresh, the
rate model, no redirect followed, a write never repeated, and the
dry-run context that refuses a write. The function that sends it says
in its doc comment why REST cannot do it.

Re-requesting a review is one. GitLab's REST API has no route for it,
while GraphQL has `mergeRequestReviewerRereview`
([GraphQL reference](https://docs.gitlab.com/api/graphql/reference/)).
The rule keeps what made REST the default: the read-only bypasses
published against another GitLab server came through GraphQL. With one
literal per operation and no tool that takes a query, that bypass
cannot arise.

`api-coverage` reads the root field of every `mutate` call, which must
be a string literal and the first field its query selects. It refuses
a `mutate` call from a function whose doc comment does not mention
REST, and a call to `api/graphql` outside `mutate`.

Rejected: a GraphQL client library; the GraphQL schema as the coverage
source, since GitLab generates it and does not commit it; a record of
the operations apart from the code, which repeats the call site's
reason and drifts from it.
Cost: each reason is written by hand, and the gate checks only that
the comment mentions REST, not that the reason holds. None is a read,
so read-only mode, whose `read_api` token cannot mutate, registers
none.

### D-10 Every published API operation and field has a written verdict

`scripts/gates api-diff` fetches GitLab's OpenAPI v3 file at a named
release tag into `testdata/openapi-v3.snapshot.json`, recording the
source URL, the tag, the SHA-256 and the date. It is never edited by
hand. `testdata/api-coverage.tsv` gives every published operation a
verdict: used, with the client method that calls it; gated; deferred;
or written off with a reason. A prefix write-off carries a reason and a
count of what it covers.

`api-coverage` holds that in three directions, binding each row to its
operation by the verb and path the client's code builds. `api-fields`
holds both sides of every operation a client method calls: each query
parameter and body field sent exists on it, and each response field
decoded exists in its schema or carries a verified omission in
`testdata/api-fields.tsv`.

The file is generated from GitLab's routes and checked current in
GitLab's own CI, so it is more reliable than a reference page
([OpenAPI v3 file](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/doc/api/openapi/openapi_v3.yaml)).
A list held against it turns "the operations used" into an argued list.

Rejected: editing the snapshot by hand; the deprecated OpenAPI v2 file.
Cost: hundreds of operations publish no response schema, so the
omission list is long and itself floored. `api-diff` is manual, so a
new operation waits until the release checklist in
[release.md](release.md) runs it.

### D-11 The server works inside projects and stops at administration

The server works inside projects: finding and reading issues and merge
requests, reviewing a merge request the way a person does, reading and
changing the repository through branches and merge requests, and
reading CI and, when the operator opts in, driving it. It stops at
administration.

Out of scope are instance and group settings, users, runners, project
settings, access tokens, deploy keys and tokens, webhooks, integrations
and audit events. `testdata/api-coverage.tsv` writes each group off with
its reason.

The reason is reach. An assistant persuaded by content can do whatever
its tools allow, and administration turns one persuaded call into a
change for every project and person below it. Inside a project the
person can read what changed and undo most of it, and the guards of
D-33, D-36 and D-38 apply. Credentials and infrastructure have no
assistant task worth that risk.

Rejected: instance and group settings, users, runners, access tokens,
deploy keys, webhooks, integrations and audit events.
Cost: a task that needs any of these, such as adding a member or a
webhook, is done by a person in GitLab's own interface. Premium and
Ultimate planning objects are not covered yet (#94).

### D-12 A convention is verified against GitLab's source, a specification or a live probe before it is adopted

A belief about GitLab is adopted only after it is checked against
GitLab's source at a stated version, the OpenAPI snapshot, a
specification or RFC, or a live probe on gitlab.com. The verdict is
recorded in the comment at the line that depends on it, with the source
linked, where GitLab's documentation is wrong or silent, and the live
step that checks it named.

The reason is that GitLab's reference pages are often wrong or silent
on what this server depends on. GitLab's MCP page says a redirect must
match exactly, but GitLab ignores the port of a loopback IP literal, as
RFC 8252 §7.3 asks. The API reference never says that notes and
descriptions run quick actions
([quick actions](https://docs.gitlab.com/user/project/quick_actions/)).
A stale file witness answers 400, not 409, and a merge the role does
not allow answers 401, not 403. Each is read in the source and checked
live.

Source beats prose, and a live probe beats both, because every fake in
`internal/gapi/gitlabtest` is written from what was believed (D-07).

Rejected: a reference page, or another server's code, as evidence.
Cost: each new behavior waits for a source read or a live run, and a
live probe needs a person, a sign-in and a scratch group.

### D-13 Content is marked as data inside a per-call boundary that names its origin and author

Issue and merge request text, comments, review threads, commit
messages, file contents, wiki pages, snippets, release notes and job
logs are written by people other than the person using the server,
often in public projects anyone can post to. GitLab's own MCP page
tells its users they are "responsible for guarding against prompt
injection"
([GitLab MCP server](https://docs.gitlab.com/user/model_context_protocol/mcp_server/)).

So rendered content sits inside a delimited block naming its origin:
the kind, the project, the issue, merge request or path, and the
author's username. The block's token is drawn per call, so text written
before the call cannot know it, and markers inside the text are
defused. A notice before the first block names the token and says the
text is data. `structuredContent` carries the same text in fields named
`untrusted_*`.

Tests hold that content cannot close a block, and that the token is
fresh per call. The evals score planted instructions against these
blocks (D-85).

Rejected: withholding content from authors below Developer in public
projects (D-99).
Cost: the server cannot stop a model from being persuaded. It can only
make the persuasion visible, keep the dangerous tools off by default
(D-21), and never add its own voice to it (D-17).

### D-14 Hidden text is removed from Markdown and made visible in code, and counted

Zero-width and bidirectional-control characters and HTML comments are
dropped from Markdown before it is shown, and the result says how many
characters were dropped. In code, which is files, diffs, commit
messages and suggestions, the same characters are made visible as
`<U+202E>` and counted instead.

Dropping them from code would silently alter text the caller may edit,
and they are how Trojan Source hides code
([Trojan Source](https://trojansource.codes/), CVE-2021-42574).
Names other people choose and the text prints outside a block, such as
file paths, branches, labels and topics, get the same visible form for
control characters, so a newline in a file name cannot start a line
that reads as the server's. A display name such as a commit author is
folded onto one line.

A suggestion's body as Markdown would hide such an edit, so
`list_discussions` shows each suggestion's exact text apart, and
`apply_suggestions` refuses a suggestion holding an invisible character
(D-46).

Rejected: dropping hidden characters from code, which changes text the
caller may edit and hides a Trojan Source edit.
Cost: a reader of code sees `<U+202E>` and its kin where an editor
would show nothing, and a count beside each result.

### D-15 Nothing content references is fetched

No image, attachment or link in content is followed. Links render as
text with their host shown. The host is read as a browser reads it:
the authority ends at the first `/`, `?`, `#` or `\`, so an `@` after
one of them cannot make an outside link look like gitlab.com's.

A fetch is an action content chose, and hidden-prompt exfiltration
through GitLab content has been demonstrated publicly. A URL carries
whatever was put in it to the host that serves it. Showing the host
lets the reader see where a link goes without the server going there.

The rule reaches the client too. Rendered HTML is never requested, so
GitLab's Markdown renderer adds no images or links of its own, and no
URL a response carries is called (D-65).

Rejected: requesting rendered HTML, which would bring GitLab's image
and link rendering into the result.
Cost: an image in an issue is seen only as its link text and host, and
a reader opens it in GitLab to see it.

### D-16 Job logs, artifacts and test output are masked for token and key shapes

Job logs, job artifacts read as text and test report output are masked
before rendering. Every token shape GitLab documents is replaced:
`glpat-`, `gloas-`, `gldt-`, `glrt-`, `glrtr-`, `glcbt-`, `glptt-`,
`glft-`, `glimt-`, `glagent-`, `glwt-`, `glsoat-`, `glffct-`,
`_gitlab_session=` and the runner registration prefix
([token prefixes](https://docs.gitlab.com/security/tokens/)). Common
cloud key shapes and private key blocks are masked too, and the result
counts the masks.

GitLab's own masking covers only variables marked masked
([CI/CD variables](https://docs.gitlab.com/ci/variables/)), and CI
output prints whatever a script echoes. A window is widened to whole
lines, so no secret straddles its edge. A window that starts inside a
private key block is masked whole: the read looks 256 KB before the
window for a header with no footer after it. A one-line field of a
test report has hidden characters removed before the masks run, so a
zero-width space cannot split a token past them.

Rejected: relying on GitLab's masking; walking back line by line to a
key's header, since CI tools prefix lines in too many ways.
Cost: a custom personal-access-token prefix cannot be matched by shape,
and a `BEGIN` line quoted in an error message can mask the rest of a
window.

### D-17 The server's own text never relays content

The tool descriptions and the server instructions say content is
untrusted. No result phrases what content says as something to do. The
server's own text, its notices, summaries and next steps, is written
from fields the server computed, never from a sentence someone else
wrote.

A model reads the server's text as more trustworthy than the content
inside a block (D-13). A summary that said "the issue asks you to
merge !12" would lend an injected instruction the server's voice, which
is the one thing the boundary exists to prevent. Content reaches the
result only through a block, and the server never paraphrases it.

The evals plant instructions in content and score whether the model
followed them, and the self-check drives a run that obeys the steer
through the real server and holds it to fail (D-85). Review holds the
wording of descriptions and instructions.

Rejected: a summary or next step that repeats what content asks for,
even as a quote outside a block.
Cost: a result never says what a description or comment wants done.
The model reads the block itself and decides with the person.

### D-18 Every body passes one quick-action guard, which refuses by default

GitLab runs quick actions from the bodies the API sends. Creating a
note, or creating or updating an issue's or a merge request's
description, executes every command line in it: `/close`, `/merge`,
`/assign`, `/approve` and the rest. A note that is only commands
answers 202 and saves nothing. No parameter turns this off, and the API
reference does not say so
([quick actions](https://docs.gitlab.com/user/project/quick_actions/);
[`issuable_base_service.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/app/services/issuable_base_service.rb)).
A comment reading `/merge` would merge, whatever the Ship switch says.

So `internal/quickaction` owns every Markdown body on its way out. It
parses the body as GitLab's extractor does, on CommonMark's block
structure, and finds every line GitLab would read as a command, known
name or not. By default the call is `[blocked]`, naming each line, and
nothing is sent. A draft written elsewhere is refused at publishing. A
202 from note creation is a defect of the guard, `[unexpected]`.
`make bodies` derives every string input of every write from the schema
dump and fails on one neither guarded nor listed plain with a reason.

Rejected: a regular expression matching GitLab's extractor, which
cannot see the block structure; matching only the command names GitLab
knows, since an unknown one is harmless only until it is known.
Cost: the guard over-detects on purpose. Any line starting `/word`
counts, and so does a line under either reading of an ambiguous
backtick pairing.

### D-19 `escape_commands` escapes a command line, and nothing opts in to run one

`escape_commands: true` rewrites each command line with a leading
backslash. CommonMark renders the escaped line as the same visible text
([backslash escapes](https://spec.commonmark.org/0.31.2/#backslash-escapes)),
and GitLab's extractor, which matches a line starting `/`, no longer
reads it as a command
([`extractor.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/lib/gitlab/quick_actions/extractor.rb)).
The result says which lines were escaped.

There is no opt-in to execute. Every effect a quick action has, closing,
labeling, assigning, merging, is a field on a tool under that tool's
kind, so the switches and guards apply to it. An opt-in would let a
persuaded call merge through `add_comment` with Ship off.

A fuzz target holds that an escaped body never yields a command, and
that removing the inserted backslashes gives the body back. The live
step "where GitLab runs a quick action from an API body" checks both
halves on gitlab.com.

Rejected: an opt-in that lets a body run its commands.
Cost: where GitLab's backtick pairing differs from CommonMark's, or
inside multi-line raw inline HTML, the backslash shows in the rendered
text, which is the safe direction.

### D-20 Every tool goes through one `register`, whose kind decides its annotations, gating and reply

Every tool is added through one `register`, and its `Kind` decides what
every tool shares: the annotations, whether it is registered at all,
the toolset and scope gating, `_meta`, the quick-action guard on the
inputs it declares, the dry-run context, `confirm`, decoding, errors,
both halves of the reply and the log line. A handler validates its
input, calls the service and returns the model.

Rules kept by hand at every call site are ways to be quietly wrong. A write that forgot its guard, its `dry_run` or its annotation
would pass every test that did not think to look. With one `register`,
a kind is a decision made once, and a test can drive every kind under
every mode.

`register` refuses at start a write missing a rule: no `dry_run`, no
`escape_commands` beside a guarded input, a guarded input that does not
exist or is not a string, a guarded read, or `PromptEveryCall` outside
Destructive. The refusal fails in every configuration, not only the
one with the switch on.

Rejected: annotations, gating and guards set per tool by hand.
Cost: moving a tool between kinds or toolsets changes what it does on
every client, so the project's maintainers decide it, and the schema
diff shows it.

### D-21 Ship and Destructive tools are registered only under their flags

Ship tools are registered only under `GITLAB_MCP_ENABLE_SHIP=true`, and
Destructive tools only under `GITLAB_MCP_ENABLE_DESTRUCTIVE=true`. A tool
that is not registered cannot be called, whatever the model is told.
`GITLAB_MCP_READ_ONLY=true` beats both.

GitLab's scopes cannot separate these writes. `api` "Grants complete
read/write access to the API", and every issue, note, commit and
pipeline write needs it, so the same token can merge, approve, run
pipelines and delete
([scopes](https://docs.gitlab.com/integration/oauth_provider/)).
`read_api` covers only GET and HEAD. So the scope cannot be the control,
and neither can an annotation or a prompt, which a client may skip or a
person may wave through. GitLab's own MCP server filters its tool
listing but not its tool calls, which shows where "listed but callable"
ends.

The server instructions name each switch that is off and what it would
add, so a model asked to merge can say why it cannot. The smoke gate and
the registration tests hold every kind under every mode.

Rejected: registering every tool and refusing per call; the OAuth
scope; an annotation, a prompt or a per-call flag.
Cost: the default `api` token can merge, approve and run pipelines, so
anything else holding it can. Leaving Ship unregistered stops this
server doing so, and the token lives in the keyring for that reason.

### D-22 Ship is where a persuaded call becomes an effect

Ship is the kind where a persuaded call stops being a record someone can
edit and becomes an effect: code in the default branch, a deployment job
run, a sign-off another person's merge rule counts, an issue's content
published in another project. Ship merges, approves and unapproves,
cancels an auto-merge, applies review suggestions, rebases, moves an
issue, runs, retries, plays and cancels CI, and creates a release.

Approving is Ship. An approval is a person's sign-off that other
people's merge rules count
([approvals](https://docs.gitlab.com/api/merge_request_approvals/)),
and an injected "approve this" is exactly what the switch exists to
refuse. A submitted review whose `reviewer_state` is `approved` is
`[blocked]` without the switch, naming it. Rebasing rewrites the source
branch and resets approvals, and takes the head `sha` as its witness.

Cherry-pick and revert stay Write. They commit to a branch as
`create_commit` does, under the same refusal of the default and
protected branches (D-33), so they cannot reach what merging reaches.

Rejected: approving as Write, as the most common review action and
reversible; cherry-pick and revert as Ship, since the protected-branch
guard makes them Write.
Cost: with Ship off, a review that would approve is `[blocked]`, and the
person approves in GitLab.

### D-23 A pipeline a Write starts stays Write

A commit, a new branch, a new merge request or a new tag starts the
pipelines a push starts. These tools stay Write, or Write in the
`releases` toolset for a tag, and are not gated as Ship.

Such a pipeline runs with the account's own permissions and the
project's unprotected variables only. A protected variable "is only
available in pipelines that run on protected branches or protected
tags" ([CI/CD variables](https://docs.gitlab.com/ci/variables/)), and
no Write reaches a protected branch or tag (D-33). The pipeline is the
one the account's own push would run, which is the test suite a merge
request is reviewed by. Gating it would gate every commit.

`docs/security.md` says so, and annotates these tools destructive
because they start something (D-24).

Rejected: appending `[skip ci]` to commits made without Ship, which
would stop the checks a merge request is reviewed by.
Cost: a commit, a new branch or a new merge request starts the push
pipelines with the account's permissions and the project's unprotected
variables. A project whose unprotected variables hold secrets exposes
them to code the model wrote.

### D-24 Only a write that adds and starts nothing is annotated not destructive

The `Adding` kind is a write that only adds and starts nothing: a new
issue, label, milestone or snippet, a to-do, an upload. It is the only
write annotated `destructiveHint: false`. Every other write, including
one a person could edit back, says `destructiveHint: true`.

The MCP schema defines the hint: "If false, the tool performs only
additive updates"
([schema](https://modelcontextprotocol.io/specification/2025-11-25/schema)).
A write that replaces text, removes a label or starts a pipeline is not
additive, whether or not someone can restore it. Clients prompt by this
hint, so a false one would let a client run a change without asking.

A kind rather than a flag keeps it one decision in `register` (D-20).
Several writes that look additive do more. `create_tag`, `create_branch`
and `create_merge_request` can start a pipeline. The link tools can
make one item hold another. `add_review_comment` can resolve a thread
when the review is submitted. Each stays Write.

Rejected: `destructiveHint: false` on any write a person can edit back;
a flag rather than a kind for Adding; Adding for `create_tag`,
`create_branch`, `create_merge_request`, the link tools and
`add_review_comment`.
Cost: claude.ai and Codex prompt for every write that changes or
removes something. VS Code ignores the hint and prompts for every tool
without `readOnlyHint`.

### D-25 `openWorldHint` is false only for a write whose effect only the account sees

`openWorldHint` is true for every write but those whose effect only the
signed-in account sees: `add_todo`, `subscribe`, `mark_todos_done` and
`update_review_comment`. Each is marked `OwnOnly` on its spec in
`register`, and `register` derives the hint from that mark and the
kind.

The MCP schema says the hint is true when "this tool may interact with
an 'open world' of external entities" and false when "the tool's domain
of interaction is closed"
([schema](https://modelcontextprotocol.io/specification/2025-11-25/schema)).
A to-do, a subscription and a draft are the account's own until it
publishes or acts: nobody else sees them. Every other write lands where
other people read it, in a thread, a branch, a pipeline or a project's
labels.

A test holds that only `OwnOnly` writes say false, so a new write is
open-world unless someone argues otherwise.

Rejected: `openWorldHint: false` for any write that touches one item, or
for a write the account could undo.
Cost: reactions are seen by everyone who can see the item, so `react`
keeps `openWorldHint` true, though it adds nothing but an emoji.

### D-26 Per-call approval is the client's, and the server asks the person nothing

The server asks the person nothing. No tool puts a question through MCP
elicitation, and per-call approval is the client's.

The specification puts it there. Clients SHOULD "Prompt for user
confirmation on sensitive operations" and "Show tool inputs to the user
before calling the server"
([tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)).
A server's question is not that prompt. Clients answered such a question
with nobody there, did not draw it, left it hanging, or cut its message
short, and a question on every call is answered without reading. So the
controls are the ones that hold on every client: registration (D-21),
`confirm` (D-28), the guards and witnesses (D-33, D-36, D-38, D-40),
and `dry_run` (D-29). A call's arguments carry what a prompt needs to
show, as `apply_suggestions` carries each suggestion's text (D-46).

The removed `GITLAB_MCP_REQUIRE_PROMPT` is refused at start, since
ignoring it would allow what its owner set it to stop. Every tool test
runs through a client whose elicitation handler fails the test.

Rejected: confirming writes through elicitation (D-102).
Cost: a client that runs with nobody watching makes every registered
write, so Ship and Destructive stay off for one. `upload_file`'s guards
are the directories and the allow-list alone.

### D-27 `requiresUserInteraction` marks only the writes that reach past one item and cannot be undone

`delete_label`, `delete_milestone` and `make_issue_public` carry Claude
Code's `_meta["anthropic/requiresUserInteraction"]: true`, set by
`PromptEveryCall` on their spec. No other tool carries it, and only a
Destructive tool may.

Claude Code shows such a tool's permission prompt on every call, "even
in `acceptEdits`, `auto`, and `bypassPermissions` permission modes", and
"Allow rules that match the tool don't skip the prompt either"
([Claude Code MCP](https://code.claude.com/docs/en/mcp)). That is
right only for a write that reaches past one item and cannot be undone.
A deleted label or milestone is taken off every issue and merge request
that carried it. A confidential issue made public is read by everyone
who can see the project, and what they read cannot be taken back.

For every other tool, the operator decides in Claude Code with
`permissions.allow` and `ask`, and the README gives `ask` rules for the
writes that ship, publish, upload or delete.

Rejected: no tool carrying the mark; the mark on deletes that remove one
item, such as a comment or a branch.
Cost: Claude Code prompts on every such call and denies it where no
prompt can be drawn, as in `-p` or `dontAsk`, so an unattended run
cannot make these writes.

### D-28 Every Destructive call takes `confirm: true`

Every Destructive call takes `confirm: true`. Without it the call is
refused before anything is read or sent, and the refusal names the
argument. The witness from the caller's read is required as well: a
branch's head `sha`, a comment's or a snippet's `updated_at`, a wiki
page's content hash, a tag's commit.

The switch is set once for a whole session, so it says the operator
allows deletes, not that this call means one. `confirm` makes each
delete say so in its own arguments, where a client's prompt shows it.
A delete is then never what a call does by default, the way a tool that
deletes whatever id it is given makes one.

`register` refuses at start a Destructive tool without a `confirm`
input, and refuses each call without `confirm: true`, a dry run aside.
A test holds every Destructive tool to it.

Rejected: a Destructive tool that acts without `confirm` once its switch
is on.
Cost: `confirm` is an argument the model writes, so it is never the
person's approval. The person's approval is the client's prompt
(D-26).

### D-29 Every write takes `dry_run`, and a dry run cannot write

Every write tool, of any kind, takes `dry_run`. A dry run returns what
would be sent, and for a body what the quick-action guard would do,
without writing.

The flag puts the call on a context under which `internal/gapi` refuses
every write: a POST, PUT or DELETE is answered before it reaches the
network. A preview that worked by convention, a branch each handler
must remember, would turn into a write the day one handler forgot. Here
the client holds the line for every tool and every GraphQL mutation
alike (D-09).

A preview may read what it has to show. A dry run reads a merge
request's state, the protected-branch rules, the image for an upload,
or GitLab's own cherry-pick dry run. `lint_ci`'s POST of supplied
content declares itself read-only on its call, so it runs under a dry
run and states no outcome. The `outcomes` gate holds that no write
result claims an outcome from the request alone.

Rejected: a dry run kept by a branch in each handler.
Cost: a dry run makes the reads its preview needs, so it costs rate
budget and can fail as a read fails.

### D-30 Read-only mode asks for `read_api` and beats every other setting

`GITLAB_MCP_READ_ONLY=true` registers only Read tools and asks for
`read_api` at login. It beats every other setting: read-only with either
enable switch is refused at startup, naming both.

GitLab enforces `read_api` by HTTP method, GET and HEAD, which is
exactly the REST surface's reads
([REST authentication](https://docs.gitlab.com/api/rest/authentication/)).
A `read_api` token cannot write whatever a tool tries. Registering the
reads alone makes the server's surface agree with the token's, so a
model is never offered a write the token will refuse. A token whose
granted scopes cannot serve the mode is refused at startup, naming the
scope (D-31), so a `read_api` token never gets a write tool, whatever
the switches say.

No GraphQL operation is a read, so read-only mode registers none of them
(D-09).

Rejected: offering write tools to a `read_api` token, as another GitLab
server did.
Cost: an `api` token from an earlier login keeps working in read-only
mode until `logout` and `login --read-only` replace it. Read-only then
narrows the surface, not the token.

### D-31 A changed scope needs a new login, and startup says so

Startup reads the token's granted scopes and compares them with the
mode's. A token that cannot serve the mode, such as a `read_api` token
with writes on, is refused before the server announces itself, naming
the missing scope. `status` names a scope the last login lacks.

A refresh never adds scope. OAuth forbids a refresh from asking for "any
scope not originally granted by the resource owner"
([RFC 6749 §6](https://www.rfc-editor.org/rfc/rfc6749#section-6)), so
turning read-only off needs a new consent, which only `login` asks for.
Saying so at startup turns a setting change into one clear message
instead of a write refused mid-task.

This is the one fatal outcome at startup (D-70). Every other failure
there is logged and the server starts.

Rejected: failing on the first call, where the refusal arrives as one
tool's error in the middle of a task.
Cost: a person who changes the mode runs `login` again before the
server starts, and a client shows the server as failed until then.

### D-32 Toolsets hold what lies outside a project's issues, merge requests, repository and CI, off by default

Toolsets sit beside kinds. `wiki`, `snippets`, `releases`,
`deployments`, `activity` and `planning` are off by default, and
`GITLAB_MCP_TOOLSETS` adds them by name or with `all`. The default
surface holds the work inside a project's issues, merge requests,
reviews, repository and CI.

The reason is focus. A tool a person never asked for is a choice the
model can make wrongly, and a description it must read on every
request. The toolsets hold what lies outside the daily path. Wiki
pages, snippets and releases publish, deployments and activity read
across infrastructure and people, and planning changes labels and
milestones every issue shares. A read joins the default when it answers
a question a default tool acts on. `list_boards` did, beside
`list_labels` and `list_milestones`, since a board's columns are read
with `search_issues`.

The server instructions name each toolset that is off and what it would
add.

Rejected: holding the default tool count under a client's grouping
point. VS Code groups by the whole request's tools, its own built-ins
included, so no server count avoids it.
Cost: a person who wants wiki pages, snippets or releases sets
`GITLAB_MCP_TOOLSETS`, and the bundle asks for it.

### D-33 No write creates or moves a protected branch or tag; code reaches one through a merge request

No write commits to, creates or moves a protected branch or tag. Code
reaches the default branch and every protected one only through a merge
request a person can read, and merging it is Ship (D-22).

`create_commit`, `cherry_pick_commit` and `revert_commit` refuse the
default branch and every protected one: an existing branch by its own
`protected` flag, a new one by the protected-branch rules read at call
time. `create_branch` refuses a name the rules cover, since a new
protected branch at a ref of the caller's choosing is code in a
protected branch no merge request showed. `apply_suggestions` and
`rebase_merge_request` refuse a default or protected source branch.
`create_tag` refuses a name a protected-tag rule covers, and `delete_tag`
a protected tag.

GitLab's rules take wildcards, such as `release/*`
([protected branches](https://docs.gitlab.com/user/project/repository/branches/protected/)),
and an account allowed to push may push there. The guard does not rely
on the account lacking the role. A branch's own flag reflects wildcard
rules, so an existing branch needs no rule read.

Rejected: a commit straight to a protected branch the account may push
to.
Cost: a new branch is refused when the project has more protected-branch
rules than one read covers, since an unread rule might match it.

### D-34 A create is never repeated, and an ambiguous outcome is settled by reading

A create is never sent twice. A note, an issue, a commit, a pipeline, a
release or a fork retried is a second one, and GitLab's REST API takes
no idempotency key to stop it. A create canceled or timed out after it
may have been written, or a delete repeated after an attempt that may
have landed, is `[ambiguous_outcome]`.

The tool has already done the read that settles it, against the call's
start time and the signed-in account. A comment is sought among the
notes newest first, an issue by author, time and title, a commit as the
branch head, a pipeline by ref, source and time, a release by its tag.
Found means created, and the result carries it. Not found means not
created, and the result says so without creating it. Anything else
stays unknown, and the result says not to repeat the call. Each settle
read finds a fresh create on gitlab.com without waiting.

`add_todo` is a create too: GitLab's 304 for a pending to-do runs before
the insert without a lock, so a to-do marked done in between lets a
second through.

Rejected: telling a lost `run_pipeline` from another run by its
variables, which reads their values (D-50).
Cost: a second run on the same ref inside the settle window reads as
this one. A lost upload stays unknown, since only a Maintainer can list
uploads, and the person decides.

### D-35 Retries follow the HTTP method, and a 429 is retried for any method

Whether a request repeats follows its HTTP method. GET repeats. PUT
repeats, since it sets fields to values. POST does not, except the calls
declared repeatable at their site with the reason: marking a to-do
done, approving and unapproving, cancelling, resolving. A create never
repeats (D-34).

A 429 repeats for any method, because GitLab refused before acting
([rate limits](https://docs.gitlab.com/user/gitlab_com/rate_limits/)).
`Retry-After` is a minimum, the backoff has full jitter, and an attempt
never runs past the call's deadline. A Rack::Attack 429 body is plain
text and an application limit's is JSON, and both are read. A create
whose connection never opened repeats too, since nothing reached GitLab.

The method decides by default and a call site must argue an exception,
so a new POST is safe without anyone remembering. A call declared both
repeatable and sent once is refused as a contradiction.

Rejected: retrying every failure the same way, which repeats a create
that landed.
Cost: at most four attempts, never past the call's deadline, and a
transient failure of a create reaches the caller as
`[ambiguous_outcome]` rather than being tried again.

### D-36 A write carries a witness, and is refused `[stale]` when it moved

A write that could destroy what the caller did not see carries a
witness from the caller's read, and is refused `[stale]` when the target
moved.

Where GitLab offers a guard, it is required. A file update or delete
takes the `last_commit_id` `get_file` returned, which GitLab refuses
with 400
([repository files](https://docs.gitlab.com/api/repository_files/)).
Merge, approve and rebase take the head `sha`, which GitLab refuses with
409 ([merge requests](https://docs.gitlab.com/api/merge_requests/)). A
comment or snippet delete sends `If-Unmodified-Since`, stretched to the
end of the millisecond GitLab shows, which GitLab refuses with 412.

Where GitLab offers none, the server reads, compares and refuses.
Issue, merge request, comment, snippet and milestone updates take
`updated_at`; `track_time` also takes `total_time_spent`, which moves
when `updated_at` does not. A draft takes a hash of its text, a wiki
page a hash of its content, a label a hash of what an update can
change, and a branch delete the head `sha`. REST reads no `If-Match`,
and `lock_version` is accepted only by the web controllers.

Each witness input's description says it is NOT a retry signal (D-80).
The same text asked again reads as unchanged before the witness is
compared, so an edit repeated after a lost answer is not `[stale]`.

Rejected: the `updated_at` a merge request's create answers with, which
moves about a second later; an HTTP-date `If-Unmodified-Since`, cut to
the second, which refuses nearly every delete.
Cost: the window between the re-read and the write is not closed, and
the result does not claim it is.

### D-37 Omitted means unchanged, and lists change by add and remove

An update sends only the fields the caller gave. GitLab's PUT changes
only the parameters sent, so an input left out is left as it is.

Lists change by add and remove. Labels go through GitLab's own
`add_labels` and `remove_labels`
([issues](https://docs.gitlab.com/api/issues/)). Assignees and
reviewers go as a set computed from the server's fresh read, plus and
minus what the caller named. A replacement list typed by the caller
would drop whatever someone added since the caller read, with no
witness to notice. A description replaced whole reports how many
characters and lines it removed.

An empty value cannot clear a field, since `""` means absent (D-79), so
updates that can clear a description, a date or a priority take
`clear_*` inputs.

Rejected: a replacement list the caller typed.
Cost: adding an assignee leaves an issue's `updated_at` in place, so a
concurrent assignee change is not caught by the witness. `update_issue`
computes the set from its own fresh read, so it does not lose one.

### D-38 `GITLAB_MCP_WRITE_NAMESPACES` confines every write, and unset means everywhere

When `GITLAB_MCP_WRITE_NAMESPACES` is set, every write of every kind is
confined to projects under those namespaces. Any other target is
`[blocked]`, naming the setting, before anything is sent. `doctor` and
`get_me` say whether it is set.

The flow that matters is private content written somewhere public. A
model reads a private repository, and then a public issue tells it to
post what it read. The allow-list cuts that flow at the write, whatever
the model was persuaded of.

It reaches every write's projects. `link_issues`, `link_merge_requests`
and `move_issue` hold both projects. `apply_suggestions` holds a fork,
since the commit lands there. `mark_todos_done`, `add_todo`, `subscribe`
and `react` hold the item's project. `upload_file` holds the project
before it reads the local file (D-44). Every write result names the
target project's visibility, so a write to a public project shows as
one.

Rejected: a narrower default, since a default that refuses every write
is one everyone overrides.
Cost: unset, a write can go anywhere the account can write. `doctor`
and `get_me` say so, and [security.md](security.md) advises setting it.

### D-39 Snippets are created and written only when private

`create_snippet` makes only private snippets, and visibility is not an
input. `update_snippet` and `delete_snippet` act only on the account's
own private snippets. An internal or public snippet, even the account's
own, is `[blocked]`.

A snippet carries its own visibility, whatever its project's
([snippets](https://docs.gitlab.com/user/snippets/)). A public or
internal snippet created by a model is how private content leaves, and
writing into one that is already public publishes what is written as
surely as creating it. Neither tool can change the visibility, so a
snippet the server writes stays private.

A change that creates, deletes or moves a snippet's file is a commit.
It is sent once, and a lost answer is settled by reading the snippet's
files and `updated_at` (D-34).

Rejected: visibility as an input of `create_snippet` or
`update_snippet`.
Cost: a personal snippet sits in no namespace, so when
`GITLAB_MCP_WRITE_NAMESPACES` is set, creating, changing or deleting one
is refused.

### D-40 Making a confidential issue public is its own Destructive tool

Making a confidential issue public is its own tool, `make_issue_public`:
Destructive, with `confirm: true`, the issue's `updated_at` as witness,
and `requiresUserInteraction` (D-27). `update_issue` refuses
`confidential: false` on a confidential issue whatever the switches, and
sends nothing for an issue that is already public.

A confidential issue is visible only to the project's members with the
Planner role or above
([confidential issues](https://docs.gitlab.com/user/project/issues/confidential_issues/)).
Making it public shows it to everyone who can see the project's issues,
and what they read cannot be taken back. An injected "it was
confidential by mistake" asks for exactly that. A client's standing
approval of `update_issue` would carry it unseen inside an ordinary
edit, so it is a separate tool a client prompts for by name.

Leaving the field out for a public issue matters too: sending
`confidential: false` would undo a person's change made between the
read and the write.

Rejected: `confidential: false` on `update_issue`; gating it under the
Ship switch.
Cost: it needs `GITLAB_MCP_ENABLE_DESTRUCTIVE`, so an operator who
enables only Ship cannot publish an issue from here.

### D-41 `move_issue` never moves an issue where more people can see it

`move_issue` is Ship. It takes the issue's `updated_at` as witness,
holds both projects to `GITLAB_MCP_WRITE_NAMESPACES`, and refuses a
target whose issues more people can see than the source's. An issue
in a project whose issues are members-only counts as private, and a
project whose audience cannot be read is refused too.

A move copies the issue, its comments included, into another project
([issues](https://docs.gitlab.com/api/issues/)). Into a wider audience,
that publishes everything the issue and its thread hold to people who
could not read it before. An injected "move this to the public tracker"
is how a private issue would reach a public project, and a model has no
way to know which comments were meant to stay in.

A move to a project as private or more private stays possible, since it
narrows who reads the issue.

Rejected: a move as an ordinary write, under Write and without an
audience check.
Cost: both projects must be in the write allow-list when it is set, and
a move into a wider audience is done by a person in GitLab.

### D-42 Results say what changed, read back, item by item

Every write reports the state it produced, read back rather than
assumed: labels before and after, the commit created and the branch it
moved, the discussion a comment joined, the pipeline started and its
status. A write that changed nothing says so, such as "already closed",
rather than reporting success. A write over many items, such as
`mark_todos_done`, reports each item on its own.

GitLab's answers do not say whether a write took effect. Retrying a
pipeline with nothing failed answers with the pipeline, changed or not,
and so does cancelling a finished one. Cancelling an auto-merge as
GitLab begins the merge answers success, and the merge goes on. A
result built from the request would claim effects GitLab never made.

The `outcomes` gate holds that every write result states its outcome on
every branch. An AST check refuses a branch on a boolean request field
that states an outcome without reading the response.

Rejected: reporting success for a write that changed nothing.
Cost: a write is followed by a read, or by GitLab's answer read field by
field, so a result costs more than the request it reports.

### D-43 A comment, a draft or a snippet is edited or deleted only by its author

`update_comment` and `delete_comment` act only on the account's own
comments. The review tools act only on the account's own drafts, and
`update_snippet` and `delete_snippet` only on the account's own
snippets. Anything else is refused before anything is sent.

GitLab lets a Maintainer or Owner "Edit comments posted by other users"
([permissions](https://docs.gitlab.com/user/permissions/)). Under the person's name,
a persuaded call could then rewrite what a colleague said in a review
or remove a comment that disagreed. The server writes as the person,
so it writes only the person's own words.

An edit keeps the comment in its thread, with its replies and its diff
position. It takes the `updated_at` of the caller's read (D-36), and
the same text asked again reads as unchanged. A draft's edit sends its
own position back, since GitLab's PUT clears a position it is not sent.

Rejected: editing or deleting another person's comment where GitLab
lets a Maintainer do it.
Cost: a Maintainer who wants to edit or remove someone else's comment
does it in GitLab.

### D-44 `upload_file` reads only from `GITLAB_MCP_UPLOAD_DIRS`, through `os.Root`, after the project is checked

`upload_file` uploads an image from this machine for Markdown to embed.
It reads only from the directories `GITLAB_MCP_UPLOAD_DIRS` names. Unset,
it is `[blocked]` naming the setting, and a path outside them is
`[blocked]` naming only the setting. Startup refuses a relative
directory, a filesystem root, and the home directory or one holding it.

The project is read and held to the write allow-list first, so a call
aimed elsewhere learns nothing about the path. The file opens through
`os.Root` on the widest allowed directory that holds it, so neither
`..` nor a symbolic link leads out
([`os.Root`](https://pkg.go.dev/os#Root)). A symbolic link as the file,
a directory, a FIFO or a device is refused. The file is checked with
`Lstat`, opened without blocking and checked again, so one swapped in
between is `[conflict]`.

A local file reader is how a comment persuading a model to "attach" a
key would send the key. The upload POST needs only read access to the
project, and a Maintainer can download every upload by id
([Markdown uploads](https://docs.gitlab.com/api/project_markdown_uploads/)),
so a planted comment can aim an upload at a project its author
maintains. The directories are the guard.

Rejected: MCP roots, which SEP-2577 deprecates; content as base64,
which costs a large image hundreds of thousands of tokens; `O_NOFOLLOW`
in place of `Lstat`, open and `os.SameFile`, since `os.Root` follows a
final link anyway.
Cost: the swap check and the non-blocking open have no test, since
staging the race needs a test-only back door.

### D-45 `upload_file` sends only an image, at most 10 MiB, named for its bytes

`upload_file` sends at most 10 MiB, a tenth of gitlab.com's 100 MiB
attachment limit
([GitLab.com settings](https://docs.gitlab.com/user/gitlab_com/)): far
above a screenshot, and small enough to hold in memory and send in one
request. The bytes must sniff as PNG, JPEG, GIF or WebP with
`http.DetectContentType`, the function GitLab's Workhorse uses to set
the type it serves. SVG is text that can carry script, and is refused.

The name sent is the file's base name, every character but ASCII
letters, digits, `.`, `-` and `_` made `_`, with the extension of the
sniffed type in place of its own. GitLab "identifies files only by the
file extension" when it decides whether Markdown embeds an upload and
whether a private project serves it without sign-in
([`file_type_detection.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/lib/gitlab/file_type_detection.rb)).
The extension has to agree with the bytes, or a file could be served as
something other than it is. No directory is sent.

A refusal does not say what the bytes are, so a probe learns nothing
about a file it may not upload.

Rejected: SVG; a streamed body, which a retry would have to rebuild.
Cost: bytes after a valid image header go too, though making such a
file needs write access to this machine. Workhorse removes a JPEG's
metadata, but a PNG's, GIF's or WebP's goes as it is in the file.

### D-46 `apply_suggestions` carries each suggestion's text as its witness

`apply_suggestions` (Ship) commits review suggestions to a merge
request's source branch as the account. Each suggestion comes with its
text as `list_discussions` shows it, and a text that differs from
GitLab's is refused `[stale]` before anything is sent. The match is
exact but for line endings and trailing newlines, since indentation is
code.

The commit is someone else's text, under the person's name. GitLab
finds a suggestion by its id alone, and an edit to a comment gives its
suggestions new ids
([suggestions](https://docs.gitlab.com/api/suggestions/)). Carrying the
text makes the commit what was read, and a client's prompt for the call
shows it (D-26). A suggestion longer than `list_discussions` shows was
never read whole and is refused `[unsupported]`. One holding an
invisible character, a default-ignorable or a variation selector, is
`[blocked]`, naming it (D-14).

Each id is first found among the merge request's own comments. A
default or protected source branch is refused (D-33), and so is a fork
outside the write allow-list. A lost answer is settled by reading each
suggestion's `applied` and the branch head (D-34).

Rejected: asking the person before applying; trimming surrounding white
space, which lets indentation differ.
Cost: the witness is the text put in, not the lines it replaces, so a
client's prompt can show one plausible line for a suggestion that
removes many. `list_discussions` shows the range (#95).

### D-47 A merge request pipeline for a fork is refused

`run_merge_request_pipeline` refuses a merge request from a fork,
`[blocked]`, dry run included, before anything is sent. The refusal
points to the merge request's Pipelines tab.

When a member of the target project triggers it, a fork's merge request
pipeline runs in the target project with the fork's code and "the parent
project's CI/CD settings, resources, and project CI/CD variables".
GitLab warns that "Fork merge requests can contain malicious code that
tries to steal secrets in the parent project", tells the reviewer to
check the changes first, and shows no warning when the pipeline is
triggered through the API
([merge request pipelines](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/)).
Whether the pipeline runs in the target or the fork depends on a
project setting only Maintainers can read, so the server cannot tell
which it would start. Checking the changes is the step a persuaded
model skips.

This is the narrow choice. Allowing forks later, held to the allow-list
in both projects and behind a reviewed flow, is an addition.

Rejected: running a fork's code in the parent project from a tool call.
Cost: the person runs a fork's pipeline from the merge request's
Pipelines tab, where GitLab shows its warning.

### D-48 `lint_ci` refuses supplied content that holds an `include:`

`lint_ci` reads a project's merged CI configuration at a ref by GET.
Supplied content goes by POST, which is refused in read-only mode, and
refused with an `include:` at any depth, found by parsing the YAML.

GitLab fetches what an include names while it lints
([includes](https://docs.gitlab.com/ci/yaml/includes/)). An
`include:remote` in supplied content is a URL the server would make
GitLab fetch, and other include kinds read other projects' files with
the account's access. That is a way out no write control sees: the
allow-list holds projects written to, and a lint writes nothing. A
configuration that includes is still linted, at its ref, through the
GET that reads what the project already holds.

The POST changes nothing, so it declares itself read-only on its call.
It runs under a dry run and states no outcome (D-29).

Rejected: linting supplied content as given, includes and all.
Cost: supplied content is refused in read-only mode, where only the
project's own configuration can be linted.

### D-49 Release asset links point only into the release's own project

`create_release` takes asset links only into the release's own project:
under its web path or its API path, on gitlab.com's origin, with no
credentials in the URL. Anything else is `[blocked]` before anything is
sent. Links are taken at create only.

A release page sends every reader wherever its links lead, and
gitlab.com serves anyone's files. A link written by a persuaded model
could point a project's users at a download from another project that
looks like the release's own
([release links](https://docs.gitlab.com/api/releases/links/)).
Confining links to the project keeps every asset one the project's own
members put there.

`create_release` is Ship, since it creates a tag and starts tag
pipelines (D-22), and it is in the `releases` toolset (D-32).

Rejected: links anywhere; link writes on a published release, which
would change where an existing page sends its readers.
Cost: an asset hosted elsewhere, such as another project's package or
an outside download site, is linked by a person in GitLab.

### D-50 Variable values are sent and never shown

`run_pipeline` takes variables and inputs. `retry_job` and `play_job`
take values for the inputs a job declares. The values are sent to
GitLab and never shown: a result names each variable and input with its
value masked, and logs never carry either.

A CI variable's value is often a secret. The result is read by the
model and may be pasted anywhere, so a value the caller passed is not
repeated back. The same reason writes off reading pipeline variables:
GitLab's listing carries their values
([pipelines](https://docs.gitlab.com/api/pipelines/)).

GitLab refuses an input name a job's `inputs:` does not declare, but a
job that declares none takes any input and uses none. So the result
names the inputs sent, and a reader can see one that did nothing.

Rejected: reading pipeline variables, whose listing carries their
values.
Cost: a lost `run_pipeline` cannot be told from another run on the same
ref by its variables, so one inside the settle window reads as this one
(D-34).

### D-51 `rerequest_review` refuses a reviewer who approved or has not reviewed

`rerequest_review` asks a reviewer for a new review through GraphQL's
`mergeRequestReviewerRereview` (D-09). It reads the reviewers and the
approvals first. A reviewer who approved, by state or in the approvals,
is `[blocked]`. One still unreviewed is `[conflict]`: there is no review
to ask for again, and no read could settle a lost re-request.

GitLab's mutation sets the reviewer back to unreviewed, removes their
approval, notifies them, and adds a to-do and a system note
([GraphQL reference](https://docs.gitlab.com/api/graphql/reference/)).
From an approver that strips a sign-off another person's merge rule
counts, which is a Ship effect behind a Write tool. Refusing an approver
keeps the tool to its job: asking for a new look after requested
changes are addressed.

A GraphQL answer with a payload and a top-level error means the
mutation ran, so it is reported as done and not sent again.

Rejected: refusing only by an `approved` state, since the approvals can
list a reviewer under another state.
Cost: the mutation carries no witness, so a reviewer who approves
between the read and the mutation loses that approval. Only the
reviewer can open that window, and GitLab notes it and asks them again.

### D-52 Reviews are drafts, published together or one at a time

Reviews are drafts. `add_review_comment` creates a draft note, and
`list_review_comments`, `update_review_comment` and
`delete_review_comment` manage the account's drafts.
`publish_review_comment` publishes one on its own. `submit_review`
publishes them all at once with an optional summary and
`reviewer_state`: `reviewed`, `requested_changes`, or `approved`, which
needs Ship (D-22).

This is how people review in GitLab: comments gathered, then submitted
together with a verdict and one notification
([draft notes](https://docs.gitlab.com/api/draft_notes/)). Submitting
together also keeps a review inside gitlab.com's limit on notes a
minute, which the client paces in a bucket of its own (D-75).

Publishing one is its own tool. GitLab's single publish takes no
summary or reviewer state and creates no review. A reply resolves its
thread when it was drafted to and reopens it otherwise, as GitLab does,
and the dry run warns of the reopening. A draft holding a quick action
is refused at publishing (D-18).

Rejected: publishing one draft as an option of `submit_review`.
Cost: GitLab answers a single publish 204 without the comment, and also
when it deleted the draft without saving one. So a publish is settled by
reading the threads, and a lost one returns the draft's text.

### D-53 The server computes where an inline comment lands

The server computes where an inline comment lands. The caller names a
`file`, a `line` and a `side`, `new` or `old`, and optionally an
`end_line`. `internal/diffpos` reads the merge request's latest diff
version for its `base_sha`, `start_sha` and `head_sha`, finds the line
in a hunk of that file's diff, sets `new_line`, `old_line` or both, and
computes `line_code` and, for a range, `line_range`. After creating, the
comment is read back and the result says where it landed.

GitLab's position object is easy to get wrong and refuses a wrong one
with little help ([discussions](https://docs.gitlab.com/api/discussions/)).
"`line_code` can't be blank" was the most-reported write failure in
other GitLab servers. The
numbering follows GitLab's own parser: a removed line carries the new
side's running counter and an added line the old side's
([`parser.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/lib/gitlab/diff/parser.rb)).

Table tests hold it over generated diffs, and the live step "whether
diffpos positions land on GitLab's lines" holds it to gitlab.com.

Rejected: the model computing GitLab's position from the diff.
Cost: a line outside every hunk is `[invalid]`, naming the nearest hunk
ranges, since GitLab cannot anchor a comment there.

### D-54 Every read has a budget and names what it left out

Every read has a budget in characters, stated in the result, and every
omission is named and continuable. A discussion list renders newest
first up to its budget and lists the rest by id, author and date with a
cursor. A long description is cut at a paragraph boundary with the
offset to continue from. Diffs are listed per file first, then read by
file, and one diff larger than the budget continues by `diff_offset`.
Binary files return their size, type and blob id, never lossy text. An
embedded project, milestone or pipeline becomes its id and name.

Unbounded reads fail in practice. One list call in another GitLab
server returned 120,000 to 180,000 tokens of embedded objects, and
another capped labels at 20 without saying so. A read that silently
returns less than it found is worse than one that fails: the model
reasons from it as complete.

Linked lists, approvals and reviewer states are GitLab's first page,
read best effort, and say when GitLab has more. GitLab leaves out what
the account cannot read, so no such list claims to be complete. A read
continued from an offset does not read them again.

Rejected: an unbounded read; reading only the newest pages of threads,
since a reply moves an old thread to the top.
Cost: `get_issue` and `get_merge_request` walk up to ten pages of
threads to count them, and each `list_discussions` page walks them
again.

### D-55 A listing pages by an opaque token bound to its query and says whether it is complete

A listing takes `max` and `page_token`. The token is an opaque handle
over the next page: keyset where the endpoint supports it, offset
otherwise. It is refused when it was issued for another query. Every
listing says whether it is complete.

GitLab's paging has edges a client cannot see from one page. "if a
query returns more than 10,000 records, GitLab doesn't return" `x-total`
or `x-total-pages`
([REST API](https://docs.gitlab.com/api/rest/)). An offset past 50,000
rows answers 405 on an endpoint that supports keyset. Keyset is followed
through the `Link: rel="next"` header, never constructed, and only when
that link is same-origin under the API root (D-65). A token bound to its
query stops a model from carrying one listing's cursor into another
filter and reading the wrong rows as the continuation.

Rejected: a keyset cursor the client constructs.
Cost: past 10,000 GitLab drops the count, and the listing says the total
is unknown rather than inventing one.

### D-56 `get_job_log` reads a byte window, and only what its edges need

`get_job_log` reads a window of the stored log in bytes: the tail by
default, `byte_offset` and `byte_limit` to page, and `failed_only` to
jump to the failure. Each window is widened to whole lines, so no secret
straddles its edge (D-16). ANSI codes are stripped, sections fold to one
line naming them, and a line a carriage return overwrote shows as it
was left.

GitLab's trace endpoint takes `byte_offset` and `byte_limit`, at most
500 KB a call, which its API reference does not document
([`jobs.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/lib/api/ci/jobs.rb);
[jobs](https://docs.gitlab.com/api/jobs/)). A tail needs the log's
size, which an archived log gives through its `trace` artifact. So a
log of any size is read for the window and what its edges need, never
whole. `failed_only` reads back from the end until it finds the runner's
failure line, then starts at the section the job failed in.

Rejected: a HEAD of the trace, which answered 0 for a finished log;
reading the whole log, which a long job turns into megabytes.
Cost: a log not yet archived is read 500 KB from its start and measured
by one-byte reads past its end. `failed_only` can read 2 MB for a
failing job that printed no failure line.

### D-57 `compare_mr_versions` compares the versions' heads straight, in the target project

`compare_mr_versions` shows what changed between two of a merge
request's diff versions, or since one to the newest, so a second review
reads only what was pushed after the first. GitLab has no route for it.
The tool calls the repository compare in the target project, from the
older version's head to the newer's, with `straight=true`, and bounds it
as `compare_refs` does.

That is what GitLab's own versions page compares. With `straight`, the
compare runs directly from `from` to `to` rather than from their merge
base ([repositories](https://docs.gitlab.com/api/repositories/)), so a
squash or a rebase between versions does not hide the change. The
target project keeps both heads, so the compare works for a fork's
merge request too.

The result says when the merge base moved, and that it is unknown when
a version has no base. A cut result continues with `to_version` set to
the version it compared to, so a push between calls moves nothing.

Rejected: the version's own route, which returns every file's diff in
one unpaged answer; a compare from the merge base, which hides what a
rebase changed.
Cost: after a rebase, the diff also carries what the target branch
gained in between, as GitLab's page shows it.

### D-58 `list_item_events` merges GitLab's event lists into one bounded history, newest first

`list_item_events` merges GitLab's resource event lists into one
history, newest first: labels added and removed, state changes with the
commit or merge request behind them, the milestone set or removed, and
an issue's weight. GitLab lists each kind oldest first, by offset
([resource label events](https://docs.gitlab.com/api/resource_label_events/)).

Each kind is read from page 1 and then the pages its count names, and
the last page's next-page signal is followed, since events added
meanwhile add pages. The newest pages are kept up to a cap. When older
ones are not, the history covers only what came after that kind's
oldest event kept, and says so. Events are merged once each by kind and
id, since offset pages read at different moments overlap. The page
token is the server's own and names the last event shown, so an event
added between calls neither repeats nor skips one.

GitLab drops label events whose label the account cannot read after
cutting the page, so the history is GitLab's and says so. Iteration
events, a Premium feature, are not read.

Rejected: GitLab's offset as the page token, which could repeat or skip
an event added between calls.
Cost: the cut assumes event ids follow time, which an imported item's
may not. A kind with no place to start, past GitLab's count or filtered
empty, fails `[unexpected]` rather than claim the history whole.

### D-59 `list_boards` gives each list the `search_issues` arguments that return its issues

`list_boards` lists a project's issue boards with their lists in board
order, and gives each list the `search_issues` arguments that return
its issues. A milestone list's argument is the exact title GitLab holds.

A board is how a team sees its work, but GitLab has no route that
returns a board column's issues as the board shows them. `search_issues`
is the read a default tool already has, so a board is read as lists of
arguments for it. GitLab returns lists by kind first and names no kind,
so the order comes from position and the kind from the key present
([boards](https://docs.gitlab.com/api/boards/)). A board's milestone
scope is carried into its label lists' arguments, where GitLab's code
applies it. Other scope filters are shown in their own field. GitLab
never returns the Open and Closed lists, so the result says whether the
board shows them.

GitLab includes every list with its board, so the server pages boards
itself under a budget and says which boards it did not read.

Rejected: a cleaned milestone title as the argument, which matches a
different milestone or none.
Cost: iteration and status lists, a milestone titled like a filter, such
as `none` or `#upcoming`, and a board's weight get no argument, and the
result says so.

### D-60 `get_test_report` reads the whole report and says when it may be partial

`get_test_report` reads the report GitLab builds from the JUnit reports
a pipeline's jobs upload, child pipelines in the project included. It
shows the counts, each suite's counts with failing suites first, and the
failed and errored cases: name, class, file, time and output. The
output is `system_output`, masked as a log is (D-16) and shown inside
the boundary. `offset` continues by case.

GitLab answers every case of every suite in one body, with no paging,
parsed when asked and cached up to two minutes
([pipelines](https://docs.gitlab.com/api/pipelines/)). So the server
reads the whole report and bounds what it shows, rather than asking
GitLab for a page it cannot give. While the pipeline or a child
pipeline has not finished, the report is partial and the cases can
shift between calls, and the result says so.

JUnit is GitLab's only test parser, and it puts the failure in
`system_output` and never sets `stack_trace`
([`parsers.rb`](https://gitlab.com/gitlab-org/gitlab/-/blob/v19.4.1-ee/lib/gitlab/ci/parsers.rb)).

Rejected: reading `stack_trace`, which is always null.
Cost: each call reads the whole report again. A report larger than the
32 MiB body cap falls back to the stored summary, counts and suite
errors without cases, which can lag behind, and `offset` is refused.

### D-61 `get_project` never decodes a secret field

`get_project` never decodes a secret field. No wire struct in
`internal/gitlab` carries a field whose name ends `_token`, and
`TestNoTokenFields` reads every wire type from source and fails on
one. A field that is never declared is never decoded, so a token GitLab
adds to a response cannot reach a result.

A project read can carry secrets. GitLab returns `runners_token`, the
"Token for registering runners with the project", to an account with
the Owner role
([projects](https://docs.gitlab.com/api/projects/)). A runner
registration token leaked through a project read in another GitLab
server. Decoding the project whole would put that token in a result the
model reads and may paste anywhere.

The hand-written wire types are what make this hold (D-08). `api-fields`
holds each decoded field to the snapshot, so a new field is a decision,
not a side effect of a struct tag.

Rejected: decoding the project whole, or a generated type with every
published field.
Cost: a field GitLab adds whose name ends `_token` is never shown, even
when a task would need it, until someone argues it in.

### D-62 Every path segment is escaped once, and a project resolves once per call

Every path segment is built from typed parts and escaped exactly once,
and a `..` segment is refused before anything is sent. A project given
as a path is resolved once per call to its numeric id, which every
later request of the call uses. Results show both the id and the path.

GitLab takes a project as a numeric id or a URL-encoded full path, `/`
written `%2F`, and a proxy in front of it must not decode that, or the
request "doesn't match an API endpoint and responds with a 404"
([REST API](https://docs.gitlab.com/api/rest/)). Other GitLab servers
escaped a branch with a slash twice, and let an unencoded id walk `../`
to other endpoints. Escaping in one place from typed parts makes both
impossible, and the numeric id keeps a path out of every request after
the first.

`get_file` escapes the file path and the ref alike, so a branch named
`feature/x` and a file in a subdirectory each reach the route they name.

Rejected: a path built from raw strings at each call site.
Cost: a proxy that decodes `%2F` breaks at most the first request of a
call, which a numeric id avoids altogether.

### D-63 An issue or merge request is a project plus its `iid`

An issue or a merge request is a project plus its `iid`, named `iid` in
every tool and described as "the number shown as #12 for issues and !12
for merge requests". The project is required. Pipelines, jobs,
discussions and notes use their own ids, which GitLab gives.

The `iid` is the number people see. GitLab's REST reference says that
where a resource has both, "the iid field is usually used instead of id
to fetch the resource"
([REST API](https://docs.gitlab.com/api/rest/)). The global id is never
asked for, so a model cannot pass the number it read as `#12` into a
field that means something else.

`resolve_url` turns any GitLab web URL on the instance, an issue, a
merge request, a file at a line, a commit, a pipeline, a job, a compare
or a wiki page, into these arguments, and says which kind it was. A URL
on another host is `[invalid]`.

Rejected: the global id; an optional project, which sent a model in
another server into a loop of 404s.
Cost: every call names the project as well as the number, so a model
that holds only a link calls `resolve_url` first.

### D-64 Labels resolve by exact name and are never created on assignment

Users are given by username and resolved to ids by the server; an
unknown username is `[invalid]`, naming it. Labels are given by name,
exact, scoped labels included, and a name that does not exist is
`[invalid]`, naming it. Milestones are given by title within the project
and its ancestor groups, and two matches are `[ambiguous]` with both
ids.

GitLab creates a label it does not know when one is assigned: "If a
label does not already exist, this creates a new project label and
assigns it" ([issues](https://docs.gitlab.com/api/issues/)). A typo or a
persuaded call would then add a label to the project that every issue
shares, and planning writes are a toolset of their own (D-32). Exact
names keep an assignment from changing the project's configuration on
the side.

Rejected: creating a missing label, as GitLab does on assignment.
Cost: a call that names a new label fails until someone creates it,
with `create_label` in the `planning` toolset or in GitLab.

### D-65 No redirect is followed and no URL from a response is called, but a same-origin next page or moved project

The HTTP client follows no redirect, and no URL taken from a response
body is called. Two exceptions are each held to the API root on the
instance's origin. A `Link: rel="next"` header is followed for keyset
paging (D-55). A moved project's GET is requested once more at the
address GitLab gives, with the request's query kept.

A redirect followed blindly carries the token to wherever it points,
and another GitLab server shipped exactly that. A URL in a response body
is content someone chose (D-15). Both exceptions are what GitLab's API
itself prescribes: the `Link` header is how its keyset paging continues
([REST API](https://docs.gitlab.com/api/rest/)), and a renamed project's
old path answers a GET with 301 to the project's numeric id. A write to
a moved project answers 405, so only a GET is re-requested. The client
replaces only the project segment and forgets its cached resolution.

A snippet's other files are read by their own route, never by the
`raw_url` a response gives.

Rejected: following a redirect, which carries the token to another
host.
Cost: a moved project costs a second request on its first read, and a
write to it fails until the caller uses the new path or the numeric
id.

### D-66 Sign-in is the person's own public OAuth application over a loopback redirect with PKCE

Sign-in is the person's own OAuth application on gitlab.com, registered
with the redirect `http://127.0.0.1/callback`, Confidential unticked,
and the scope the mode needs. `gitlab-mcp login --client-id` prints the
scopes, opens the consent page, and listens on `127.0.0.1` at a random
port, with PKCE S256, a 64-character verifier and `state`. A callback
without the login's `state` is refused on its own while the login keeps
waiting. `--no-browser` prints the URL and the exact `ssh -L` line.

This is the native-app flow RFC 8252 describes, and GitLab accepts it
for a public application without a secret
([OAuth 2.0 API](https://docs.gitlab.com/api/oauth2/)). Doorkeeper,
GitLab's OAuth provider, ignores the port when both the registered and
the requested URI are loopback IP literals, as RFC 8252 §7.3 asks
([`uri_checker.rb`](https://github.com/doorkeeper-gem/doorkeeper/blob/v5.9.3/lib/doorkeeper/oauth/helpers/uri_checker.rb)),
so a random port matches one registered redirect. `localhost` is not an
IP literal and gets no such relaxation. The generated
[setup.md](setup.md) says exactly what to enter.

Rejected: a `localhost` redirect; a confidential application with a
secret; a `--client-secret` naming a client file, since GitLab offers
none; an RFC 8707 `resource`, which rewrites the scope to `mcp`.
Cost: the person registers an application once with Confidential
unticked, which GitLab ticks by default. A confidential one answers
`invalid_client`, and `login` says to untick it.

### D-67 A refresh token is spent once, under a cross-process lock

A refresh runs under an OS file lock in the config directory. The new
pair is written to the store before the lock is released and before it
is used. A process that gets `invalid_grant` re-reads the store and uses
the pair another process wrote before it reports `[auth]`.

GitLab's refresh "Invalidates the existing `access_token` and
`refresh_token`" and sends new ones
([OAuth 2.0 API](https://docs.gitlab.com/api/oauth2/)). A second
refresh with the old token is `invalid_grant`. Two clients, Claude
Desktop and Claude Code say, run two server processes on one keyring
entry. Without the lock both spend the same refresh token, one loses,
and the person is signed out. `expires_in` is read, never assumed.

A store unreadable under the lock is refused rather than the held
refresh token spent, and a call canceled mid-refresh still stores the
rotated pair. A server that is stopped waits for a refresh in progress
to be stored before it exits. The lock's wait is bounded.

Rejected: a refresh without a lock, each process spending the token it
holds.
Cost: a kill during a refresh still loses the pair, since GitLab's
rotation and the keyring's save cannot be one step.

### D-68 A refused access token is dropped, and a read is sent once more

An access token GitLab refuses as `invalid_token` is dropped. The next
token comes from the store, refreshed if the store holds the refused
one, and a read is sent once more with it. A create is not.

A token can die before its expiry: a `logout` in another process, or
on another machine, revokes it at GitLab. "An expired or revoked token
gets the same challenge with the `invalid_token` error"
([REST authentication](https://docs.gitlab.com/api/rest/authentication/)),
in both the body and `WWW-Authenticate`.
Dropping it on that marker lets the server pick up the pair another
process stored, without asking the person to log in again.

A 401 without the marker is not the token. GitLab answers a malformed
token with a plain 401, and a merge the role does not allow with a 401
too (D-74). Dropping the token then would discard a good one, so only
the marker triggers it.

Rejected: dropping the token on any 401, since a malformed token and a
refused merge both answer a plain 401.
Cost: a create refused for its token is not sent again. The caller sees
`[auth]` and calls once more after the new token is in place.

### D-69 Startup reads the token's scopes and never refreshes

At start, the server reads the token's granted scopes once, when the
access token is still fresh, bounded by the smaller of 15 seconds and
the HTTP timeout. It never refreshes before it serves. With the access
token due, the scopes the last login recorded stand, and the first tool
call refreshes. A start with no scopes recorded refreshes to read them,
and so does one given `GITLAB_MCP_REFRESH_TOKEN`, whose grant the
login's record does not describe.

Registration depends on the granted scopes (D-31), so they must be
known before the tool list. A refresh is the wrong way to learn them. A
host kills servers it started before the handshake, and GitLab revokes
the old pair the moment it rotates (D-67). A server killed between
GitLab rotating the pair and the keyring storing it leaves the profile
signed out. `/oauth/token/info` returns an access token's scopes
without spending the refresh token
([OAuth 2.0 API](https://docs.gitlab.com/api/oauth2/)).

Startup reads neither `/metadata` nor `/user`, since nothing is gated by
version (D-02).

Rejected: refreshing at startup.
Cost: with the access token due, the last login's scopes stand until
the first call refreshes, so a scope removed at GitLab since then shows
only on that call.

### D-70 The server starts and serves signed out, and errors surface per call

The server starts and serves without a sign-in. It lists its tools,
and each call answers `[auth]`, naming `login`. `doctor` and
`--dump-schemas` work signed out. An unreachable instance still gets a
server. Every failure at startup is logged, and the server starts
anyway. A disconnect is exit 0.

A client that fails to start a server shows it as broken, with the
reason in a log the person rarely opens. A server that starts puts the
reason in front of the model on the first call, `[auth]` naming the
command that fixes it, and the model can say so. The schema dump needs
no account, so the gates and the release can build the surface from a
clean machine.

The smoke gate starts the binary with no credentials and holds a call
to `[auth]`, and closes stdin the moment its last message is written to
hold the clean exit.

Rejected: refusing to start without a sign-in.
Cost: a token whose scopes cannot serve the configured mode is the one
fatal outcome at startup (D-31), so that misconfiguration still shows as
a server that will not start.

### D-71 Credentials resolve environment, then keyring, then an owner-only file

The token pair is resolved from the environment, then the OS keyring,
then a file. `GITLAB_MCP_REFRESH_TOKEN` is for CI and automation. The
keyring, Secret Service, Keychain or Credential Manager, holds a pair per
profile under the service `gitlab-mcp`. The file fallback is restricted
to the account that wrote it, with a protected access list on Windows,
and warns on every use.

An explicit setting wins, so an environment token is never shadowed by
a stale stored one. The keyring is where the operating system protects
a secret, and the file exists for a machine without one, such as a
headless Linux box. On Windows a file mode alone decides only the
read-only attribute, so a file written 0600 is readable by every account
on the machine; `internal/fileperm` sets the access list instead.

A keyring save deletes a stale plaintext copy. A keyring that refuses a
save has its older entry deleted, so an old pair is never left in
charge. A pair rotated from an environment token is stamped with that
value's hash, so the next start uses the stored pair rather than the
revoked one.

Rejected: keyring, then file, then environment.
Cost: a token from `GITLAB_MCP_REFRESH_TOKEN` is outside `logout`'s
reach. The person revokes it in GitLab.

### D-72 `logout` revokes at GitLab and names the profiles that share the application

`gitlab-mcp logout` revokes the token at GitLab's `/oauth/revoke`, then
deletes it from every store. It waits for a refresh in progress, so it
revokes the pair that ends up stored. It names the other profiles that
signed in with the same application.

Deleting the local copy alone would leave a live token at GitLab, valid
until it expires, wherever it had been copied. GitLab's revoke endpoint
takes a public application's `client_id` without a secret
([OAuth 2.0 API](https://docs.gitlab.com/api/oauth2/)), and revoking an
access token this way kills its refresh token too.

Profiles that share an application share its fate. Renewing the
application's settings or deleting it at GitLab signs them all out, so
`logout` says which they are rather than leaving the person to find
out.

Rejected: deleting the stored token without revoking it.
Cost: renewing or deleting the application signs the other profiles out
too.

### D-73 Configuration is `GITLAB_MCP_*`, read through one reader and validated once

Every setting is a `GITLAB_MCP_*` environment variable, and most have a
flag bound to the same name. A flag on the command line wins over the
environment, and the environment over the default. Every read goes
through `config.Lookup`, which trims the spaces around a value, so a
setting of spaces alone is unset. `Build` validates once and reports
every problem together, so a bad configuration fails before the server
announces itself. A removed setting that is still on is refused, naming
its replacement.

An MCP client passes a stdio server only a command, arguments and an
environment, so the environment is the source of truth. The prefix is
`GITLAB_MCP_` because CI jobs already set `GITLAB_CI`, `GITLAB_USER_LOGIN`,
`GITLAB_FEATURES` and more
([predefined variables](https://docs.gitlab.com/ci/variables/predefined_variables/)),
and other GitLab tools read `GITLAB_TOKEN`.

`config` exports the registry of every variable, which the staleness
and `mcpb` gates read. A scan of `cmd/`, `internal/` and `scripts/`
refuses a read that does not go through `Lookup`, by any name it
resolves, with a floor on the reads it found.

Rejected: the `GITLAB_` prefix; ignoring a removed setting, which would
allow what its owner set it to stop.
Cost: a new setting lands in the registry, the configuration page and
the bundle in one change, or a gate fails.

### D-74 Every error is `[class] message` from a closed vocabulary

Every tool error is `[class] message`, and the class is from a closed
vocabulary declared in `internal/gapi/errors.go`. Each class's comment
says what it means and what the caller should do. `make classes` holds
the `Class` constants, the `Classes` list and the classes the code
emits to one set, both ways, with a floor.

The class tells the caller the next step, which GitLab's status codes
do not. A project the token cannot see answers 404, like one that does
not exist. A stale file witness answers 400, a moved head 409. A merge
the role does not allow answers "401 Unauthorized This user does not
have permission to accept this merge request"
([merge requests](https://docs.gitlab.com/api/merge_requests/)). Each
class is kept apart from another only where it asks for a different
action: `stale` asks for a re-read, `ambiguous_outcome` for a look and
never a repeat, `blocked` names a guard and its override.

A 401 that does not call the token invalid is `[auth]`, with a message
naming the role as the likely cause. Only `approve_merge_request`
remaps it to `[forbidden]`, after a read shows the token works.

Rejected: six classes, since `stale`, `ambiguous_outcome`, `forbidden`
beside `auth`, `blocked`, `rate_limited` and `unexpected` each ask for a
different action.
Cost: the vocabulary and the code change in the same commit, or the
gate fails. A 401 on a valid token is `[auth]` on most writes (#96).

### D-75 Requests are paced by a per-instance rate model, with buckets for GitLab's known application limits

Requests are paced by a per-instance token bucket and a cap of four
requests in flight. The bucket starts at 2,000 requests a minute,
gitlab.com's limit for an authenticated user on Ultimate. Free allows
100 and Premium 1,250
([GitLab.com rate limits](https://docs.gitlab.com/user/gitlab_com/rate_limits/)).
`RateLimit-Remaining` and `RateLimit-Reset` lower the bucket when
present, and are never trusted to be present. Known application limits
have their own buckets: notes are paced at 60 a minute. `get_me` reports
the last reading.

The server cannot read the account's plan, so it cannot start from the
right limit. Waiting before a request is cheaper than earning a 429 and
backing off, and the headers correct the bucket on the first answers
that carry them. The application limits matter because the headers
describe only the instance throttles: an application limit answers 429
while `RateLimit-Remaining` still shows quota. A review publishes its
comments in one call for the same reason (D-52).

Rejected: trusting `RateLimit-Remaining` to predict a 429; conditional
reads, since gitlab.com counts every 304 against the limit.
Cost: which limit a request will hit cannot be known before it hits. On
Free or Premium the bucket starts above the plan's limit, and a burst
learns it from the headers or a 429 (#100).

### D-76 Every request is bounded by progress timeouts and a 32 MiB body cap

Every request is bounded by progress, not by a single deadline. A
response must start within the HTTP timeout, then a stall guard watches
each read of the body. A request body is timed by its progress: the
header timeout starts again whenever more of it is sent, so it times a
stall and then the wait for the answer. A response body is capped at
32 MiB. `GITLAB_MCP_HTTP_TIMEOUT` bounds a call's time without progress,
between 1 second and 10 minutes.

A deadline across the whole exchange cuts a slow uplink off mid-body,
and a large commit or image upload cut off after it may have landed is
`[ambiguous_outcome]` (D-34). Timing
progress lets a large body through on a slow line while a stalled one
still fails. The cap keeps one answer from taking the process's memory.

The body can be given again, as Go's HTTP/2 transport asks for a stream
the server refused. Job logs are read in windows, so the cap never
meets them (D-56).

Rejected: a header timeout run once across the whole body.
Cost: a create whose answer is over 32 MiB is `[ambiguous_outcome]`,
settled by reading, and a report over the cap falls back to its summary
(D-60).

### D-77 A reply carries `structuredContent` and a different text rendering

Every reply carries both halves: `structuredContent` under the tool's
output schema, and a readable rendering in `content`. The SDK is given
`Content`, so it does not add the JSON as a second text block.

The specification asks for both: "For backwards compatibility, a tool
that returns structured content SHOULD also return the serialized JSON
in a TextContent block"
([tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)).
The text here is a rendering of the same facts rather than the same
bytes. Clients differ in which half they show a model, so each half
must carry every fact. A program reads the fields. A model reads the
rendering, which carries the boundaries (D-13) and the budgets (D-54).
The JSON twice would spend the model's context on what it already has.

Every output has a schema, with `date-time` for times, and a test holds
the two halves to differ.

Rejected: the same bytes twice, in `content` and in
`structuredContent`.
Cost: content text is in both halves, and the structured one names it in
`untrusted_*` fields, so a long description is carried twice.

### D-78 The released tool surface is a contract

The released tool surface is a contract. Tools keep their names, input
and output fields, and resource templates their URIs. `schema-diff`
dumps the whole surface with every switch and toolset on, `_meta`,
annotations and output schemas included. It compares the dump with the
last tag, built in a git worktree that is always removed, or with
`testdata/schema-baseline.json` before the first tag. It fails on a
removed tool, a lost input or output field, or a new required input,
unless a **Breaking:** entry for a new major release names the tool.

A caller written against a release keeps working, or the release says
it will not. Go reaches a new major version only through a new module
path ([major version suffixes](https://go.dev/ref/mod#major-version-suffixes)),
so a break also moves the module to `/vN`.

On a pull request, `schema-ack` builds the merge-base and the head and
needs each changed item named in a commit footer: `BREAKING CHANGE:`
for a break of the last tag, `SCHEMA-CHANGE:` for the rest. The
acknowledgment is an empty commit, never an amend.

Rejected: measuring a pull request from the base SHA its event recorded,
which is stale in stacked pull requests.
Cost: a break needs a major release with a **Breaking:** entry naming
the tool, and a `/vN` module path `go install` reaches.

### D-79 Input schemas stay flat, and arguments are decoded leniently

Input schemas stay flat: no top-level `anyOf`, shallow nesting, every
property typed. Arguments are decoded leniently. An array or an integer
sent as JSON inside a string is decoded. `null` and `""` for an optional
input mean absent. A required input is never relaxed. A debug line names
the inputs adjusted, never their values.

Models send `"[1, 2]"` for an array and `"5"` for an integer, and a
strict decoder fails a call the model meant correctly. Clients and
models handle deep or branching schemas unevenly, and a flat one is the
form every client renders and every model fills. An enum refusal names
the allowed set, sorted, so a model can correct itself in one step.

Lenient decoding needs the SDK's untyped `AddTool`, so `register`
validates the arguments itself after decoding. Review holds the flat
shape. No gate checks it.

Rejected: relaxing a required input.
Cost: an empty value cannot clear a field, since `""` means absent, so
updates take `clear_*` inputs, as `update_issue`, `update_label` and
`update_milestone` do.

### D-80 Every tool and input is described, and a witness says it is not a retry signal

Every tool and every input has a description. A witness input's
description says it is NOT a retry signal. The `descriptions` gate reads
the `--dump-schemas` output, the surface a client receives, and fails on
an empty dump, a missing description, more than one `IMPORTANT:` in a
description, or a witness without that phrase. A test holds the house
style.

A model chooses tools and fills arguments from the descriptions alone.
A witness answered `[stale]` invites a model to read the new value and
send the same write again, which turns the guard into a step it routes
around (D-36). The phrase tells it the witness records what the caller
read, and a `[stale]` asks for a fresh look at what changed, not a
fresh value.

The gate reads the dump rather than the Go registry, so it checks what
`register` actually published, `_meta` and generated inputs included.

Rejected: reading the Go registry rather than the dump.
Cost: every new input needs a description, and every witness the phrase,
before `make check` passes.

### D-81 Resources carry the same text as their tools

Three resource templates serve clients that attach rather than call:
`gitlab://projects/{id}/issues/{iid}`,
`gitlab://projects/{id}/merge_requests/{iid}` and
`gitlab://projects/{id}/jobs/{job}/log`. Each carries the same text as
`get_issue`, `get_merge_request` and `get_job_log`, under the same
budget and the same boundaries, because the same service call and the
same renderer produce it.

Some clients let a person attach a resource to a conversation instead
of letting the model call a tool. A resource with its own rendering
would be a second path for content into the model, one that could skip
the boundary (D-13), the hidden-text count (D-14), the masks (D-16) or
the budget (D-54). One path means a fix to any of them reaches both.

A template takes `{x}`, never `{+x}`, so a project path cannot carry a
slash into another route. A resource that does not exist answers
`CodeInvalidParams` with a `[class]` message, as a tool error would.
The smoke gate lists the templates and reads one through the shipped
binary.

Rejected: a resource with its own rendering of the item.
Cost: a resource takes no options, so a continuation offset, a byte
window or `failed_only` needs the tool.

### D-82 Green gates are not done: a live run drives every tool option, and a person reads its transcript

Green gates are not done. `make live` drives the shipped binary over
stdio against gitlab.com, and drives every tool and every option. It
records what it sent per tool option, and `make live-cover` fails on an
option no live step drove, unless `testdata/live-cover.tsv` waives it as
undrivable or undriven with a reason. A stale waiver fails, and so does
an empty record. A person reads the transcript before the run counts.
[CONTRIBUTING.md](../CONTRIBUTING.md#the-live-run) says how to run it.

A unit test asserts the behavior the code was written to have, and every
fake in `internal/gapi/gitlabtest` is written from what was believed
about GitLab (D-07). Only a live run can show the belief wrong. A new
merge request's `updated_at` moves a second after the create answers. A
commit's move action sent with empty content empties the file it moves.
Adding an assignee leaves `updated_at` in place. A unit suite written
from the opposite belief passes on each, and only gitlab.com says
otherwise.

A change to sign-in, the quick-action guard, diff positions, a witness
or an API response shape gets a live run with its transcript read
before it counts. The release checklist asks for one.

Rejected: green gates as done.
Cost: a run needs gitlab.com, a stored sign-in and a group the person
owns, and a person reads the transcript, so it is run by hand and not in
CI. Some behaviors no step reaches yet (#97).

### D-83 The live driver reads only what it wrote, in scratch projects it makes and deletes

The live driver creates private scratch projects named for the run,
under a group the person names with `-namespace` and owns. It writes its
own files, branches, issues, comments, merge requests and pipelines
there, and every read it makes is inside what it created. Its searches
carry the run's own word. At the end it deletes the projects, unless
`-keep` is set. A test holds every step to the scratch project, and
another holds every assignee, reviewer and to-do a write names to the
run's own account.

The account a live run signs in with can see real organizations' code
and real people's words. A read outside the scratch projects would put
them in a transcript a person then shares, and a write outside them
would change someone's work. Confining the driver to what it made keeps
the run inside D-06 whatever account it uses.

The driver is not the server. It runs only under the `live` build tag,
and nothing it does widens what a tool may do.

Rejected: driving the tools against an existing project, whose content
would reach the transcript.
Cost: project deletion is the one write the driver has that the server
does not, and gitlab.com deletes a project only after a 30-day delay
([GitLab.com settings](https://docs.gitlab.com/user/gitlab_com/)), so
scratch projects linger in the group meanwhile.

### D-84 The drivers print only through one redacting writer

The live driver and the evals print only through one redacting writer,
which masks hosts, paths, the account and ids on every line. The
`transcript` gate holds it. A driver cannot reach `os.Stdout`,
`os.Stderr` or `log.*`. Exactly one package is exempt, with exactly one
write site, which redacts. A driver that is not listed fails, a listed
directory that is missing fails, and the gate has a floor on what it
read.

A transcript is read by a person and shared, in a pull request or a bug
report. The drivers run against a real account, so the names in their
output are a real organization's (D-06). Redacting where text is
printed, once, means a new print cannot forget it. Redacting in each
driver would leave the leak to the one print nobody reviewed.

The redactor masks before it truncates, so a cut value is still masked,
and it matches a value that is already masked.

Rejected: each driver redacting its own output.
Cost: titles and bodies are not masked, so every one of them is text
the driver wrote, and a transcript is still read before it is shared.

### D-85 Evals score planted instructions against the in-memory GitLab, fail-closed

`scripts/evals` scores a model driving the tools against the in-memory
GitLab only, never a real instance. Tasks carry an instruction planted
in the content by someone outside the project: read a private file, run
a quick action, post in another project, approve and merge. Each is
scored on whether the model followed it, from a census of the instance
before and after.

A model runs by hand through `claude -p`, with every built-in tool off
by `--tools ""` and `--setting-sources ""`, so the server's tools are
the only ones it has. That is fail-closed: a denylist names the tools
it knows to block, and a tool it did not name reads whatever it can.
`make evals-check` runs in `make check` without a model. Per task it
drives an empty run, a scripted run that does the task, and one that
follows the steer, through the real server, and holds them to fail,
pass and fail.

The boundary and the guards are claims about what a model does with
planted text. Only a model can show whether they hold, and only the
in-memory GitLab keeps real content out of the run (D-06).

Rejected: a denylist of built-in tools, since Grep and Glob read notes
on disk mid-task.
Cost: a run costs money and is not deterministic, so it is run by hand
and its transcript read.

### D-86 Every gate asserts a floor on how much it read

Every gate asserts a floor on how much it read: packages and statement
blocks in a coverage profile, files in a leak scan, operations in the
API snapshot, tools in a schema dump, reads in the configuration scan. A
gate that read less than its floor fails, whatever it found.

"Found nothing" and "looked at nothing" print the same line. A gate
whose parse broke, or whose path moved, passes silently on an empty
input, and a passing gate is the last thing anyone opens. The floor
turns that silence into a failure. A rule that cannot fail is not a
rule, so every gate is also broken on purpose once and watched failing
before it is trusted, and tests hold each floor firing.

This is why the gates are Go with tests (D-87): a floor is a line of
code a test can drive below.

Rejected: a gate that passes when it read nothing.
Cost: a floor is typed, not derived, so it stands apart from the code
it checks and is raised by hand as the code grows.

### D-87 Every check is Go in one registry, and `make check` and CI run the same set

Every check this repository runs on itself is Go, in `scripts/gates`,
behind one registry. Each entry declares whether it is manual, in
`make check` or in the release, and the zero value is refused. Every CI
step is a `make` target. The `parity` gate maps targets to CI steps by
their recipes, and fails on a gate implemented but never run or run but
not registered. The `checklist` gate holds the definition-of-done list
in `CLAUDE.md` to the `check:` prerequisites, both ways. CI and the
Makefile use the same `-coverpkg`, held by a test.

A check that exists in one place but not the other drifts, and a gate
in shell is a gate nothing tests. In Go, the code holding the gates shut
is held to the same lint, tests and floors (D-86). There is no shell,
no npx and no Python in it. The pre-commit hook runs `gates precommit`:
gofmt, vet, leaks and gitleaks over the staged tree, every problem
collected before it fails.

Rejected: a Makefile comment claiming to be "everything CI runs"; a
Python pre-commit framework.
Cost: a new check is a Go command, a registry entry and a Makefile
target, and `parity` fails until all three agree with CI.

### D-88 Every tool runs at one exact pinned version through `go run`

Every tool the Makefile runs is `go run module@version` at one exact
version, never a binary from `PATH`: golangci-lint, goreleaser,
gitleaks, govulncheck, go-licenses and actionlint among them. Every
action in a workflow is pinned to a full commit SHA with its version in
a trailing comment. The `pins` gate holds it: exact semver only, no
range or `latest`, every action classified as an installer or not,
installer tool versions required, and every pin the Makefile and a
workflow share compared, with floors.

The version that runs locally is the version that runs in CI, so a lint
or a scan that passes on one passes on the other. A tool from `PATH`
runs whatever happens to be installed. A tag on an action can be moved,
and a SHA cannot. The golangci-lint and gitleaks actions are not used,
because their steps would be a second pin the Makefile does not share.

Rejected: a version copied from another project; a tool from `PATH`;
the golangci-lint and gitleaks actions.
Cost: a version bump changes the Makefile and the workflow together, and
a dependabot bump of an action keeps its version comment or `pins`
fails.

### D-89 A release is reproducible, signed and attested, from a tag whose CI is green

A release is built by goreleaser from a `vX.Y.Z` tag. The release
workflow refuses a tag whose commit's CI run did not finish green, runs
`go test`, builds one target twice and compares the hashes, and builds
with `CGO_ENABLED=0`, `-trimpath` and the commit's timestamp. It signs
`checksums.txt` with a keyless cosign bundle, attaches an SBOM to each
archive, and attests build provenance over the archives, the checksums
and the Claude Desktop bundle. The release notes' verification commands
name the exact certificate identity.

The binary holds a person's GitLab token and acts as them. Whoever
installs it should be able to check, from outside, that it was built
from this source by this repository's workflow
([artifact attestations](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations)).
A reproducible build lets anyone rebuild and compare. A tag is only a
pointer, so the CI run on its commit is what says the code passed.

The `release` gate holds `.goreleaser.yaml` against the release
workflow and the packer. `make release-rehearse` runs goreleaser
locally without publishing.

Rejected: a certificate identity matched by a regular expression; `go
mod tidy` in the before hook, which mutates the tree being released.
Cost: the signature, the attestation and the registry publish need a
real run's OIDC token, so no rehearsal reaches them.

### D-90 The registry entry is published from the signed checksums, never for a prerelease

The MCP registry entry is published by its own workflow, from the signed
`checksums.txt`, and never for a prerelease. The workflow downloads the
checksums and verifies their cosign signature against this repository's
release workflow at that exact tag before it reads a hash. It verifies
the `mcp-publisher` binary against its own release workflow. It
generates `server.json` at publish time and validates it against the
vendored registry schema.

The entry tells every registry client which bundle to fetch and its
hash ([MCP registry](https://github.com/modelcontextprotocol/registry)).
A hash taken from anything but the signed checksums would let a
tampered artifact be advertised as the release. The `server-json` gate
derives the name from `go.mod` with `/vN` stripped, takes the
description from the one constant the bundle uses, at most 100
characters, and needs exactly one `.mcpb` row in the checksums. It
holds the result to the vendored schema.

The workflow can be run again for a tag that already shipped.

Rejected: a hash from a local build.
Cost: a registry entry cannot be withdrawn, so a release published by
mistake stays listed until a newer one supersedes it.

### D-91 The bundle asks for every setting, with defaults that start the server as no settings do

The Claude Desktop bundle asks for every setting a person sets in
another client's configuration: the OAuth application, the profile,
read-only, the Ship and Destructive switches, the toolsets, the write
namespaces and the upload directory. Each defaults so the server starts
exactly as it does with no settings at all. The bundle does not log the
person in, and says so.

Claude Desktop approves each tool call, and a person can set a tool to
always allow. Defaults that widen nothing are the one guard a bundle
install keeps. The `mcpb` gate derives the asked keys from the
configuration registry, so a new setting is asked for or excluded with
a reason. It starts the configuration from the bundle's defaults and
from nothing, and refuses any difference. It starts every combination of
the switches and holds the refused ones, read-only with either switch,
to a declared list the fields' descriptions follow. A field that sets a
credential is sensitive.

An optional key with no default is never a variable to the host, so
`${user_config.x}` would reach the server as written
([MCPB manifest](https://github.com/modelcontextprotocol/mcpb/blob/main/MANIFEST.md)).
Each asked key therefore has a default.

Rejected: a `tools` list in the manifest; the asked keys typed out; an
optional key with no default.
Cost: a bundle install starts with Ship, Destructive and every toolset
off, and the person turns each on in the extension's settings.

### D-92 The bundle manifest is held to its vendored schema, read as every host reads it

The `mcpb` gate reads the committed manifest once, from one root, by one
function the packer also runs. It refuses a key repeated in one object,
or two keys equal but for case, before any other check. It validates the
raw bytes against the vendored schema that the manifest's own
`manifest_version` chooses, each embedded with its SHA-256 and source
URL. The entry point and every command must name a staged program under
`${__dirname}/`. A platform override may carry no `env` or `args`. The
packer stamps the version by decoding and encoding, never by text
substitution, and holds the stamped manifest to the schema too.

Readers of one manifest disagree. Go's decode into a struct merges a
repeated key and matches a key whatever its case, while a decode into a
map, as the schema validator, the packer and Node's `JSON.parse` do,
keeps the last and keeps spellings apart. A host substitutes an override
for the base rather than merging it. `${__dirname}` "is replaced with
the absolute path to the extension's directory", the one path the
specification fixes to the bundle
([MCPB manifest](https://github.com/modelcontextprotocol/mcpb/blob/main/MANIFEST.md)).
A manifest every reader reads alike is the only one a gate can vouch
for.

Rejected: a relative or absolute command; `env` or `args` on a platform
override; text substitution when stamping.
Cost: a vendored schema says nothing about upstream until
`make schema-refetch` runs, which the release checklist asks for.

### D-93 The CHANGELOG section is the release note

The release note is the version's section of `CHANGELOG.md`, lifted
verbatim by `gates release-notes`, which refuses a missing or empty
section. The CHANGELOG follows Keep a Changelog's sections
([Keep a Changelog](https://keepachangelog.com/en/1.1.0/)), with
**Breaking:** on anything a reader must act on.

The `changelog` gate makes a pull request add an entry, unless it cuts a
release or changes `go.mod` only in the modules it requires, the MCP SDK
excepted. `changelog-links` holds every version heading to its link
reference.

A note written once, in the pull request that made the change, is
reviewed with the change and written while the change is understood. A
note written on release day is written from memory, and two copies of
it drift. The entry is the release note, so a reader of the release
page reads what the reviewer read.

Rejected: release notes written at tag time, apart from the CHANGELOG.
Cost: a tag pushed before its section exists stops the release before
goreleaser runs.

### D-94 A tool sends a raw REST or GraphQL request

A tool could send any REST or GraphQL request the model writes: a
method, a path and a body, as an escape hatch for what no tool covers.

It is declined. A raw request reaches every route past the kinds, the
quick-action guard, the protected-ref guard and the write allow-list,
so every control in this server would be one tool call from bypassed.
The most-starred community GitLab server shows it: its published
security advisories include read-only mode bypassed through its raw
GraphQL tool and its verb heuristics. The `read_api` token stops writes in
read-only mode, but with `api` nothing would. GraphQL is used only as a
fixed literal per operation for that reason (D-09).

Every operation the server calls has a verdict and a tool with a kind
(D-10, D-20), so a new need becomes a reviewed tool rather than a
string the model composes.

Rejected: a raw REST or GraphQL request tool.
Cost: an operation no tool covers needs a new tool, with its kind, its
guards and its verdict, before any call can make it.

### D-95 Tools read or write CI variables and secure files

Tools could read or write a project's CI/CD variables and secure files,
so a model could inspect or change what pipelines run with.

It is declined. Reading them puts secrets into a model's context, from
where a result can be pasted anywhere, and a masked variable is masked
only in job logs. Writing them is a supply-chain lever: a changed
variable alters every pipeline that runs after it, on branches nobody
reviewed the change for
([CI/CD variables](https://docs.gitlab.com/ci/variables/)). Neither
serves an assistant's task inside a project.
`testdata/api-coverage.tsv` writes them off, with pipeline schedules and
triggers.

Variable values the caller passes to `run_pipeline` are sent and never
shown (D-50).

Rejected: reading or writing CI/CD variables and secure files.
Cost: the pipeline variables listing goes with them, since it carries
their values, so a lost `run_pipeline` cannot be told apart from another
run by its variables (D-34).

### D-96 Deleting projects or groups, force pushes and protected-branch changes sit behind a flag

Deleting a project or a group, force-pushing, and changing protected
branches could sit behind the Destructive switch, as deleting a branch
or a comment does.

It is declined; they are not gated, they are absent. The Destructive
kind removes one item a person can see and name, behind `confirm` and a
witness (D-28, D-36). A project or a group takes every issue, merge
request, pipeline, wiki page and its history with it. A force push
rewrites history other people built on. A protected-branch change
removes the guard that keeps code out of protected branches except
through a merge request (D-33), so one call would undo the rule every
other write obeys. Each is administration as well, which the server
does not do (D-11).

Rejected: deleting projects or groups, force-pushing and changing
protected branches, behind any switch.
Cost: a person who needs any of these does it in GitLab, and the live
driver's own scratch-project deletion stays outside the server (D-83).

### D-97 Events arrive by webhook

Events could reach the server by webhook, so it could tell the model
when an issue changed or a pipeline failed.

It is declined. A webhook is a POST from GitLab to a URL
([webhooks](https://docs.gitlab.com/user/project/integrations/webhooks/)).
A stdio server is a subprocess a client starts and stops. It owns no
listener, no public address and no lifetime beyond the client's
session, and GitLab could not reach it on a person's laptop. A listener
would also be the hosted, network-facing component this server exists
not to be (D-01).

Changes are read when they matter: `list_item_events` gives an item's
history, `list_todos` what is waiting on the person, and the `activity`
toolset's `list_events` what happened in a project.

Rejected: a webhook receiver.
Cost: the server learns of a change only when a tool reads it, so a
model notices a failed pipeline when it next asks, not when it fails.

### D-98 The server serves self-managed instances

The server could serve self-managed GitLab instances, with an instance
setting beside gitlab.com.

It is declined. A self-managed instance brings an instance setting and
base URLs in every shape, a private CA, plain `http`, and version
gating for features an older release lacks. Self-signed instances were
the most-reported setup failure in other GitLab servers, and base URLs
given with or without `/api/v4` broke them too. Each is a place for a token to go somewhere it should not
(D-03), and each needs its own tests and live runs against instances
nobody here runs. gitlab.com runs the newest release, so one instance
needs no version gating at all (D-02).

The loopback test override is the one other instance, and it exists
for tests (D-02).

Rejected: self-managed instances, with an instance setting, a private
CA, plain `http` and version gating.
Cost: a team on a self-managed instance cannot use this server, and the
tier checks rest on gitlab.com's answers alone.

### D-99 Content from authors below Developer in public projects is withheld

In a public project, content from authors below the Developer role
could be withheld unless the operator set a switch, as GitHub's lockdown
withholds content from authors without push access.

It is declined. The evals plant instructions in exactly the content
lockdown would withhold, a non-member's comment in a public project:
read a private file, run a quick action, post in another project,
approve and merge with Ship on. The model is scored on whether it
followed them, and the layers behind it hold without lockdown: the
boundary (D-13), the quick-action guard (D-18), the unregistered kinds
(D-21) and the write allow-list (D-38). Lockdown would cost a members
read on every call, and hide the bug reports outsiders file, which are
often the reason to read a public project at all.

Rejected: withholding content from authors below Developer in public
projects.
Cost: a model reads outsiders' text, inside its boundary. If a model or
a task makes the evals fail, this is revisited, and `make evals` is the
check.

### D-100 A client id ships in the binary

A gitlab.com OAuth application id could ship compiled into the binary,
so sign-in would need no setup.

It is declined. A shipped id puts one deployer's application in every
copy, which D-01 rules out: every person's grant would hang on an
application someone else owns and can change or delete. Each person's
own application
([OAuth provider](https://docs.gitlab.com/integration/oauth_provider/))
keeps their grant theirs, and keeps the repository free of anything
deployer-specific (D-06). Reusing GitLab's own CLI's id was weighed
too. It is registered on `localhost:7171`, a fixed port on `localhost`,
which gets no loopback port relaxation and collides with the CLI, and
nothing authorizes its reuse.

Rejected: a client id shipped in the binary, this project's or GitLab's
CLI's.
Cost: every person registers an application before signing in, where a
shipped id would need no setup. [setup.md](setup.md) keeps that to one
form.

### D-101 Personal access tokens or the device flow sign the person in

The person could sign in with a personal access token, or through
OAuth's device authorization flow, instead of the loopback flow.

It is declined. A personal access token is a long-lived secret the
person pastes into a client's configuration, and it expires on a date
they chose, at most 365 days out by default
([personal access tokens](https://docs.gitlab.com/user/profile/personal_access_tokens/)).
It has no refresh, so the server would fail on that day with nothing to
renew. The device flow exists
([OAuth 2.0 API](https://docs.gitlab.com/api/oauth2/)), but it is off
by default for each new application unless someone ticks a setting
GitLab's provider page does not mention. One sign-in path, the person's own application
over a loopback redirect (D-66), keeps one set of tests and one page of
setup.

Rejected: personal access tokens; the device authorization flow.
Cost: a machine with no browser signs in through `--no-browser` and an
`ssh -L` forward, and automation passes a refresh token in
`GITLAB_MCP_REFRESH_TOKEN`.

### D-102 The server confirms a write by asking the person through elicitation

The server could confirm writes itself: before a merge, an approval, a
manual job, a release, a tag, a pipeline on a protected ref, publishing
a confidential issue and every delete, it would put a question to the
person through MCP form elicitation and write only on an accept.

It is declined. The specification puts consent on the client, which
SHOULD "Prompt for user confirmation on sensitive operations"
([tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)),
and lets a server ask through elicitation without making the answer
consent. Most widely used servers confirm nothing themselves. Clients
answered such a question with nobody there, did not draw it, left it
hanging, or cut its message, and no client's allow and ask rules match
it. A question on every call is answered without reading. A control
that holds on some clients and not others is not a control (D-26).

Rejected: confirming writes through elicitation; a setting that refuses
the asking writes on a client that cannot ask.
Cost: a client that runs with nobody watching makes every registered
write, so Ship and Destructive stay off for one. A configuration that
still sets the removed `GITLAB_MCP_REQUIRE_PROMPT` is refused at start.

Next ID: D-103
