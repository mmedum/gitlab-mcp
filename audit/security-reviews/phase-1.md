# Security review — phase 1

Range: the phase 0 commits on `phase-1`'s base to the phase 1 build,
run with `/security-review` on 2026-09-27 after the code review fixes.
Scope: the Go changes that ship (`internal/**`) and the live driver.

**Result: no HIGH or MEDIUM finding at confidence 8 or above.**

What was examined, and why each path holds:

- **Boundaries.** Every new renderer puts GitLab content inside the
  per-call block or inline boundary after preparing it, or through
  `Ident` for names: job logs, merged CI configuration, lint messages,
  search excerpts, to-do items, labels, milestones, tags, drafts and
  diffs.
- **Job-log masking.** Timestamps, continuation lines, ANSI escapes,
  section markers and carriage returns are resolved before secrets are
  masked, so none of them can split a token shape. A window that
  starts inside an open private key block is masked from a restored
  header; structured content carries only masked text.
- **Logs.** The one new line, a resource read, carries the template
  name, outcome and duration, never the URI.
- **Paths and tokens.** New ids are positive and escaped as one path
  segment; free text goes only into query values; the draft page token
  is bound to its merge request and decodes only to a draft id.
- **Token and host.** Reading a job log as bytes leaves the redirect
  rules unchanged. The live driver's spike client follows no redirect
  and sends the token only under the scratch project's API path.
- **Live driver confinement.** A tool without a confinement rule is
  refused; a group argument must be the run's namespace with the run's
  word; members, users and labels are narrowed to the run's own.

Recorded below the threshold, for a later phase:

1. A job-log window edge can fall between a timestamped line and its
   continuation, or mid-token on a line over 4 KiB with no space
   within 1 KiB, so a secret crossing it could show in two unmasked
   halves over two calls (confidence 4). Widening to the logical line
   after timestamps are removed would close it.
2. A private key header the runner split across a continuation line is
   not found when a window starts after it (confidence 3).
