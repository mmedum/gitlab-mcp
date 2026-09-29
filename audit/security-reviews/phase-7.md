# Security review — phase 7

Range: the phase 7 changes on `phase-7/confirm-with-the-person`, cut
from `main` after the sign-in fix merged, run on 2026-09-29 before the
code review's fixes were final. Scope: asking the person through MCP
form elicitation before the thirteen writes of §4.12, the signed answer
state, the question text, `GITLAB_MCP_REQUIRE_PROMPT`, and the stdio
client's answers in the live driver.

**Result: one finding at confidence 8, fixed before the commit.**

- **`run_pipeline` skipped its question for a fully qualified tag
  (high).** It looked the ref up by its literal name, as a branch and
  then a tag; `refs/tags/<tag>` found neither and asked nothing, while
  GitLab resolves it and runs the protected tag's pipeline. Fixed: a
  `refs/heads/` or `refs/tags/` ref is read as the branch or tag it
  names, and a ref that is neither is asked about, since its protection
  cannot be told. A test covers each spelling.

Examined and holding:

- **Forged, tampered, replayed or cross-call state.** HMAC-SHA256 under
  a key drawn per process, compared with `hmac.Equal`; the nonce is spent
  on the first redeem; tool, argument hash and expiry are checked.
- **An answer applied to other arguments.** The state binds the decoded
  input; the retry reads again and compares the question's binding, so a
  head, branch or comment that moved is refused.
- **Answers on a tool that does not ask**, and answers with no state,
  are refused; anything but an accept is refused before the service
  runs.
- **A write reached without asking.** Each of the thirteen asks after
  its reads and dry run, just before its write; a write with no asker is
  refused; a Destructive tool without `Asks` fails at start.
- **Question spoofing.** Every value from GitLab or the call goes through
  `quoted()`: one line, hidden and control characters dropped, backticks
  and quote lookalikes folded, links broken, cut; it cannot leave its
  code span or start a line.
- **Secrets.** A pipeline's or job's variables are named by key; their
  values never reach a question or a log.
- **The stdio client** answers `elicitation/create` only when the live
  driver sets `OnElicit`, and method-not-found otherwise.

Out of scope, and recorded as such in §4.12: a client that accepts an
empty form by itself, and the Ship tools outside the chosen set.
