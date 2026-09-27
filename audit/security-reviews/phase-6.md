# Security review — phase 6

Range: the phase 6 changes on `phase-6`, cut from `phase-5`, run on
2026-09-27 before the simplification fixes were final. Scope: the
seventeen tools of §17.13 — issue moves and links, label and milestone
writes, rebase, cherry-pick, revert, blame, job artifacts and tag writes —
and the live driver's steps for them.

**Result: two findings at confidence 8, both fixed before the commit.**

- **A rebase of a fork's merge request wrote to the fork unchecked
  (medium).** A rebase force-pushes the source branch, which for a fork
  is another project; `rebase_merge_request` held only the target to the
  write allow-list, and checked the branch's protection only in the
  target. Fixed: the source project is resolved as a write target, held
  to the allow-list, refused when more people can see it than the
  target, and its branch checked for protection.
- **`move_issue` compared project visibility alone (low).** A public
  project can keep its issues to members; moving one of those to a public
  project with public issues passed. Fixed: who can see a project's
  issues counts `issues_access_level`, and an unknown visibility refuses
  rather than passes.

Examined and holding:

- **Second projects.** `move_issue`, `link_issues` and `unlink_issues`
  resolve both projects through `writeTarget`; every later call uses the
  resolved id.
- **Protected refs.** `cherry_pick_commit` and `revert_commit` run
  `create_commit`'s guard; `create_tag` reads every protected-tag rule
  and matches as RefMatcher does, refusing when the rules cannot all be
  read; `delete_tag` refuses a protected tag and takes the tag's commit.
- **Artifact and blame paths.** A path is one escaped segment; `.` and
  `..` segments are refused before sending; the directory listed goes in
  the query.
- **Masking and boundaries.** An artifact file is masked as a job log is
  before it is cut; blame lines, titles, descriptions and GitLab's merge
  error are rendered inside untrusted-content boundaries.
- **Quick actions.** No new input reaches an issue or merge request body;
  label, milestone, tag and commit texts are listed in the bodies gate
  with the service that runs no quick action.
- **Kinds.** Move and rebase are Ship; label, milestone and tag deletes
  are Destructive, as §17.13 decided.
