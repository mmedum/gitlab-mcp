# Security review — phase 2

Range: the phase 1 commits on `phase-2`'s base to the phase 2 build,
run with `/security-review` on 2026-09-27 after the simplification pass.
Scope: the Go changes that ship (`internal/**`), the gates that hold
them, and the live driver.

**Result: two findings at confidence 8, both fixed before the phase
was committed.**

1. **`create_branch` had no protected-branch guard (high, confidence
   8).** A persuaded model could commit a payload to an ordinary branch,
   then create a branch under a wildcard protected rule at that ref:
   code in a protected branch no merge request showed, with its
   pipeline seeing protected variables (CLAUDE.md rule 7). Fixed:
   `create_branch` runs the guard `create_commit` runs and refuses a
   name the rules cover `[blocked]`; the guard also refuses when there
   are more rules than one read covers. Tested in the in-memory
   instance and live against a wildcard rule.
2. **`lint_ci` content could make GitLab fetch a URL (medium,
   confidence 8).** GitLab fetches an `include: remote:` while linting
   supplied content, and the tool is Read kind, runs under a dry run and
   is outside the write allow-list: a way to send private text to a host
   of an attacker's choosing that no write control sees (§4.7). Fixed:
   supplied content is parsed, and an `include` key at any depth is
   refused, escaped, quoted and anchored keys included; content that
   does not parse is refused too. Tested with each spelling, and live.

Examined and holding:

- **Quick actions.** Every Markdown body a write sends is declared
  guarded and routed through `internal/quickaction` by `register`:
  descriptions, plain and optional, comment and draft bodies, and the
  review summary. Titles, commit messages, branch names and label names
  are not places GitLab runs commands (§18 row 53).
- **Approval.** A review that would approve is refused without
  `GITLAB_MCP_ENABLE_SHIP`, before anything is read.
- **The allow-list.** It is held against the path GitLab returns for
  the project, and every later request uses the numeric id from that
  answer. `mark_todos_done` is outside it by design: it changes only the
  account's own list.
- **Dry runs.** No write reaches the network under a dry run; the one
  POST allowed through declares itself read-only on its `Call`.
- **Retries.** No create is repeated; a lost one is settled by a read
  tied to the call: a new draft id, the newest threads, the thread a
  reply joined, the branch head.
- **Paths.** Every caller value in a path is one escaped segment, with
  `.`, `..` and control characters refused.
- **The live driver.** It writes only in its scratch projects, assigns
  and asks only its own account, and marks only to-do items it read in
  its own project.

Recorded as a decision rather than a finding: a commit, a new branch or
a new merge request on an unprotected branch starts the pipelines a push
starts, with the account's own permissions and unprotected variables
only (`docs/security.md`). §17 asks whether that should stay Write.
