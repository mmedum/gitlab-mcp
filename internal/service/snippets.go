package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
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
			return model.Snippet{}, gapi.Errf(gapi.ClassInvalid, "the snippet has no file %q; its files are %s", file, strings.Join(files, ", "))
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
	var t target
	if in.Project != "" {
		var err error
		if t, err = s.writeTarget(ctx, in.Project); err != nil {
			return model.SnippetWrite{}, err
		}
	} else {
		if _, err := s.api(); err != nil {
			return model.SnippetWrite{}, err
		}
		if len(s.cfg.WriteNamespaces) > 0 {
			return model.SnippetWrite{}, gapi.Errf(gapi.ClassBlocked, "writes are confined to the namespaces %s names, and a personal "+
				"snippet is in none; nothing was sent. Create it in a project instead", config.EnvWriteNamespaces)
		}
		t.ref = model.WriteTarget{Visibility: "private"}
	}
	if gapi.IsDryRun(ctx) {
		return model.SnippetWrite{Outcome: "dry_run", Visibility: "private", Files: paths, Write: model.Write{DryRun: true, Target: t.ref,
			WouldSend: preview("POST", "create a private snippet", fieldsOf(body))}}, nil
	}
	start := time.Now()
	var sn *gitlab.Snippet
	var err error
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
