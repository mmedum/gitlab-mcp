# Security review — after v2.0.0

Range: `v2.0.0` to `main` after PR #26, the seven features merged since
the release. Each was reviewed on its own branch before it merged, on
2026-09-29 and 2026-09-30; `/code-review high` covered correctness and
a security pass covered this file's questions. `run_merge_request_pipeline`
(#18) is not merged and is reviewed with it.

**Result: no open finding at confidence 8 or above.** Every finding
below was fixed before its pull request merged.

- **Linked items on `get_issue`, `get_merge_request` and `get_commit`**
  (#20): one low finding, fixed. An external tracker's id comes from
  text other people wrote and was shown outside the untrusted boundary;
  it is now kept only in a strict id shape, and anything else reaches
  the model only as the bounded, marked title. Examined and holding: no
  URL from a response is called; references are built by the instance
  resolver, not from a user's text; confidential and unreadable items
  are only ever what GitLab returns for the account.
- **`get_test_report`** (#21): one medium finding, fixed. A suite's
  parse error could quote the report and was not masked; it now goes
  through the same color stripping, hidden-character removal and token
  masking as case output. Low findings, fixed: suite errors are cut
  short and charged to the budget; hidden characters are removed before
  masking, so a token split by a zero-width character is masked; a
  case's output is cut before masking, bounding memory.
- **`list_item_events`** (#22): no finding above low. Examined and
  holding: label names shown as elsewhere, milestone titles inside the
  boundary; the page token is bound to the project, the item and its
  type; the read is capped per kind.
- **`list_boards`** (#23): no finding. Board names and milestone titles
  are inside the boundary; label names and usernames are shown as
  `list_labels` shows them.
- **`track_time`** (#24): no finding with a concrete path. Examined and
  holding: no text body is sent, so no quick action can run; durations
  are checked more strictly than GitLab's parser; spent-time POSTs are
  never retried; the write allow-list applies.
- **`subscribe` and `add_todo`** (#25): no finding with a concrete path.
  Both touch only the account's own notifications and to-dos, and the
  write allow-list is checked before any request. A 304 is an answer
  only where the call says why.
- **`update_snippet` and `delete_snippet`** (#26): one finding, fixed.
  `update_snippet` could write into one of the account's existing public
  or internal snippets, which would publish whatever a persuaded model
  put there, the path `create_snippet` closes by making only private
  snippets. It now refuses any snippet that is not private. Examined
  and holding: own snippets only; the personal route cannot reach a
  project snippet around the write allow-list; `delete_snippet` is
  Destructive, needs `confirm: true` and asks the person, the question
  binding the title and every file name.
- **`run_merge_request_pipeline`** (#18, not merged): no finding with a
  concrete path. It is Ship; a merge request from a fork is refused
  before anything is sent; it asks the person whenever both branches
  may be protected, the condition under which GitLab exposes protected
  variables to a merge request pipeline.
