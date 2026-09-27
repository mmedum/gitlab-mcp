package main

// repoCommands are the repository, release and supply-chain checks.
func repoCommands() map[string]command {
	return map[string]command{
		// The pipeline.
		"parity": {run: parity, maxArgs: 0, mode: modeCheck,
			doc: "`make check` and CI run the same set, and every gate runs where the registry says"},
		"checklist": {run: checklist, maxArgs: 0, mode: modeCheck,
			doc: "CLAUDE.md's definition of done against the Makefile's check: prerequisites"},
		"pins": {run: pins, maxArgs: 0, mode: modeCheck,
			doc: "every action by SHA and every tool at one exact version, the same everywhere"},
		"precommit": {run: precommit, maxArgs: 0, mode: modeManual,
			reason: "run by .githooks/pre-commit on a maintainer's commit, not by a pipeline",
			doc:    "gofmt, vet, leaks and gitleaks over the staged tree, every problem collected"},
		"coverage": {run: coverage, args: "PROFILE", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "statement coverage per package, scored on its own files, with floors"},

		// Documents.
		"staleness": {run: staleness, args: "[BINARY]", maxArgs: 1, mode: modeCheck,
			doc: "the docs held to what the code defines"},
		"changelog-links": {run: changelogLinks, maxArgs: 0, mode: modeCheck,
			doc: "every CHANGELOG version heading has its link reference"},
		"changelog": {run: changelog, args: "BASE HEAD", minArgs: 2, maxArgs: 2, mode: modePR,
			reason: "measures a pull request from its merge-base, which only a pull request has",
			doc:    "a pull request adds a CHANGELOG entry unless it cuts a release"},
		"merge-base": {run: mergeBase, args: "BASE HEAD", minArgs: 2, maxArgs: 2, mode: modePR,
			reason: "prints the commit a pull request is measured from; nothing to measure outside one",
			doc:    "print the merge-base of HEAD and the base branch as it is now"},
		"release-notes": {run: releaseNotes, args: "VERSION", minArgs: 1, maxArgs: 1, mode: modeRelease,
			reason: "release.yml writes the release body with it before goreleaser runs",
			doc:    "one version's CHANGELOG section, verbatim, which is the release note"},

		// Confidentiality.
		"leaks": {run: leaks, maxArgs: 0, mode: modeCheck,
			doc: "nothing from a real instance or account is in the tree"},
		"leaks-history": {run: leaksHistory, maxArgs: 0, mode: modeManual,
			reason: "reads every blob, commit message and tag in the clone; slow, and before going public",
			doc:    "the leak rules over every commit, commit message and tag"},
		"transcript": {run: transcript, maxArgs: 0, mode: modeCheck,
			doc: "the drivers reach a terminal only through the redacting printer"},

		// Release.
		"mcpb": {run: mcpb, maxArgs: 0, mode: modeCheck,
			doc: "the committed bundle manifest against its vendored schema and the staging table"},
		"mcpb-pack": {run: mcpbPack, args: "DIST VERSION OUT", minArgs: 3, maxArgs: 3, mode: modeRelease,
			reason: "needs every built binary: the universal binary's post hook in .goreleaser.yaml",
			doc:    "pack the Claude Desktop bundle from the binaries goreleaser built"},
		"release": {run: releaseGate, maxArgs: 0, mode: modeCheck,
			doc: "goreleaser's config held against the release workflow and the packer"},
		"server-json": {run: serverJSON, args: "[TAG CHECKSUMS]", maxArgs: 2, mode: modeCheck,
			doc: "the registry entry against the vendored schema and the registry's rules; " +
				"with TAG CHECKSUMS, print the entry to publish"},
		"schema-refetch": {run: schemaRefetch, maxArgs: 0, mode: modeManual,
			reason: "needs the network; it compares, writes nothing, and is in the release checklist",
			doc:    "the vendored schemas against what their sources serve now"},
		"deps": {run: deps, maxArgs: 0, mode: modeManual,
			reason: "needs the module proxy; it is in the release checklist",
			doc:    "every direct dependency updated within six months or pinned with a reason"},
	}
}
