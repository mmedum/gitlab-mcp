## Summary

<!-- One to three sentences: what changed and why. -->

## Test plan

- [ ] `make check`
- [ ] Tests for the new behavior
- [ ] A live run with its transcript read, if this touches sign-in, the quick-action guard, diff positions, a write's witness or an API response shape
- [ ] CHANGELOG entry under `[Unreleased]`, if this is user-visible
- [ ] Nothing from a real GitLab instance anywhere in the diff, the commits or this description

## Notes

<!-- Optional: follow-ups, caveats, or context a reviewer needs first. -->

<!--
If the tool surface changed (the schema artifact in CI shows it), add an
empty commit to this pull request carrying a footer. Never amend:

  git commit --allow-empty -m "Acknowledge the tool surface change" \
    -m "SCHEMA-CHANGE: <what was added>"

Use `BREAKING CHANGE: <what breaks>` instead for a removed or renamed
tool, a lost output field or a new required input. CI fails a changed
surface that carries neither.
-->
