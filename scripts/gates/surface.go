package main

// surfaceCommands are the tool-surface, API and live-driver checks.
func surfaceCommands() map[string]command {
	return map[string]command{
		"api-diff": {run: apiDiff, args: "[tag]", minArgs: 0, maxArgs: 1, mode: modeManual,
			doc:    "Refetch the OpenAPI snapshot at a GitLab release tag (default: the newest vX.Y.Z-ee)",
			reason: "needs the network; `make check` reads what it wrote, and the release checklist runs it"},
		"api-coverage": {run: apiCoverage, args: "", minArgs: 0, maxArgs: 0, mode: modeCheck,
			doc: "Every published operation has a verdict, and every client call a used row"},
		"api-fields": {run: apiFields, args: "", minArgs: 0, maxArgs: 0, mode: modeCheck,
			doc: "Every parameter sent and field decoded exists on its operation, or is excused"},
		"schema-diff": {run: schemaDiff, args: "<bin>", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "The tool surface against the last tag, else the recorded baseline"},
		"schema-baseline": {run: schemaBaseline, args: "<bin>", minArgs: 1, maxArgs: 1, mode: modeManual,
			doc:    "Record the current tool surface as testdata/schema-baseline.json",
			reason: "recording a baseline accepts every change in it, which is a decision, not a check"},
		"schema-ack": {run: schemaAck, args: "<bin> <base-ref> <head>", minArgs: 3, maxArgs: 3, mode: modePR,
			doc:    "A changed tool surface is acknowledged by SCHEMA-CHANGE: or BREAKING CHANGE: in a commit",
			reason: "it measures a pull request from its merge-base, which only a pull request has"},
		"descriptions": {run: descriptions, args: "<schemas.json>", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "Every tool and input is described; witnesses say they are NOT a retry signal"},
		"bodies": {run: bodies, args: "<schemas.json>", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "Every string a write sends goes through the quick-action guard or is listed plain"},
		"classes": {run: classes, args: "", minArgs: 0, maxArgs: 0, mode: modeCheck,
			doc: "The error vocabulary, held against the code and §6.5 both ways"},
		"outcomes": {run: outcomes, args: "", minArgs: 0, maxArgs: 0, mode: modeCheck,
			doc: "Every write result states its outcome, and none from the request alone"},
		"smoke": {run: smoke, args: "<bin>", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "Drive the built binary over stdio at two protocol revisions"},
		"live-cover": {run: liveCover, args: "<bin>", minArgs: 1, maxArgs: 1, mode: modeCheck,
			doc: "Every tool option was driven live, or is waived with a reason"},
	}
}
