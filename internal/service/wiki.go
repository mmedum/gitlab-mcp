package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
)

// The wiki toolset (§7.8): a project's wiki pages. GitLab's wiki API
// exposes no version and no updated_at, so the witness a write carries
// is a hash of the content the caller read, compared with a fresh read
// before the write (§4.6). As with issues, the window between that read
// and the write stays open (§17b).

// WikiFormats are the markups a page can be written in.
var WikiFormats = []string{"asciidoc", "markdown", "org", "rdoc"}

// contentHash is the wiki witness: the SHA-256 of a page's content as
// GitLab returns it, before anything is removed for display.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// ListWikiPages lists a project wiki's pages. GitLab answers with every
// page at once.
func (s *Service) ListWikiPages(ctx context.Context, raw string) (model.WikiPages, error) {
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.WikiPages{}, err
	}
	pages, err := s.client.ListWikiPages(ctx, p)
	if err != nil {
		return model.WikiPages{}, err
	}
	out := model.WikiPages{Project: ref, Pages: make([]model.WikiPageRow, 0, len(pages)),
		Listing: model.Listing{Returned: len(pages), Complete: true}}
	for _, pg := range pages {
		title, _ := render.Line(pg.Title, render.NoteBudget)
		out.Pages = append(out.Pages, model.WikiPageRow{Slug: pg.Slug, Title: title, Format: pg.Format})
	}
	return out, nil
}

// GetWikiPage reads a page's content under the file budget, inside a
// boundary, with the hash a write needs.
func (s *Service) GetWikiPage(ctx context.Context, raw, slug string, offset int) (model.WikiPage, error) {
	if strings.TrimSpace(slug) == "" {
		return model.WikiPage{}, gapi.Errf(gapi.ClassInvalid, "slug is empty; list_wiki_pages lists the slugs")
	}
	p, ref, err := s.project(ctx, raw)
	if err != nil {
		return model.WikiPage{}, err
	}
	pg, err := s.client.GetWikiPage(ctx, p, slug)
	if err != nil {
		return model.WikiPage{}, err
	}
	out := model.WikiPage{Project: ref, Slug: pg.Slug, Format: pg.Format, ContentSHA256: contentHash(pg.Content)}
	out.UntrustedTitle, _ = render.Line(pg.Title, render.NoteBudget)
	text, removed := render.Markdown(pg.Content, s.self())
	if out.UntrustedContent, out.Budget, err = cut(text, removed, offset, render.FileBudget, "the page", "offset"); err != nil {
		return model.WikiPage{}, err
	}
	return out, nil
}

// WikiSave is save_wiki_page's request: a new page when Slug is empty,
// otherwise a change to that page. Nil fields are not changed.
type WikiSave struct {
	Project       string
	Slug          string
	Title         *string
	Content       *string
	Format        string
	ContentSHA256 string
}

// SaveWikiPage creates a page, or changes one the caller read.
func (s *Service) SaveWikiPage(ctx context.Context, in WikiSave) (model.WikiWrite, error) {
	if in.Format != "" && !slices.Contains(WikiFormats, in.Format) {
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "format must be one of %s", strings.Join(WikiFormats, ", "))
	}
	if in.Slug == "" {
		return s.createWikiPage(ctx, in)
	}
	return s.updateWikiPage(ctx, in)
}

func (s *Service) createWikiPage(ctx context.Context, in WikiSave) (model.WikiWrite, error) {
	switch {
	case in.Title == nil || strings.TrimSpace(*in.Title) == "":
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "a new page needs a title; to change a page, pass its slug")
	case in.Content == nil:
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "a new page needs content")
	case in.ContentSHA256 != "":
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "content_sha256 is the witness of a page you read: pass it with that page's slug")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.WikiWrite{}, err
	}
	body := gapi.WikiPageCreate{Title: *in.Title, Content: *in.Content, Format: in.Format}
	if gapi.IsDryRun(ctx) {
		return model.WikiWrite{Outcome: "dry_run", Title: *in.Title, Format: in.Format, Changed: []string{}, Write: model.Write{DryRun: true,
			Target: t.ref, WouldSend: preview("POST", "create a wiki page", fieldsOf(body))}}, nil
	}
	pg, err := s.client.CreateWikiPage(ctx, t.p, body)
	if err != nil {
		return model.WikiWrite{}, settle(err, "wiki page", func() (string, error) {
			pages, err := s.client.ListWikiPages(ctx, t.p)
			if err != nil {
				return "", err
			}
			// GitLab lists a page in a directory by its last part as the
			// title, so the slug, the title with hyphens for spaces, is
			// matched too.
			slug := strings.ReplaceAll(strings.TrimSpace(*in.Title), " ", "-")
			if i := slices.IndexFunc(pages, func(p gitlab.WikiPageBasic) bool { return p.Title == *in.Title || p.Slug == slug }); i >= 0 {
				return "the page " + pages[i].Slug, nil
			}
			return "", nil
		})
	}
	return model.WikiWrite{Outcome: "created", Write: model.Write{Target: t.ref}, Slug: pg.Slug, Title: pg.Title, Format: pg.Format,
		ContentSHA256: contentHash(pg.Content), Changed: []string{}}, nil
}

func (s *Service) updateWikiPage(ctx context.Context, in WikiSave) (model.WikiWrite, error) {
	switch {
	case strings.TrimSpace(in.ContentSHA256) == "":
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "content_sha256 is required to change a page: pass the one get_wiki_page "+
			"returned, so a change made since your read is not overwritten")
	case in.Title == nil && in.Content == nil && in.Format == "":
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "nothing to change: pass title, content or format")
	case in.Title != nil && strings.TrimSpace(*in.Title) == "":
		return model.WikiWrite{}, gapi.Errf(gapi.ClassInvalid, "title is empty")
	}
	t, err := s.writeTarget(ctx, in.Project)
	if err != nil {
		return model.WikiWrite{}, err
	}
	before, err := s.client.GetWikiPage(ctx, t.p, in.Slug)
	if err != nil {
		return model.WikiWrite{}, err
	}
	if err := checkHash(in.ContentSHA256, before.Content); err != nil {
		return model.WikiWrite{}, err
	}
	body := gapi.WikiPageUpdate{Title: in.Title, Content: in.Content}
	if in.Format != "" {
		body.Format = &in.Format
	}
	if gapi.IsDryRun(ctx) {
		return model.WikiWrite{Outcome: "dry_run", Slug: before.Slug, Title: before.Title, Format: before.Format,
			ContentSHA256: contentHash(before.Content), Changed: []string{}, Write: model.Write{DryRun: true, Target: t.ref,
				WouldSend: preview("PUT", "change a wiki page", fieldsOf(body))}}, nil
	}
	pg, err := s.client.UpdateWikiPage(ctx, t.p, in.Slug, body)
	if err != nil {
		return model.WikiWrite{}, err
	}
	out := model.WikiWrite{Outcome: "updated", Write: model.Write{Target: t.ref}, Slug: pg.Slug, Title: pg.Title, Format: pg.Format,
		ContentSHA256: contentHash(pg.Content), Changed: names(field{"title", pg.Title != before.Title},
			field{"format", pg.Format != before.Format}, field{"content", pg.Content != before.Content})}
	if in.Content != nil {
		out.ContentRemoved = removedFrom(before.Content, pg.Content)
	}
	if len(out.Changed) == 0 {
		out.Outcome = "unchanged"
	}
	return out, nil
}

// checkHash refuses a write whose witness is not the hash of the page's
// current content.
func checkHash(witness, content string) error {
	if current := contentHash(content); !strings.EqualFold(strings.TrimSpace(witness), current) {
		return gapi.Errf(gapi.ClassStale, "the page changed since it was read: its content_sha256 is now %s. Read it again, check "+
			"the change still makes sense, and pass the new content_sha256", current)
	}
	return nil
}

// DeleteWikiPage deletes a page the caller read.
func (s *Service) DeleteWikiPage(ctx context.Context, raw, slug, witness string) (model.WikiDelete, error) {
	switch {
	case strings.TrimSpace(slug) == "":
		return model.WikiDelete{}, gapi.Errf(gapi.ClassInvalid, "slug is empty; list_wiki_pages lists the slugs")
	case strings.TrimSpace(witness) == "":
		return model.WikiDelete{}, gapi.Errf(gapi.ClassInvalid, "content_sha256 is required: pass the one get_wiki_page returned, so a "+
			"page changed since your read is not deleted unseen")
	}
	t, err := s.writeTarget(ctx, raw)
	if err != nil {
		return model.WikiDelete{}, err
	}
	pg, err := s.client.GetWikiPage(ctx, t.p, slug)
	if err != nil {
		return model.WikiDelete{}, err
	}
	if err := checkHash(witness, pg.Content); err != nil {
		return model.WikiDelete{}, err
	}
	out := model.WikiDelete{Outcome: "deleted", Write: model.Write{Target: t.ref}, Slug: pg.Slug}
	if gapi.IsDryRun(ctx) {
		out.Outcome, out.DryRun, out.WouldSend = "dry_run", true, preview("DELETE", "delete the wiki page", nil)
		return out, nil
	}
	err = s.client.DeleteWikiPage(ctx, t.p, slug)
	_, readErr := s.client.GetWikiPage(ctx, t.p, slug)
	if out.Notes, err = deleted(err, readErr, "page"); err != nil {
		return model.WikiDelete{}, err
	}
	return out, nil
}
