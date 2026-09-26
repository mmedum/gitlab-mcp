# Documentation

For people using the server:

| Document | Read it when |
|---|---|
| [setup.md](setup.md) | registering your OAuth application and logging in, on gitlab.com or a self-managed instance |
| [configuration.md](configuration.md) | changing what the server registers, where it stores things, or how it reaches your instance |
| [runbook.md](runbook.md) | `doctor` reports a failure, or a tool answers `[auth]`, `[rate_limited]` or `[unsupported]` |
| [security.md](security.md) | deciding which flags to turn on, or what is safe to paste |

For people working on it:

| Document | Read it when |
|---|---|
| [architecture.md](architecture.md) | changing the tool surface, sign-in, scopes, the write guards or the kinds; it holds the design, its evidence and the phase plan |
| [development.md](development.md) | running `make check`, adding a tool or an API call, or running the live driver |
| [release.md](release.md) | cutting a release |

Outside this directory: [README](../README.md) for what the server does
and how to install it, [CONTRIBUTING](../CONTRIBUTING.md) for the rules
a change is held to, [SECURITY](../SECURITY.md) for reporting a
vulnerability, and [CHANGELOG](../CHANGELOG.md) for what each release
holds.
