package main

import (
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mmedum/gitlab-mcp/v2/scripts/internal/gatekit"
)

// The release gate holds goreleaser's config against the release
// workflow, the Makefile and the packer's staging table. Every question
// is "do these files name the same thing": a staged glob that matches no
// build directory, a bundle hashed and never uploaded, a checksum file
// with no attestation. Nothing is built; the config and the names are
// static, so this runs on every commit rather than first failing on a
// tag.

const (
	releaseGoreleaser = ".goreleaser.yaml"
	releaseWorkflow   = "release.yml"
)

// releaseExtraFile is one `extra_files` entry.
type releaseExtraFile struct {
	Glob string `yaml:"glob"`
}

// releaseConfig is the part of .goreleaser.yaml the gate reads.
type releaseConfig struct {
	ProjectName string `yaml:"project_name"`
	Before      struct {
		Hooks []string `yaml:"hooks"`
	} `yaml:"before"`
	Builds []struct {
		ID     string   `yaml:"id"`
		Binary string   `yaml:"binary"`
		Goos   []string `yaml:"goos"`
		Goarch []string `yaml:"goarch"`
		Ignore []struct {
			Goos   string `yaml:"goos"`
			Goarch string `yaml:"goarch"`
		} `yaml:"ignore"`
		Env          []string `yaml:"env"`
		Flags        []string `yaml:"flags"`
		ModTimestamp string   `yaml:"mod_timestamp"`
		Ldflags      []string `yaml:"ldflags"`
	} `yaml:"builds"`
	UniversalBinaries []struct {
		ID           string   `yaml:"id"`
		IDs          []string `yaml:"ids"`
		Replace      bool     `yaml:"replace"`
		NameTemplate string   `yaml:"name_template"`
		Hooks        struct {
			Post string `yaml:"post"`
		} `yaml:"hooks"`
	} `yaml:"universal_binaries"`
	Archives []struct {
		IDs             []string `yaml:"ids"`
		Formats         []string `yaml:"formats"`
		Files           []string `yaml:"files"`
		FormatOverrides []struct {
			Goos    string   `yaml:"goos"`
			Formats []string `yaml:"formats"`
		} `yaml:"format_overrides"`
	} `yaml:"archives"`
	Checksum struct {
		NameTemplate string             `yaml:"name_template"`
		ExtraFiles   []releaseExtraFile `yaml:"extra_files"`
	} `yaml:"checksum"`
	SBOMs []releaseSBOM `yaml:"sboms"`
	Signs []struct {
		Cmd       string   `yaml:"cmd"`
		Signature string   `yaml:"signature"`
		Args      []string `yaml:"args"`
		Artifacts string   `yaml:"artifacts"`
	} `yaml:"signs"`
	// Present at all is the defect: `changelog: disable: true` skips the
	// pipe that reads --release-notes, and the body collapses to the
	// footer while every step stays green.
	Changelog *yaml.Node `yaml:"changelog"`
	Release   struct {
		Draft      bool               `yaml:"draft"`
		Prerelease string             `yaml:"prerelease"`
		ExtraFiles []releaseExtraFile `yaml:"extra_files"`
		Footer     string             `yaml:"footer"`
	} `yaml:"release"`
}

// releaseSBOM is one `sboms` entry.
type releaseSBOM struct {
	Artifacts string `yaml:"artifacts"`
}

// releaseTarget is one directory a build writes under dist/.
type releaseTarget struct {
	goos, goarch string
	// dir is goreleaser's documented `<id>_<goos>_<goarch>`. A real
	// build appends a variant (`_v1`), deliberately not modeled: a glob
	// that hardcodes it should fail here.
	dir    string
	binary string
}

// releaseManifestPlatform maps a manifest platform to a GOOS.
var releaseManifestPlatform = map[string]string{"darwin": "darwin", "win32": "windows", "linux": "linux"}

func releaseGate(out io.Writer, _ []string) error {
	return releaseCheck(out, ".")
}

func releaseCheck(out io.Writer, root string) error {
	cfg, flow, mcpbOut, err := releaseRead(root)
	if err != nil {
		return err
	}
	owner, repo, err := serverJSONOwnerRepo(root)
	if err != nil {
		return err
	}
	staged := mcpbFiles
	problems, targets := releaseValidate(cfg, staged, mcpbOut, flow)
	problems = append(problems, releaseMoreProblems(cfg, owner, repo, mcpbOut)...)
	problems = append(problems, releaseWorkflowShape(flow)...)
	slices.Sort(problems)
	if err := gatekit.Problems(out, "the release wiring", problems); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "release ok: %d build targets, %d staged globs each resolve to one, bundle %s signed, "+
		"uploaded and attested\n", len(targets), releaseGlobCount(staged), releaseNormalVersion(mcpbOut))
	return nil
}

// releaseRead loads the three files the gate compares.
func releaseRead(root string) (releaseConfig, workflow, string, error) {
	var cfg releaseConfig
	data, err := os.ReadFile(filepath.Join(root, releaseGoreleaser))
	if err != nil {
		return cfg, workflow{}, "", err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, workflow{}, "", fmt.Errorf("%s is not valid YAML: %w", releaseGoreleaser, err)
	}
	flow, err := readWorkflow(filepath.Join(root, workflowDir, releaseWorkflow), workflowDir+"/"+releaseWorkflow)
	if err != nil {
		return cfg, workflow{}, "", err
	}
	mk, err := readMakefile(filepath.Join(root, parityMakefile))
	if err != nil {
		return cfg, workflow{}, "", err
	}
	mcpbOut, err := mk.variable("MCPB_OUT")
	if err != nil {
		return cfg, workflow{}, "", err
	}
	return cfg, flow, mcpbOut, nil
}

// releaseValidate is the checks, over arguments, so a test can break
// each one without touching the repository.
func releaseValidate(cfg releaseConfig, files []mcpbStaged, mcpbOut string, flow workflow) ([]string, []releaseTarget) {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	targets := releaseTargets(cfg)
	releaseCheckFloors(targets, files, fail)
	releaseCheckGlobs(targets, files, fail)
	releaseCheckUniversal(cfg, mcpbOut, fail)
	releaseCheckArchives(cfg, mcpbOut, fail)
	releaseCheckBuilds(cfg, fail)

	// An SBOM per archive, and a keyless cosign bundle over the
	// checksums.
	if !slices.ContainsFunc(cfg.SBOMs, func(s releaseSBOM) bool { return s.Artifacts == "archive" }) {
		fail("no sboms block over the archives")
	}
	problems = append(problems, releaseSigns(cfg)...)
	if cfg.Release.Draft {
		fail("release.draft is true, so a tag publishes nothing until somebody presses a button")
	}
	if cfg.Changelog != nil {
		fail("there is a changelog block; `disable` skips the pipe that reads --release-notes, so delete it")
	}
	problems = append(problems, releaseWorkflowProblems(cfg, flow)...)
	slices.Sort(problems)
	return problems, targets
}

// releaseCheckFloors are the floors, as problems so a test can reach
// them: six platform archives, and a staging table that is the bundle.
func releaseCheckFloors(targets []releaseTarget, files []mcpbStaged, fail func(format string, a ...any)) {
	platforms := 0
	for _, t := range targets {
		if t.goarch != "all" {
			platforms++
		}
	}
	if platforms < 6 {
		fail("%s builds %d platform targets and §12 promises six archives", releaseGoreleaser, platforms)
	}
	if releaseGlobCount(files) < 4 {
		fail("the packer stages %d binaries; that is not the bundle", releaseGlobCount(files))
	}
}

// releaseCheckGlobs holds that every staged glob resolves to exactly one
// build target, the binary that target writes, on the platform it is
// staged for.
func releaseCheckGlobs(targets []releaseTarget, files []mcpbStaged, fail func(format string, a ...any)) {
	for _, f := range files {
		if f.glob == "" {
			continue
		}
		dir, base := path.Split(strings.TrimPrefix(f.glob, "dist/"))
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" || base == "" {
			fail("%s: the glob %q is not <directory>/<binary>", f.path, f.glob)
			continue
		}
		var matched []releaseTarget
		for _, t := range targets {
			if ok, err := path.Match(dir, t.dir); err == nil && ok {
				matched = append(matched, t)
			}
		}
		if len(matched) != 1 {
			fail("%s: the glob %q matches %d build directories (%s builds %s); it must match exactly one",
				f.path, f.glob, len(matched), releaseGoreleaser, releaseTargetNames(targets))
			continue
		}
		t := matched[0]
		if base != t.binary {
			fail("%s: the glob ends in %q and %s/%s writes %q", f.path, base, t.goos, t.goarch, t.binary)
		}
		if want := releaseManifestPlatform[f.platform]; f.platform != "" && want != t.goos {
			fail("%s is the entry point for %q and its glob resolves to %s/%s", f.path, f.platform, t.goos, t.goarch)
		}
		if f.goos != "" && (f.goos != t.goos || f.goarch != t.goarch) {
			fail("%s says it runs on %s/%s and its glob resolves to %s/%s", f.path, f.goos, f.goarch, t.goos, t.goarch)
		}
	}
}

// releaseCheckUniversal holds that there is one universal binary, joined
// from darwin builds, whose post hook packs the bundle to the Makefile's
// path: the one point where every binary exists and checksums.txt is not
// yet written.
func releaseCheckUniversal(cfg releaseConfig, mcpbOut string, fail func(format string, a ...any)) {
	switch len(cfg.UniversalBinaries) {
	case 1:
		u := cfg.UniversalBinaries[0]
		hook := u.Hooks.Post
		switch {
		case !strings.Contains(hook, "scripts/gates mcpb-pack"):
			fail("the universal binary's post hook does not run `gates mcpb-pack`: %q", hook)
		case !strings.Contains(releaseNormalVersion(hook), releaseNormalVersion(mcpbOut)):
			fail("the post hook packs to a path the Makefile's MCPB_OUT (%q) does not name: %q", mcpbOut, hook)
		}
		if u.Replace {
			fail("universal_binaries.replace is true, which removes the per-architecture macOS archives")
		}
		if len(u.IDs) == 0 {
			fail("the universal binary names no ids")
		}
		if !releaseBuildsDarwin(cfg, u.IDs) {
			fail("the universal binary joins %v, which goreleaser does not build for darwin", u.IDs)
		}
	case 0:
		fail("no universal_binaries block: a manifest names one command per platform, so macOS needs one binary for both")
	default:
		fail("%d universal binaries; the bundle stages one macOS file", len(cfg.UniversalBinaries))
	}
}

// releaseCheckArchives holds that the bundle is checksummed and
// uploaded, both, or it ships unsigned or is hashed and never published;
// and that the archives keep the universal binary out, by ids.
func releaseCheckArchives(cfg releaseConfig, mcpbOut string, fail func(format string, a ...any)) {
	bundle := releaseNormalVersion(mcpbOut)
	if !releaseCovers(cfg.Checksum.ExtraFiles, bundle) {
		fail("checksum.extra_files does not cover %s, so the bundle is not under the signature", mcpbOut)
	}
	if !releaseCovers(cfg.Release.ExtraFiles, bundle) {
		fail("release.extra_files does not cover %s, so the bundle is hashed and never uploaded", mcpbOut)
	}

	if len(cfg.Archives) == 0 {
		fail("no archives block")
	}
	for i, a := range cfg.Archives {
		if len(a.IDs) == 0 {
			fail("archives[%d] names no ids, so it archives the universal binary as well", i)
		}
		for _, u := range cfg.UniversalBinaries {
			if slices.Contains(a.IDs, u.ID) {
				fail("archives[%d] includes the universal binary %q", i, u.ID)
			}
		}
	}
}

// releaseCheckBuilds holds that every build is reproducible and stamped.
func releaseCheckBuilds(cfg releaseConfig, fail func(format string, a ...any)) {
	for _, h := range cfg.Before.Hooks {
		if strings.Contains(h, "mod tidy") {
			fail("a before hook runs %q; a hook that rewrites go.mod dirties the tree and only the tag notices", h)
		}
	}
	for _, b := range cfg.Builds {
		if !slices.Contains(b.Flags, "-trimpath") {
			fail("build %q does not pass -trimpath", b.ID)
		}
		if !strings.Contains(b.ModTimestamp, "CommitTimestamp") {
			fail("build %q does not stamp mod_timestamp from the commit, so a tag does not rebuild byte for byte", b.ID)
		}
		ld := strings.Join(b.Ldflags, " ")
		if !strings.Contains(ld, "version.Version={{ .Version }}") && !strings.Contains(ld, "version.Version={{.Version}}") {
			fail("build %q does not stamp internal/version.Version with {{ .Version }}", b.ID)
		}
	}
}

// releaseWorkflowProblems holds release.yml to the config.
func releaseWorkflowProblems(cfg releaseConfig, flow workflow) []string {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if !flow.triggersOnTag("v") {
		fail("%s does not trigger on a v* tag", flow.path)
	}
	var tested, writesNotes, passesNotes, cosign, syft bool
	repro, attested := 0, false
	for _, s := range flow.steps() {
		lines := strings.Join(workflowRunLines(s.Run), "\n")
		if strings.Contains(lines, "go test") && !passesNotes {
			tested = true
		}
		if strings.Contains(lines, "scripts/gates release-notes") {
			writesNotes = true
		}
		switch a := s.action(); {
		case a == "sigstore/cosign-installer":
			_, cosign = s.input("cosign-release")
		case strings.HasPrefix(a, "anchore/sbom-action"):
			_, syft = s.input("syft-version")
		case a == "goreleaser/goreleaser-action":
			args, _ := s.input("args")
			if strings.Contains(args, "--release-notes") {
				passesNotes = true
			}
			if strings.Contains(args, "--single-target") {
				repro++
			}
		case a == "actions/attest-build-provenance":
			attested = true
			subjects, _ := s.input("subject-path")
			problems = append(problems, releaseSubjectProblems(subjects, cfg.Checksum.NameTemplate)...)
		}
	}
	if !tested {
		fail("%s runs no `go test` before goreleaser signs", flow.path)
	}
	if !writesNotes {
		fail("%s never runs `gates release-notes`, so nothing writes the notes file", flow.path)
	}
	if !passesNotes {
		fail("%s does not pass --release-notes to goreleaser, so the body is built from commit subjects", flow.path)
	}
	if repro < 2 {
		fail("%s builds one target %d time(s); the reproducible-build check needs two builds to compare", flow.path, repro)
	}
	if !cosign {
		fail("%s installs no cosign with a cosign-release, and the config signs with cosign", flow.path)
	}
	if !syft {
		fail("%s installs no syft with a syft-version, and the config writes SBOMs", flow.path)
	}
	if !attested {
		fail("%s runs no build-provenance attestation", flow.path)
	}
	return problems
}

// releaseSubjectProblems holds the attestation's subject-path: a comma-
// or newline-separated list naming the archives, the checksum file and
// the bundle. A space-separated value is one literal glob.
func releaseSubjectProblems(subjects, checksum string) []string {
	var problems []string
	parts := strings.FieldsFunc(subjects, func(r rune) bool { return r == ',' || r == '\n' })
	for _, p := range parts {
		if strings.ContainsAny(strings.TrimSpace(p), " \t") {
			problems = append(problems, fmt.Sprintf("subject-path %q separates with a space, which is read as one "+
				"glob; separate with commas or newlines", subjects))
			break
		}
	}
	has := func(want string) bool {
		return slices.ContainsFunc(parts, func(p string) bool { return strings.Contains(p, want) })
	}
	for _, want := range []string{".tar.gz", ".zip", ".mcpb", checksum} {
		if want != "" && !has(want) {
			problems = append(problems, fmt.Sprintf("subject-path %q does not name %s, so it carries no attestation",
				subjects, want))
		}
	}
	return problems
}

// releaseSigns holds the signing block, which no rehearsal exercises:
// keyless signing needs a workflow's OIDC token.
func releaseSigns(cfg releaseConfig) []string {
	var problems []string
	signed := false
	for _, s := range cfg.Signs {
		if s.Artifacts != "checksum" {
			continue
		}
		signed = true
		args := strings.Join(s.Args, " ")
		switch {
		case s.Cmd != "cosign":
			problems = append(problems, fmt.Sprintf("the checksum file is signed with %q, not cosign", s.Cmd))
		case !strings.HasSuffix(s.Signature, ".bundle"):
			problems = append(problems, fmt.Sprintf("the signature is written to %q; cosign 3 writes one .bundle", s.Signature))
		case !strings.Contains(args, "--bundle"):
			problems = append(problems, "the signature does not pass --bundle, which cosign 3 requires")
		case !strings.Contains(args, "--yes"):
			problems = append(problems, "the signature does not pass --yes, so cosign waits for a confirmation")
		}
	}
	if !signed {
		problems = append(problems, "nothing signs the checksum file")
	}
	return problems
}

// releaseTargets is every directory a build writes, the universal one
// included.
func releaseTargets(cfg releaseConfig) []releaseTarget {
	var targets []releaseTarget
	for _, b := range cfg.Builds {
		ignored := map[string]bool{}
		for _, ig := range b.Ignore {
			ignored[ig.Goos+"/"+ig.Goarch] = true
		}
		for _, goos := range b.Goos {
			for _, goarch := range b.Goarch {
				if ignored[goos+"/"+goarch] {
					continue
				}
				binary := b.Binary
				if goos == "windows" {
					binary += ".exe"
				}
				targets = append(targets, releaseTarget{goos: goos, goarch: goarch, binary: binary,
					dir: b.ID + "_" + goos + "_" + goarch})
			}
		}
	}
	for _, u := range cfg.UniversalBinaries {
		// The file inside is named by name_template, which defaults to
		// the project name, not the build's binary.
		name := cfg.ProjectName
		if u.NameTemplate != "" {
			name = strings.NewReplacer("{{ .ProjectName }}", cfg.ProjectName, "{{.ProjectName}}", cfg.ProjectName).
				Replace(u.NameTemplate)
		}
		targets = append(targets, releaseTarget{goos: "darwin", goarch: "all", binary: name, dir: u.ID + "_darwin_all"})
	}
	return targets
}

func releaseTargetNames(targets []releaseTarget) string {
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.dir)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// releaseCovers reports whether an extra_files glob covers a path.
func releaseCovers(files []releaseExtraFile, want string) bool {
	want = path.Clean(want)
	for _, f := range files {
		if ok, err := path.Match(path.Clean(f.Glob), want); err == nil && ok {
			return true
		}
	}
	return false
}

func releaseBuildsDarwin(cfg releaseConfig, ids []string) bool {
	for _, b := range cfg.Builds {
		if slices.Contains(ids, b.ID) && slices.Contains(b.Goos, "darwin") {
			return true
		}
	}
	return false
}

func releaseGlobCount(files []mcpbStaged) int {
	n := 0
	for _, f := range files {
		if f.glob != "" {
			n++
		}
	}
	return n
}

// releaseNormalVersion reduces make's $(VERSION) and goreleaser's
// {{ .Version }} to one spelling, so two paths can be compared.
func releaseNormalVersion(s string) string {
	return strings.NewReplacer("$(VERSION)", "<version>", "{{ .Version }}", "<version>", "{{.Version}}", "<version>").Replace(s)
}

// releaseArchiveFiles are the files an archive may carry beside the
// binary, each with whether it is required. NOTICE travels with the
// binary as Apache-2.0 section 4(d) asks.
var releaseArchiveFiles = map[string]bool{"LICENSE": true, "README.md": true, "NOTICE": false}

// releaseBeforeHooks are the before hooks allowed, each with the reason
// it leaves the tree as it was. goreleaser refuses to release from a
// dirty tree and `--snapshot` skips that check, so a hook that writes
// passes every rehearsal and fails the tag.
var releaseBeforeHooks = map[string]string{
	"go mod download": "fills the module cache, outside the tree",
}

// releaseMoreProblems are the checks beyond the sibling set: the
// environment and flags every build carries, the archive contents, the
// hooks, the release block and the footer's verification commands.
func releaseMoreProblems(cfg releaseConfig, owner, repo, mcpbOut string) []string {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	for _, h := range cfg.Before.Hooks {
		if _, ok := releaseBeforeHooks[strings.TrimSpace(h)]; !ok {
			fail("the before hook %q is not one releaseBeforeHooks knows leaves the tree clean", h)
		}
	}
	for _, b := range cfg.Builds {
		if !slices.Contains(b.Env, "CGO_ENABLED=0") {
			fail("build %q does not set CGO_ENABLED=0", b.ID)
		}
		ld := strings.Join(b.Ldflags, " ")
		if !strings.Contains(ld, "-s -w") {
			fail("build %q does not strip with -s -w", b.ID)
		}
		if strings.Contains(ld, ".Tag") {
			fail("build %q stamps {{ .Tag }}, which is v0.0.0 in a snapshot; stamp {{ .Version }}", b.ID)
		}
	}
	for i, a := range cfg.Archives {
		for _, f := range a.Files {
			if _, ok := releaseArchiveFiles[f]; !ok {
				fail("archives[%d] carries %s, which releaseArchiveFiles does not allow", i, f)
			}
		}
		for f, required := range releaseArchiveFiles {
			if required && !slices.Contains(a.Files, f) {
				fail("archives[%d] does not carry %s", i, f)
			}
		}
		zip := false
		for _, o := range a.FormatOverrides {
			zip = zip || (o.Goos == "windows" && slices.Contains(o.Formats, "zip"))
		}
		if !zip {
			fail("archives[%d] does not zip the Windows build", i)
		}
	}
	if cfg.Release.Prerelease != "auto" {
		fail("release.prerelease is %q; `auto` marks a -rc tag a prerelease, which keeps it out of the registry", cfg.Release.Prerelease)
	}
	problems = append(problems, releaseFooterProblems(cfg.Release.Footer, owner, repo, mcpbOut)...)
	return problems
}

// releaseFooterProblems holds the verification commands the release
// page prints to what the release actually signs and attests: the
// certificate identity exactly, never a regexp, and the artifacts by the
// names goreleaser and the packer give them.
func releaseFooterProblems(footer, owner, repo, mcpbOut string) []string {
	var problems []string
	identity := fmt.Sprintf("--certificate-identity 'https://github.com/%s/%s/.github/workflows/release.yml@refs/tags/{{ .Tag }}'", owner, repo)
	if !strings.Contains(footer, identity) {
		problems = append(problems, "the release footer does not verify with "+identity)
	}
	if strings.Contains(footer, "identity-regexp") {
		problems = append(problems, "the release footer verifies with a certificate-identity regexp; name the identity exactly")
	}
	if !strings.Contains(footer, "--bundle checksums.txt.bundle") {
		problems = append(problems, "the release footer does not verify checksums.txt against checksums.txt.bundle, which is what the config signs")
	}
	bundle := path.Base(releaseNormalVersion(mcpbOut))
	archive := gatekit.BinaryName + "_<version>_linux_amd64.tar.gz"
	for _, want := range []string{bundle, archive} {
		if !strings.Contains(releaseNormalVersion(footer), "gh attestation verify "+want) {
			problems = append(problems, fmt.Sprintf("the release footer does not show `gh attestation verify %s`", want))
		}
	}
	return problems
}

// releaseWorkflowShape holds release.yml's own shape: runnable by hand
// on a tag, read-only by default with write raised in the one job that
// publishes, refusing a commit whose ci run is not green, notes written
// outside the checkout, and no registry entry for a prerelease.
func releaseWorkflowShape(flow workflow) []string {
	var problems []string
	fail := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }
	if workflowMappingValue(&flow.On, "workflow_dispatch") == nil {
		fail("%s has no workflow_dispatch, so a release that failed for another reason cannot be run again", flow.path)
	}
	if top := workflowPermissions(&flow.Permissions); len(top) != 1 || top["contents"] != "read" {
		fail("%s's top-level permissions are %v, want contents: read alone", flow.path, top)
	}
	writers, verifies, registry := 0, "", false
	for _, name := range slices.Sorted(maps.Keys(flow.Jobs)) {
		job := flow.Jobs[name]
		for _, s := range job.Steps {
			run := strings.Join(workflowRunLines(s.Run), "\n")
			if strings.Contains(run, "actions/workflows/ci.yml/runs") && workflowPermissions(&job.Permissions)["actions"] == "read" {
				verifies = name
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(flow.Jobs)) {
		job := flow.Jobs[name]
		perms := workflowPermissions(&job.Permissions)
		if perms["contents"] == "write" {
			writers++
			if !releaseJobRunsGoreleaser(job) {
				fail("%s job %s raises contents: write and does not run goreleaser", flow.path, name)
			}
			if !slices.Contains(releaseNeeds(job), verifies) || verifies == "" {
				fail("%s job %s publishes without needing the job that checks ci is green", flow.path, name)
			}
		}
		for _, s := range job.Steps {
			run := strings.Join(workflowRunLines(s.Run), "\n")
			if strings.Contains(run, "scripts/gates release-notes") && !strings.Contains(run, "RUNNER_TEMP") {
				fail("%s writes the release notes inside the checkout, which makes goreleaser's tree dirty", flow.path)
			}
		}
		if strings.Contains(job.Uses, "publish-mcp.yml") {
			registry = true
			if !strings.Contains(job.If, "!contains(github.ref_name, '-')") {
				fail("%s job %s publishes to the registry without skipping a prerelease tag", flow.path, name)
			}
		}
	}
	if verifies == "" {
		fail("%s has no job with actions: read that queries this commit's ci run", flow.path)
	}
	if writers != 1 {
		fail("%s raises contents: write in %d jobs, want exactly one", flow.path, writers)
	}
	if !registry {
		fail("%s never calls publish-mcp.yml", flow.path)
	}
	return problems
}

func releaseJobRunsGoreleaser(job workflowJob) bool {
	for _, s := range job.Steps {
		if s.action() == "goreleaser/goreleaser-action" {
			return true
		}
	}
	return false
}

// releaseNeeds is a job's needs, one name or a list.
func releaseNeeds(job workflowJob) []string {
	switch job.Needs.Kind {
	case yaml.ScalarNode:
		return []string{job.Needs.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(job.Needs.Content))
		for _, n := range job.Needs.Content {
			out = append(out, n.Value)
		}
		return out
	}
	return nil
}
