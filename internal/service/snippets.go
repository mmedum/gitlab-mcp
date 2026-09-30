package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/v2/internal/config"
	"github.com/mmedum/gitlab-mcp/v2/internal/gapi"
	"github.com/mmedum/gitlab-mcp/v2/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/v2/internal/model"
	"github.com/mmedum/gitlab-mcp/v2/internal/render"
)

// The snippets toolset (§7.8). Snippets this server creates are private,
// always: visibility is not an input.

// ListSnippets lists a project's snippets, or the signed-in account's
// own when project is empty.
func (s *Service) ListSnippets(ctx context.Context, raw string, opts gapi.ListOptions) (model.Snippets, error) {
	c, err := s.api()
	if err != nil {
		return model.Snippets{}, err
	}
	var rows []gitlab.Snippet
	var page gapi.Page
	out := model.Snippets{}
	if raw == "" {
		rows, page, err = c.ListSnippets(ctx, time.Time{}, opts)
	} else {
		p, ref, perr := s.project(ctx, raw)
		if perr != nil {
			return model.Snippets{}, perr
		}
		out.Project = &ref
		rows, page, err = c.ListProjectSnippets(ctx, p, opts)
	}
	if err != nil {
		return model.Snippets{}, err
	}
	out.Snippets, out.Listing = make([]model.SnippetRow, 0, len(rows)), listing(len(rows), page)
	for _, sn := range rows {
		out.Snippets = append(out.Snippets, snippetRow(sn))
	}
	return out, nil
}

func snippetRow(sn gitlab.Snippet) model.SnippetRow {
	title, _ := render.Line(sn.Title, render.NoteBudget)
	return model.SnippetRow{ID: sn.ID, UntrustedTitle: title, Visibility: sn.Visibility, Author: sn.Author.Username,
		ProjectID: sn.ProjectID, Files: snippetFiles(sn), CreatedAt: sn.CreatedAt, UpdatedAt: sn.UpdatedAt, WebURL: sn.WebURL}
}

// snippetFiles names a snippet's files; one from before snippets had a
// repository names only file_name.
func snippetFiles(sn gitlab.Snippet) []string {
	out := make([]string, 0, len(sn.Files))
	for _, f := range sn.Files {
		out = append(out, f.Path)
	}
	if len(out) == 0 && sn.FileName != "" {
		out = append(out, sn.FileName)
	}
	return out
}

// GetSnippet reads a snippet and one of its files: the first unless
// file names another. A personal snippet has no project.
func (s *Service) GetSnippet(ctx context.Context, raw string, id int64, file string, offset int) (model.Snippet, error) {
	c, err := s.api()
	if err != nil {
		return model.Snippet{}, err
	}
	var p gapi.Project
	out := model.Snippet{}
	if raw != "" {
		var ref model.ProjectRef
		if p, ref, err = s.project(ctx, raw); err != nil {
			return model.Snippet{}, err
		}
		out.Project = &ref
	}
	var sn *gitlab.Snippet
	var content []byte
	meta := func() (err error) {
		if raw == "" {
			sn, err = c.GetSnippet(ctx, id)
		} else {
			sn, err = c.GetProjectSnippet(ctx, p, id)
		}
		return err
	}
	first := func() (err error) {
		if raw == "" {
			content, err = c.SnippetRaw(ctx, id)
		} else {
			content, err = c.ProjectSnippetRaw(ctx, p, id)
		}
		return err
	}
	if file == "" {
		// The first file needs nothing from the snippet's own read.
		err = parallel(meta, first)
	} else if err = meta(); err == nil {
		files := snippetFiles(*sn)
		switch {
		case !slices.Contains(files, file):
			// The names are the snippet author's, so each is made plain as
			// a path is anywhere else outside a boundary.
			names := make([]string, len(files))
			for i, f := range files {
				names[i] = render.Ident(f)
			}
			return model.Snippet{}, gapi.Errf(gapi.ClassInvalid, "the snippet has no file %q; its files are %s", file, strings.Join(names, ", "))
		case file == files[0]:
			err = first()
		case raw == "":
			// HEAD is the snippet repository's default branch, whatever
			// it is named.
			content, err = c.SnippetFileRaw(ctx, id, "HEAD", file)
		default:
			content, err = c.ProjectSnippetFileRaw(ctx, p, id, "HEAD", file)
		}
	}
	if err != nil {
		return model.Snippet{}, err
	}
	out.ID, out.Visibility, out.Author, out.Files = sn.ID, sn.Visibility, sn.Author.Username, snippetFiles(*sn)
	if out.File = file; file == "" && len(out.Files) > 0 {
		out.File = out.Files[0]
	}
	out.CreatedAt, out.UpdatedAt, out.WebURL = sn.CreatedAt, sn.UpdatedAt, sn.WebURL
	out.UntrustedTitle, _ = render.Line(sn.Title, render.NoteBudget)
	if sn.Description != nil {
		out.UntrustedDescription, _ = render.Markdown(*sn.Description, s.self())
	}
	if out.UntrustedContent, out.Binary, out.Budget, err = fileContent(content, offset); err != nil {
		return model.Snippet{}, err
	}
	return out, nil
}

// SnippetFile is one file of a new snippet.
type SnippetFile struct {
	Path    string
	Content string
}

// SnippetCreate is create_snippet's request.
type SnippetCreate struct {
	Project     string // empty for a personal snippet
	Title       string
	Description string
	Files       []SnippetFile
}

// CreateSnippet creates a private snippet, in a project or the account's
// own. Visibility is not the caller's to choose: a public snippet from a
// model is how private content leaves (§4.7, §7.8).
func (s *Service) CreateSnippet(ctx context.Context, in SnippetCreate) (model.SnippetWrite, error) {
	switch {
	case strings.TrimSpace(in.Title) == "":
		return model.SnippetWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	case len(in.Files) == 0:
		return model.SnippetWrite{}, gapi.Errf(gapi.ClassInvalid, "files is empty: a snippet holds at least one file")
	}
	body := gapi.SnippetCreate{Title: in.Title, Description: in.Description, Visibility: "private"}
	paths := make([]string, 0, len(in.Files))
	for i, f := range in.Files {
		switch {
		case strings.TrimSpace(f.Path) == "":
			return model.SnippetWrite{}, gapi.Errf(gapi.ClassInvalid, "file %d has no path", i+1)
		case f.Content == "":
			return model.SnippetWrite{}, gapi.Errf(gapi.ClassInvalid, "file %d (%s) is empty, and GitLab refuses an empty snippet file", i+1, f.Path)
		case slices.Contains(paths, f.Path):
			return model.SnippetWrite{}, gapi.Errf(gapi.ClassInvalid, "the path %q is given twice", f.Path)
		}
		paths = append(paths, f.Path)
		body.Files = append(body.Files, gapi.SnippetFileCreate{FilePath: f.Path, Content: f.Content})
	}
	t, err := s.snippetTarget(ctx, in.Project, "Create it in a project instead")
	if err != nil {
		return model.SnippetWrite{}, err
	}
	if in.Project == "" {
		t.ref.Visibility = "private"
	}
	if gapi.IsDryRun(ctx) {
		return model.SnippetWrite{Outcome: "dry_run", Visibility: "private", Files: paths, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "create a private snippet", fieldsOf(body))}}, nil
	}
	start := time.Now()
	var sn *gitlab.Snippet
	if in.Project != "" {
		sn, err = s.client.CreateProjectSnippet(ctx, t.p, body)
	} else {
		sn, err = s.client.CreateSnippet(ctx, body)
	}
	if err != nil {
		return model.SnippetWrite{}, settle(err, "snippet", func() (string, error) {
			return s.findSnippet(ctx, t.p, in.Project != "", in.Title, start)
		})
	}
	if sn.Visibility != "private" {
		// The request said private; anything else is a defect worth
		// shouting about, since it is exactly what §7.8 exists to prevent.
		return model.SnippetWrite{}, gapi.Errf(gapi.ClassUnexpected, "GitLab created snippet %d as %s although private was sent; "+
			"check it now", sn.ID, sn.Visibility)
	}
	return model.SnippetWrite{Outcome: "created", Write: model.Write{Target: t.ref}, ID: sn.ID, Visibility: sn.Visibility,
		Files: snippetFiles(*sn), WebURL: sn.WebURL}, nil
}

// findSnippet settles a lost create_snippet: one by this account with the
// title, created since the call began, among the newest.
func (s *Service) findSnippet(ctx context.Context, p gapi.Project, inProject bool, title string, start time.Time) (string, error) {
	me, err := s.me(ctx)
	if err != nil {
		return "", err
	}
	since := start.Add(-settleSkew)
	var rows []gitlab.Snippet
	if inProject {
		rows, _, err = s.client.ListProjectSnippets(ctx, p, gapi.ListOptions{PerPage: gapi.MaxPerPage})
	} else {
		rows, _, err = s.client.ListSnippets(ctx, since, gapi.ListOptions{PerPage: gapi.MaxPerPage})
	}
	if err != nil {
		return "", err
	}
	for _, sn := range rows {
		if sn.Title == title && sn.Author.Username == me.Username && !sn.CreatedAt.Before(since) {
			return fmt.Sprintf("snippet %d", sn.ID), nil
		}
	}
	return "", nil
}

// snippetTarget resolves where a snippet write goes: a project, held to
// the write allow-list, or the account's own snippets, which the
// allow-list refuses, a personal snippet being in no namespace. A
// personal target names no project and no visibility; the caller says
// what the snippet's is.
func (s *Service) snippetTarget(ctx context.Context, raw, instead string) (target, error) {
	if raw != "" {
		return s.writeTarget(ctx, raw)
	}
	if _, err := s.api(); err != nil {
		return target{}, err
	}
	if len(s.cfg.WriteNamespaces) > 0 {
		return target{}, gapi.Errf(gapi.ClassBlocked, "writes are confined to the namespaces %s names, and a personal "+
			"snippet is in none; nothing was sent. %s", config.EnvWriteNamespaces, instead)
	}
	return target{}, nil
}

// ownSnippet reads a snippet this server may change: the signed-in
// account's own. GitLab lets a project's maintainers change anyone's
// snippet there; another person's snippet is theirs. GitLab's personal
// routes also find the account's snippets in projects, so one reached
// without its project is refused: the allow-list is held against the
// project.
func (s *Service) ownSnippet(ctx context.Context, t target, inProject bool, id int64, verb string) (*gitlab.Snippet, error) {
	var sn *gitlab.Snippet
	var me *gitlab.User
	if err := parallel(
		func() (err error) {
			if inProject {
				sn, err = s.client.GetProjectSnippet(ctx, t.p, id)
			} else {
				sn, err = s.client.GetSnippet(ctx, id)
			}
			return err
		},
		func() (err error) { me, err = s.me(ctx); return err },
	); err != nil {
		return nil, err
	}
	switch {
	case !inProject && sn.ProjectID != nil:
		return nil, gapi.Errf(gapi.ClassInvalid, "snippet %d is in the project with id %d, not one of your personal snippets; "+
			"pass that project", id, *sn.ProjectID)
	case sn.Author.Username != me.Username:
		return nil, gapi.Errf(gapi.ClassBlocked, "the snippet is @%s's, and this server %s only your own; nothing was sent",
			sn.Author.Username, verb)
	}
	return sn, nil
}

// SnippetFileChange is one change to a snippet's files.
type SnippetFileChange struct {
	Action       string // create, update, delete or move
	Path         string
	PreviousPath string // the file a move starts from
	Content      string
}

// SnippetFileActions are the actions a file change takes.
var SnippetFileActions = []string{"create", "update", "delete", "move"}

// SnippetEdit is update_snippet's request. Nil fields are not changed.
type SnippetEdit struct {
	Project     string // empty for a personal snippet
	ID          int64
	Title       *string
	Description *string
	// Content replaces the file of a one-file snippet.
	Content *string
	// Files changes a snippet's files, one action each.
	Files     []SnippetFileChange
	UpdatedAt string
}

// UpdateSnippet changes one of the signed-in account's own snippets. Its
// visibility is left as it is.
func (s *Service) UpdateSnippet(ctx context.Context, in SnippetEdit) (model.SnippetUpdate, error) {
	witness, err := checkSnippetEdit(in)
	if err != nil {
		return model.SnippetUpdate{}, err
	}
	t, err := s.snippetTarget(ctx, in.Project, "Change a project's snippet instead")
	if err != nil {
		return model.SnippetUpdate{}, err
	}
	before, err := s.ownSnippet(ctx, t, in.Project != "", in.ID, "changes")
	if err != nil {
		return model.SnippetUpdate{}, err
	}
	// Writing into a public or internal snippet is how private content
	// leaves, which create_snippet's private-only rule closes (§4.7).
	if before.Visibility != "private" {
		return model.SnippetUpdate{}, gapi.Errf(gapi.ClassBlocked, "the snippet is %s, and this server writes only to private "+
			"snippets, as create_snippet makes them; nothing was sent", before.Visibility)
	}
	// GitLab's PUT checks no witness (§18 row 103), so the window between
	// this read and the write stays open; the check narrows it.
	if err := checkWitness(witness, before.UpdatedAt, "snippet"); err != nil {
		return model.SnippetUpdate{}, err
	}
	files := snippetFiles(*before)
	if in.Content != nil && len(files) > 1 {
		return model.SnippetUpdate{}, gapi.Errf(gapi.ClassInvalid, "the snippet has %d files, and GitLab changes a snippet of "+
			"several files only through files: pass an update action naming the file", len(files))
	}
	actions, planned, err := snippetActions(files, in.Files)
	if err != nil {
		return model.SnippetUpdate{}, err
	}
	if in.Project == "" {
		t.ref.Visibility = before.Visibility
	}
	body := gapi.SnippetUpdate{Title: in.Title, Description: in.Description, Content: in.Content, Files: actions}
	out := model.SnippetUpdate{Outcome: "updated", Write: model.Write{Target: t.ref}, ID: before.ID, Visibility: before.Visibility,
		Files: planned, Changed: []string{}, WebURL: before.WebURL}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("PUT", "change the snippet", fieldsOf(body))
		return out, nil
	}
	var after *gitlab.Snippet
	if in.Project != "" {
		after, err = s.client.UpdateProjectSnippet(ctx, t.p, in.ID, body)
	} else {
		after, err = s.client.UpdateSnippet(ctx, in.ID, body)
	}
	if err != nil {
		return model.SnippetUpdate{}, s.settleSnippetUpdate(ctx, err, t, in, before, planned)
	}
	if after.ID != in.ID || after.UpdatedAt.IsZero() {
		return model.SnippetUpdate{}, gapi.Errf(gapi.ClassUnexpected, "GitLab answered the change without the snippet; "+
			"get_snippet shows what it holds")
	}
	out.Visibility, out.Files, out.UpdatedAt, out.WebURL = after.Visibility, snippetFiles(*after), &after.UpdatedAt, after.WebURL
	// GitLab's answer names the files but shows no content, so content
	// and file actions it accepted count as changed.
	committed := in.Content != nil || len(in.Files) > 0
	out.Changed = names(field{"title", after.Title != before.Title},
		field{"description", snippetDescription(after) != snippetDescription(before)},
		field{"files", len(in.Files) > 0 || !slices.Equal(files, out.Files)}, field{"content", in.Content != nil})
	if in.Description != nil {
		out.DescriptionRemoved = removedFrom(snippetDescription(before), snippetDescription(after))
	}
	switch {
	case len(out.Changed) == 0 && after.UpdatedAt.Equal(before.UpdatedAt):
		out.Outcome, out.Notes = "unchanged", []string{"GitLab kept the snippet as it was: it already read so."}
	case committed:
		// A change to the files is a commit to the snippet's repository,
		// and GitLab's post-receive job touches the snippet once it runs,
		// after the answer (§18 row 103). The answer's updated_at is about
		// to go stale, so it is not handed on as a witness.
		out.UpdatedAt = nil
		out.Notes = append(out.Notes, "GitLab moves updated_at again once it has processed the change to the files, "+
			"shortly after it answers, so no updated_at is given here: read the snippet with get_snippet for the updated_at "+
			"of the next change or delete.")
	}
	if in.Title != nil && after.Title != *in.Title || !slices.Equal(out.Files, planned) {
		out.Notes = append(out.Notes, "GitLab stored the snippet otherwise than was sent; get_snippet shows what it holds.")
	}
	return out, nil
}

// settleSnippetUpdate reads a snippet whose change GitLab did not
// confirm. Such a change created, deleted or moved a file and is not
// repeated, so the read says what it can and the call is not sent again.
func (s *Service) settleSnippetUpdate(ctx context.Context, err error, t target, in SnippetEdit, before *gitlab.Snippet,
	planned []string) error {
	if !gapi.IsClass(err, gapi.ClassAmbiguousOutcome) {
		return err
	}
	var now *gitlab.Snippet
	var readErr error
	if in.Project != "" {
		now, readErr = s.client.GetProjectSnippet(ctx, t.p, in.ID)
	} else {
		now, readErr = s.client.GetSnippet(ctx, in.ID)
	}
	if readErr != nil {
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the snippet change, and reading it to find out "+
			"failed too, so it is unknown: %s", settledUnknown)
	}
	files := snippetFiles(*now)
	shown := make([]string, len(files))
	for i, f := range files {
		shown[i] = render.Ident(f)
	}
	shows := fmt.Sprintf("its files are %s and its updated_at %s", strings.Join(shown, ", "), now.UpdatedAt.UTC().Format(time.RFC3339Nano))
	switch {
	case slices.Equal(files, planned) && !slices.Equal(files, snippetFiles(*before)):
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the snippet change, but a read shows it most "+
			"likely landed: %s. %s", shows, settledLanded)
	case slices.Equal(files, snippetFiles(*before)) && now.UpdatedAt.Equal(before.UpdatedAt):
		// Not settledNotLanded: this PUT was never sent twice, and it may
		// still land, so calling again is not yet known to be safe.
		return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the snippet change, and a read shows the "+
			"snippet as it was: %s. It most likely did not land; read it again before calling again, in case the request is "+
			"still on its way", shows)
	}
	return gapi.Wrap(gapi.ClassAmbiguousOutcome, err, "GitLab did not confirm the snippet change, and a read shows neither "+
		"the snippet as it was nor as asked: %s. Whether this landed is unknown: %s", shows, settledUnknown)
}

// checkSnippetEdit refuses an update_snippet call that says nothing to
// change or that GitLab would refuse whatever the snippet holds, and
// reads its witness.
func checkSnippetEdit(in SnippetEdit) (time.Time, error) {
	switch {
	case in.Title == nil && in.Description == nil && in.Content == nil && len(in.Files) == 0:
		return time.Time{}, gapi.Errf(gapi.ClassInvalid, "nothing to change: pass title, description, content or files")
	case in.Title != nil && strings.TrimSpace(*in.Title) == "":
		return time.Time{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	case in.Content != nil && len(in.Files) > 0:
		return time.Time{}, gapi.Errf(gapi.ClassInvalid, "pass content or files, not both: content replaces the file "+
			"of a one-file snippet, and files changes a snippet's files one action at a time")
	case in.Content != nil && strings.TrimSpace(*in.Content) == "":
		return time.Time{}, gapi.Errf(gapi.ClassInvalid, "content is empty, and GitLab refuses an empty snippet file")
	}
	return parseWitness(in.UpdatedAt)
}

// snippetDescription is a snippet's description, "" when it has none.
func snippetDescription(sn *gitlab.Snippet) string {
	if sn.Description == nil {
		return ""
	}
	return *sn.Description
}

// snippetActions checks each file change against the snippet's files
// as GitLab's SnippetInputAction does, and against what the changes
// before it leave, so a change GitLab would refuse or apply to another
// file is refused before anything is sent. It returns the actions and
// the files they leave.
func snippetActions(files []string, changes []SnippetFileChange) ([]gapi.SnippetFileAction, []string, error) {
	have := slices.Clone(files)
	missing := func(n int, path string) error {
		names := make([]string, len(have))
		for i, f := range have {
			names[i] = render.Ident(f)
		}
		return gapi.Errf(gapi.ClassInvalid, "file change %d: the snippet has no file %q; its files are %s", n, path,
			strings.Join(names, ", "))
	}
	out := make([]gapi.SnippetFileAction, 0, len(changes))
	for i, c := range changes {
		n := i + 1
		switch {
		case !slices.Contains(SnippetFileActions, c.Action):
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d: action must be one of %s", n, strings.Join(SnippetFileActions, ", "))
		case strings.TrimSpace(c.Path) == "":
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d (%s) has no path", n, c.Action)
		case c.PreviousPath != "" && c.Action != "move":
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d: previous_path is the file a move starts from; "+
				"%s takes path alone", n, c.Action)
		case strings.TrimSpace(c.Content) == "" && (c.Action == "create" || c.Action == "update"):
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d (%s %s) has no content, and GitLab refuses an empty "+
				"snippet file", n, c.Action, c.Path)
		case c.Content != "" && c.Action == "delete":
			return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d: delete takes no content", n)
		}
		switch c.Action {
		case "create":
			if slices.Contains(have, c.Path) {
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d creates %q, which the snippet already has; update it instead",
					n, c.Path)
			}
			have = append(have, c.Path)
		case "update":
			if !slices.Contains(have, c.Path) {
				return nil, nil, missing(n, c.Path)
			}
		case "delete":
			if !slices.Contains(have, c.Path) {
				return nil, nil, missing(n, c.Path)
			}
			have = slices.DeleteFunc(have, func(f string) bool { return f == c.Path })
		case "move":
			switch {
			case c.PreviousPath == "":
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d: a move needs previous_path, the file it renames", n)
			case !slices.Contains(have, c.PreviousPath):
				return nil, nil, missing(n, c.PreviousPath)
			case c.Path == c.PreviousPath:
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d moves %q onto itself", n, c.Path)
			case slices.Contains(have, c.Path):
				return nil, nil, gapi.Errf(gapi.ClassInvalid, "file change %d moves onto %q, which the snippet already has", n, c.Path)
			}
			have[slices.Index(have, c.PreviousPath)] = c.Path
		}
		out = append(out, gapi.SnippetFileAction{Action: c.Action, FilePath: c.Path, PreviousPath: c.PreviousPath, Content: c.Content})
	}
	if len(have) == 0 {
		return nil, nil, gapi.Errf(gapi.ClassInvalid, "the changes delete every file, and a snippet holds at least one; "+
			"delete_snippet deletes the snippet")
	}
	return out, have, nil
}

// DeleteSnippet deletes one of the signed-in account's own snippets.
func (s *Service) DeleteSnippet(ctx context.Context, raw string, id int64, updatedAt string) (model.SnippetDelete, error) {
	witness, err := parseWitness(updatedAt)
	if err != nil {
		return model.SnippetDelete{}, err
	}
	t, err := s.snippetTarget(ctx, raw, "Delete a project's snippet instead")
	if err != nil {
		return model.SnippetDelete{}, err
	}
	sn, err := s.ownSnippet(ctx, t, raw != "", id, "deletes")
	if err != nil {
		return model.SnippetDelete{}, err
	}
	if err := checkWitness(witness, sn.UpdatedAt, "snippet"); err != nil {
		return model.SnippetDelete{}, err
	}
	if raw == "" {
		t.ref.Visibility = sn.Visibility
	}
	out := model.SnippetDelete{Outcome: "deleted", Write: model.Write{Target: t.ref}, ID: id}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the snippet", nil)
		return out, nil
	}
	if err := ask(ctx, render.AskDeleteSnippet(t.ref.Project.Path, id, sn.Title, snippetFiles(*sn))); err != nil {
		return model.SnippetDelete{}, err
	}
	// GitLab refuses the delete with 412 when the snippet changed after
	// the time read, as it does a comment's.
	var readErr error
	if raw == "" {
		err = s.client.DeleteSnippet(ctx, id, witness)
		_, readErr = s.client.GetSnippet(ctx, id)
	} else {
		err = s.client.DeleteProjectSnippet(ctx, t.p, id, witness)
		_, readErr = s.client.GetProjectSnippet(ctx, t.p, id)
	}
	if out.Notes, err = deleted(err, readErr, "snippet"); err != nil {
		return model.SnippetDelete{}, err
	}
	return out, nil
}
