package gitlabtest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
)

// Branches and commits. A commit applies its actions atomically, checks
// each file's witness against the last commit that touched it (§2.10),
// and moves every open merge request whose source is that branch, as a
// push does.

// ------------------------------------------------------------ diffs

func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(content, "\n"), "\n")
}

// hunkRange is git's: the count is left out when it is one, and an
// empty side starts at the line before it.
func hunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return fmt.Sprint(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

// unified is a one-hunk unified diff of two texts, with up to three
// lines of context, and its added and removed line counts.
func unified(before, after string) (string, int, int) {
	a, b := splitLines(before), splitLines(after)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	if pre == len(a) && pre == len(b) {
		return "", 0, 0
	}
	const context = 3
	from := max(pre-context, 0)
	aEnd, bEnd := len(a)-suf, len(b)-suf
	tail := min(suf, context)
	var out strings.Builder
	fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(from, aEnd+tail-from), hunkRange(from, bEnd+tail-from))
	for _, l := range a[from:pre] {
		out.WriteString(" " + l + "\n")
	}
	for _, l := range a[pre:aEnd] {
		out.WriteString("-" + l + "\n")
	}
	for _, l := range b[pre:bEnd] {
		out.WriteString("+" + l + "\n")
	}
	for _, l := range a[aEnd : aEnd+tail] {
		out.WriteString(" " + l + "\n")
	}
	return out.String(), bEnd - pre, aEnd - pre
}

// fileDiff is one file's change; an absent side is a missing file.
func fileDiff(oldPath, newPath string, before, after *string) (gitlab.Diff, int, int) {
	d := gitlab.Diff{OldPath: oldPath, NewPath: newPath, AMode: "100644", BMode: "100644"}
	var a, b string
	switch {
	case before == nil:
		d.AMode, d.NewFile, b = "0", true, *after
	case after == nil:
		d.BMode, d.DeletedFile, a = "0", true, *before
	default:
		a, b = *before, *after
	}
	d.RenamedFile = oldPath != newPath
	if strings.Contains(a+b, "\x00") {
		d.Diff = fmt.Sprintf("Binary files a/%s and b/%s differ\n", oldPath, newPath)
		return d, 0, 0
	}
	var adds, dels int
	d.Diff, adds, dels = unified(a, b)
	return d, adds, dels
}

// treeDiffs is every file that differs between two trees, by path.
func treeDiffs(from, to map[string]string) []gitlab.Diff {
	paths := map[string]bool{}
	for k := range from {
		paths[k] = true
	}
	for k := range to {
		paths[k] = true
	}
	sorted := make([]string, 0, len(paths))
	for k := range paths {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	out := []gitlab.Diff{}
	for _, path := range sorted {
		a, inA := from[path]
		b, inB := to[path]
		if inA && inB && a == b {
			continue
		}
		var before, after *string
		if inA {
			before = &a
		}
		if inB {
			after = &b
		}
		d, _, _ := fileDiff(path, path, before, after)
		out = append(out, d)
	}
	return out
}

// ------------------------------------------------------------ witnesses

// branchOf is the branch a ref reads: the branch itself, or the branch a
// commit heads.
func branchOf(p *project, ref string) string {
	if ref == "HEAD" || ref == "" {
		return p.DefaultBranch
	}
	if _, ok := p.trees[ref]; ok {
		return ref
	}
	for branch, commits := range p.commits {
		if len(commits) > 0 && (commits[0].ID == ref || commits[0].ShortID == ref) {
			return branch
		}
	}
	return ""
}

// lastCommit is the last commit that touched a file on a branch. A
// branch no write has touched counts its head for every file, which is
// what the file read has always answered.
func lastCommit(p *project, branch, path string) string {
	if c, ok := p.fileCommits[branch][path]; ok {
		return c
	}
	if len(p.commits[branch]) == 0 {
		return ""
	}
	return p.commits[branch][0].ID
}

// pinFileCommits records every file's last commit on a branch before the
// branch first moves, so a file the move does not touch keeps it.
func pinFileCommits(p *project, branch string) map[string]string {
	m := p.fileCommits[branch]
	if m == nil {
		m = map[string]string{}
		for path := range p.trees[branch] {
			m[path] = lastCommit(p, branch, path)
		}
		p.fileCommits[branch] = m
	}
	return m
}

// ------------------------------------------------------------ branches

// protectedName reports whether a protected-branch rule covers a name.
func protectedName(p *project, name string) bool {
	return slices.ContainsFunc(p.protected, func(rule gitlab.ProtectedBranch) bool { return wildcard(rule.Name, name) })
}

// wildcard is GitLab's branch pattern: * matches any run of characters,
// slashes included, and nothing else is special.
func wildcard(pattern, name string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == name
	}
	rest, ok := strings.CutPrefix(name, parts[0])
	if !ok {
		return false
	}
	for _, part := range parts[1 : len(parts)-1] {
		j := strings.Index(rest, part)
		if j < 0 {
			return false
		}
		rest = rest[j+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// setBranch adds or moves a branch in the list the branches read serves.
func setBranch(p *project, name string) gitlab.Branch {
	protected := protectedName(p, name)
	b := gitlab.Branch{Name: name, Default: name == p.DefaultBranch, Protected: protected, CanPush: true,
		DevelopersCanPush: !protected, DevelopersCanMerge: !protected, WebURL: p.WebURL + "/-/tree/" + name,
		Commit: p.commits[name][0]}
	if i := slices.IndexFunc(p.branches, func(x gitlab.Branch) bool { return x.Name == name }); i >= 0 {
		p.branches[i].Commit = b.Commit
		return p.branches[i]
	}
	p.branches = append(p.branches, b)
	return b
}

// startBranch creates branch at ref: a branch, a tag or a commit. It
// reports false for a ref that names none of them.
func startBranch(p *project, branch, ref string) bool {
	commits, ok := history(p, ref)
	if !ok {
		return false
	}
	// The tree is the branch the commit heads, or failing that the
	// branch that holds it: the instance keeps trees only at heads.
	from := branchOf(p, commits[0].ID)
	if from == "" {
		for b, cs := range p.commits {
			if commitIndex(cs, commits[0].ID) >= 0 {
				from = b
				break
			}
		}
	}
	p.commits[branch] = slices.Clone(commits)
	p.trees[branch] = cloneTree(p.trees[from])
	heads := from != "" && p.commits[from][0].ID == commits[0].ID
	files := map[string]string{}
	for path := range p.trees[branch] {
		files[path] = commits[0].ID
		if heads {
			files[path] = lastCommit(p, from, path)
		}
	}
	p.fileCommits[branch] = files
	setBranch(p, branch)
	return true
}

func (s *Server) createBranch(w http.ResponseWriter, r *http.Request, p *project) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "branch", "ref") {
		return
	}
	name, _ := b.str("branch")
	ref, _ := b.str("ref")
	if _, exists := p.trees[name]; exists {
		message(w, http.StatusBadRequest, "Branch already exists")
		return
	}
	if !startBranch(p, name, ref) {
		message(w, http.StatusBadRequest, "Invalid reference name: "+ref)
		return
	}
	writeJSON(w, http.StatusCreated, setBranch(p, name))
}

// getBranch is GET repository/branches/:branch.
func (s *Server) getBranch(w http.ResponseWriter, p *project, name string) {
	for _, b := range p.branches {
		if b.Name == name {
			writeJSON(w, http.StatusOK, withMerged(p, b))
			return
		}
	}
	message(w, http.StatusNotFound, "404 Branch Not Found")
}

// ------------------------------------------------------------ commits

// commitAction is one entry of a commit's actions, in GitLab's names.
type commitAction struct {
	Action       string  `json:"action"`
	FilePath     string  `json:"file_path"`
	PreviousPath string  `json:"previous_path"`
	Content      *string `json:"content"`
	Encoding     string  `json:"encoding"`
	LastCommitID string  `json:"last_commit_id"`
}

func (s *Server) createCommit(w http.ResponseWriter, r *http.Request, p *project, user string) {
	b, ok := readBody(w, r)
	if !ok || !b.require(w, "branch", "commit_message", "actions") {
		return
	}
	var actions []commitAction
	if err := json.Unmarshal(b["actions"], &actions); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "actions is invalid"})
		return
	}
	if len(actions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "actions is empty"})
		return
	}
	branch, _ := b.str("branch")
	start, _ := b.str("start_branch")
	msg, _ := b.str("commit_message")
	base := branch
	if _, exists := p.trees[branch]; !exists {
		if start == "" {
			message(w, http.StatusBadRequest, "You can only create or edit files when you are on a branch")
			return
		}
		if _, ok := history(p, start); !ok {
			message(w, http.StatusBadRequest, "Invalid reference name: "+start)
			return
		}
		base = ""
	}

	// Validate every action against a working copy before anything moves.
	var tree map[string]string
	var witness func(path string) string
	if base != "" {
		tree = cloneTree(p.trees[base])
		witness = func(path string) string { return lastCommit(p, base, path) }
	} else {
		// A new branch is checked against start_branch's files.
		from := branchOf(p, start)
		tree = cloneTree(p.trees[from])
		witness = func(path string) string { return lastCommit(p, from, path) }
	}
	before := cloneTree(tree)
	for i := range actions {
		if problem, status := applyAction(tree, witness, &actions[i], i); problem != nil {
			writeJSON(w, status, problem)
			return
		}
	}

	if base == "" {
		startBranch(p, branch, start)
	}
	c := s.writeCommit(p, branch, msg, user, actions, before, tree)
	writeJSON(w, http.StatusCreated, c)
}

// writeCommit moves branch to a new commit by user whose tree is tree,
// made by actions from before, and moves the open merge requests from
// that branch, as a push does.
func (s *Server) writeCommit(p *project, branch, msg, user string, actions []commitAction, before, tree map[string]string) gitlab.Commit {
	files := pinFileCommits(p, branch)
	parent := p.commits[branch][0]
	s.nextCommit++
	now := s.opts.Now().UTC()
	u := s.user(user)
	id := fakeSHA(p.PathWithNamespace, branch, msg, itoa(s.nextCommit))
	title, _, _ := strings.Cut(msg, "\n")
	c := gitlab.Commit{ID: id, ShortID: id[:8], Title: title, Message: msg, AuthorName: u.Name, AuthorEmail: user + "@example.com",
		AuthoredDate: now, CommitterName: u.Name, CommitterEmail: user + "@example.com", CommittedDate: now, CreatedAt: now,
		ParentIDs: []string{parent.ID}, WebURL: p.WebURL + "/-/commit/" + id, Stats: &gitlab.CommitStats{}}
	diffs := make([]gitlab.Diff, 0, len(actions))
	for _, a := range actions {
		oldPath := a.FilePath
		if a.Action == "move" {
			oldPath = a.PreviousPath
		}
		var old, cur *string
		if v, ok := before[oldPath]; ok && a.Action != "create" {
			old = &v
		}
		if v, ok := tree[a.FilePath]; ok && a.Action != "delete" {
			cur = &v
		}
		d, adds, dels := fileDiff(oldPath, a.FilePath, old, cur)
		diffs = append(diffs, d)
		c.Stats.Additions += adds
		c.Stats.Deletions += dels
		delete(files, oldPath)
		if a.Action != "delete" {
			files[a.FilePath] = id
		}
	}
	c.Stats.Total = c.Stats.Additions + c.Stats.Deletions
	p.diffs[id] = diffs
	p.trees[branch] = tree
	p.snapshots[id] = tree
	p.commits[branch] = append([]gitlab.Commit{c}, p.commits[branch]...)
	setBranch(p, branch)
	for _, mr := range p.mrs {
		if mr.State == "opened" && mr.SourceBranch == branch {
			s.refreshMR(p, mr)
			s.resetOnPush(p, mr)
			s.autoMRPipeline(p, mr, user)
		}
	}
	return c
}

// applyAction applies one action to a working tree, or returns GitLab's
// refusal and its status.
func applyAction(tree map[string]string, witness func(string) string, a *commitAction, i int) (map[string]any, int) {
	field := func(name string) map[string]any {
		return map[string]any{"error": fmt.Sprintf("actions[%d][%s] is missing", i, name)}
	}
	if !slices.Contains([]string{"create", "update", "delete", "move", "chmod"}, a.Action) {
		if a.Action == "" {
			return field("action"), http.StatusBadRequest
		}
		return map[string]any{"error": fmt.Sprintf("actions[%d][action] does not have a valid value", i)}, http.StatusBadRequest
	}
	if a.FilePath == "" {
		return field("file_path"), http.StatusBadRequest
	}
	content := ""
	if a.Content != nil {
		content = *a.Content
	}
	if a.Encoding == "base64" {
		raw, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return map[string]any{"message": "Invalid base64"}, http.StatusBadRequest
		}
		content = string(raw)
	}
	subject := a.FilePath
	if a.Action == "move" {
		if a.PreviousPath == "" {
			return field("previous_path"), http.StatusBadRequest
		}
		subject = a.PreviousPath
	}
	exists := func(path string) bool { _, ok := tree[path]; return ok }
	switch {
	case a.Action == "create" && exists(a.FilePath):
		return map[string]any{"message": "A file with this name already exists"}, http.StatusBadRequest
	case a.Action != "create" && !exists(subject):
		return map[string]any{"message": "A file with this name doesn't exist"}, http.StatusBadRequest
	case a.Action == "move" && exists(a.FilePath):
		return map[string]any{"message": "A file with this name already exists"}, http.StatusBadRequest
	case a.Action != "create" && a.LastCommitID != "" && a.LastCommitID != witness(subject):
		return map[string]any{"message": "The file has changed since you started editing it: " + subject}, http.StatusBadRequest
	}
	switch a.Action {
	case "create", "update":
		tree[a.FilePath] = content
	case "delete":
		delete(tree, a.FilePath)
	case "move":
		moved := tree[a.PreviousPath]
		if a.Content != nil {
			moved = content
		}
		delete(tree, a.PreviousPath)
		tree[a.FilePath] = moved
	}
	return nil, 0
}
