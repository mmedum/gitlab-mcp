# Security review — phase 5

Range: the phase 5 changes on `phase-5`, cut from `phase-4`, run with
`/security-review` on 2026-09-27 after the simplification pass. Scope:
the windowed job log reads, diff and description continuations, trigger
jobs, issue threads, job inputs, release asset links, a draft's
`line_code`, and the live driver's spike O.

**Result: one finding at confidence 8, fixed before the commit.**

- **A key printed indented or behind a prefix, opened before the window,
  was shown unmasked (medium).** The new walk back matched each line
  against `^-----BEGIN` and a bare base64 pattern. So a key CI printed
  indented, as a YAML block does, or behind `svc-1  | `, as docker
  compose logs do, ended the walk at its first line, and a window that
  started inside it showed the rest unmasked. The whole-log code had
  found a BEGIN anywhere in a line. First fixed by reading a line past
  indentation and a `name |` prefix; the code review then showed other CI
  prefixes (`#8 0.412 `, `[pod/x/c] `) still ended the walk. Fixed for
  good by dropping the walk: the window's read reaches 256 KB before it,
  and a BEGIN with no END after it, found anywhere in its line, masks
  the window, as the whole-log code did within that reach. Tests cover
  indented, tabbed, compose, docker build and kubectl prefixes, and an
  indented key masked end to end.

Examined and holding:

- **Window edges.** Both edges still move to line boundaries within
  reach, so no single-line token straddles a cut; a key that opens
  inside the window and runs past it is caught by the mask's own
  end-of-text alternative; timestamps and colors are stripped before a
  line is classified.
- **Release links.** `url.Parse` refuses backslashes and bad userinfo;
  userinfo, relative and opaque URLs are refused; the origin is compared
  by `instance.SameOrigin`, scheme case-folded and port normalized; the
  URL sent is the parsed one.
- **Issue threads.** The body is `add_comment`'s guarded `body`; the
  thread id is a path argument, escaped once, as for merge requests.
- **Job inputs.** Ship tools only; sent as JSON; a result and a dry run
  name the inputs, not their values.
- **Trigger jobs.** A downstream pipeline's `web_url` is rendered as data
  and never called.
- **Logs and errors.** The new calls add only offsets, sizes and call
  names.
- **Spike O.** It reads and writes only the run's scratch project, and
  cancels the pipeline it started.
