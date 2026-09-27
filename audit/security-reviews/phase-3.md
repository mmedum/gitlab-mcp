# Security review — phase 3

Range: the phase 2 commits on `phase-3`'s base to the phase 3 build,
run with `/security-review` on 2026-09-27 after the simplification pass.
Scope: the Go changes that ship (`internal/**`), the gates that hold
them, and the live driver.

**Result: no finding at confidence 8 or above.** Two were examined
below it and are recorded here.

Examined and holding:

- **The allow-list.** Every new write resolves its project through
  `writeTarget` and is held to `GITLAB_MCP_WRITE_NAMESPACES`: merge,
  approve and unapprove, the five CI calls, `create_release`, the wiki
  writes, both deletes and a project snippet. A personal snippet has no
  project; it is refused whenever the allow-list is set, and is always
  private, which the server checks against GitLab's answer.
- **Kinds.** The CI calls, merging, approving and `create_release` are
  Ship; the three deletes are Destructive and refused without
  `confirm: true`. No Write tool reaches a Ship action.
- **Deletes.** `delete_branch` refuses the default branch, protected
  branches, and one GitLab does not count merged unless `unmerged` is
  set, and needs the head `sha`. `delete_comment` refuses system notes
  and other people's comments, whoever GitLab would allow, and sends
  `If-Unmodified-Since` (spike N). `delete_wiki_page` needs a hash of
  the content read.
- **Dry runs.** Every new write checks the dry run before its first
  write, with the client's refusal behind it; previews name fields,
  never values.
- **Secrets.** Pipeline and job variable values and input values are
  sent and never shown: results, previews and logs carry keys.
- **Quick actions.** Merge and squash commit messages, wiki content, a
  snippet's description and a release's name, notes and tag message are
  plain inputs; none of the services they reach interprets quick
  actions (§18 row 67).
- **Untrusted content.** Page content, snippet files and descriptions,
  release names and notes, and event titles render inside the call's
  boundary; branch, environment, job and slug names and external URLs
  are made visible. No `raw_url` or `external_url` is ever called.

Below the threshold:

1. **Closing keywords in a merge commit message (confidence 4).** A
   persuaded merge could carry "Closes other-group/project#N". Checked
   against `app/workers/process_commit_worker.rb` and
   `app/services/git/branch_hooks_service.rb` at v19.4.1-ee: a merge
   commit landing on the default branch from a merge request is skipped
   for closing, which `PostMergeService` does from the merge request's
   own description and commits, and a commit on another branch closes
   nothing. A mention does create GitLab's own "mentioned in" system
   note on the issue it names, as every body has since phase 2; that is
   GitLab's record of a reference, not a write this server sends.
2. **The source branch deleted on merge.** `remove_source_branch` is
   GitLab's merge behavior under Ship, and GitLab does not delete a
   protected source branch.
