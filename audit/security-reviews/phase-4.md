# Security review — phase 4

Range: the phase 4 changes on `phase-4`, cut from `phase-3`, run with
`/security-review` on 2026-09-27 after the simplification pass and the
code review. Scope: the evals harness, the in-memory instance, the live
driver's spike G, the smoke gate and the Makefile. No code that ships
changed.

**Result: no finding at confidence 8 or above.** Three were examined
below it and are recorded here.

Examined and holding:

- **The evals fence.** `claude` runs from a fixed argument list with no
  shell. Every built-in tool is off at the source (`--tools ""`), only
  this server's tools are allowed, the maintainer's settings and other
  servers are left out, and a run that calls anything else is an error.
  A run whose tools answer `[auth]` is now an error too.
- **The evals sign-in.** The refresh token is minted by the in-memory
  instance and valid only there; a random part in every token name keeps
  one instance's tokens from matching another's. It travels in a 0600
  configuration file in a temporary directory, removed afterwards. Each
  world signs the server in under its own `evals-<random>` profile, and
  only that profile's keyring item is deleted.
- **Planted content.** The four injected instructions are synthetic
  fixtures in the in-memory instance, written by a synthetic outsider.
  `Comment` stores them without running a quick action; it is reachable
  only from tests and the harness, and the server's guard is untouched.
- **Spike G.** The `mcp`-scoped sign-in returns a grant and persists
  nothing: the maintainer's stored sign-in is untouched. The token is
  held in memory, never printed, sent only to the run's own scratch
  project, and revoked on every path out; a pair a refresh mints after
  revocation is revoked too. The transcript carries the method, the
  route template, the status, GitLab's `error=` name (`[a-z_]+`) and the
  granted scopes, through the redacting printer.
- **The smoke gate.** Its floor is derived from the gate's own
  environment and holds a constant floor beside it.

Below confidence 8:

| Observation | Confidence | Verdict |
|---|---|---|
| Spike G's deferred revocation ignores its error: after a probe fails and the revocation fails too, an `mcp`-scoped token stays live on the maintainer's own account until it expires | 2 | Kept. Maintainer tooling, the narrowest scope, the maintainer's own account |
| An evals run killed before cleanup leaves a keyring item and a temporary configuration file holding a fixture refresh token | 1 | Kept. The token is worthless once the in-memory instance exits |
| Spike G prints the `error=` name of a `WWW-Authenticate` header | 1 | Kept. The pattern admits only lowercase letters and underscores |
