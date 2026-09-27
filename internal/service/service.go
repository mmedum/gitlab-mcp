// Package service is the orchestration between the tools and the REST
// client: resolving a project once per call, the reads that take more
// than one request, and turning wire types into the model a result
// carries, with every piece of other people's text prepared by render
// on the way (docs/architecture.md §4.1, §4.8).
package service

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/mmedum/gitlab-mcp/internal/config"
	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/render"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
)

// Options are what the service needs.
type Options struct {
	// Client is nil when nobody is signed in; every call then answers
	// [auth].
	Client  *gapi.Client
	Config  config.Config
	Granted []string
}

// Registered is one tool the server registered, for get_me and
// resolve_url to report the surface that exists rather than a second
// description of the rules that built it.
type Registered struct {
	Name string
	Kind scopes.Kind
}

// Service holds what every call shares.
type Service struct {
	client  *gapi.Client
	inst    instance.Instance
	cfg     config.Config
	granted []string

	mu         sync.RWMutex
	registered []Registered
	// meta is the instance's version and edition, zero until get_me
	// reads them.
	meta instance.Metadata
}

// New builds a Service. The instance comes from the client, or from the
// configuration when there is no client, so resolve_url can still say
// what a URL is before anyone signs in.
func New(o Options) *Service {
	s := &Service{client: o.Client, cfg: o.Config, granted: o.Granted, inst: o.Config.Target()}
	if o.Client != nil {
		s.inst = o.Client.Instance()
	}
	return s
}

// SetRegistered records the tools the server registered.
func (s *Service) SetRegistered(r []Registered) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registered = slices.Clone(r)
}

func (s *Service) isRegistered(name string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.ContainsFunc(s.registered, func(r Registered) bool { return r.Name == name })
}

// errSignedOut is every call's answer without a client.
func errSignedOut() error {
	return gapi.Errf(gapi.ClassAuth, "not signed in: run `gitlab-mcp login`, then restart the server")
}

func (s *Service) api() (*gapi.Client, error) {
	if s.client == nil {
		return nil, errSignedOut()
	}
	return s.client, nil
}

// self is the instance's host, which links on it keep their path.
func (s *Service) self() string {
	if s.inst.IsZero() {
		return ""
	}
	return s.inst.Host()
}

// parseProject reads a numeric id, a full path, or a web URL on the
// configured instance (§6.1).
func (s *Service) parseProject(raw string) (gapi.Project, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") || (!s.inst.IsZero() && strings.HasPrefix(strings.ToLower(raw), strings.ToLower(s.inst.Host())+"/")) {
		ref, err := s.inst.ResolveURL(raw)
		if err != nil {
			return gapi.Project{}, gapi.AsError(err)
		}
		raw = ref.Project
	}
	return gapi.ParseProject(raw)
}

// project resolves the caller's project to its id and path, with at
// most one read and none when the process already knows it, and reports
// a move GitLab answered with (§6.1, §2.15).
func (s *Service) project(ctx context.Context, raw string) (gapi.Project, model.ProjectRef, error) {
	c, err := s.api()
	if err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	p, err := s.parseProject(raw)
	if err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	if p, err = c.ResolveProject(ctx, p); err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	if p.Path() != "" {
		return p, projectRef(ctx, p.ID(), p.Path()), nil
	}
	// An id the process has not met yet: one read learns its path.
	proj, err := c.GetProject(ctx, p)
	if err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	return p, projectRef(ctx, proj.ID, proj.PathWithNamespace), nil
}

// projectRef names a project, with the old path when this call met a
// move.
func projectRef(ctx context.Context, id int64, path string) model.ProjectRef {
	ref := model.ProjectRef{ID: id, Path: path}
	for _, m := range gapi.Moves(ctx) {
		if !strings.EqualFold(m.From, path) {
			from := m.From
			ref.MovedFrom = &from
			break
		}
	}
	return ref
}

// cut shows the part of a prepared text from offset that fits budget,
// with the hidden characters removed while preparing it, and refuses an
// offset past the end. what names the text in that refusal, param the
// input that carried the offset.
func cut(text string, removed, offset, budget int, what, param string) (string, model.Budget, error) {
	shown, b := render.Cut(text, offset, budget)
	if offset > b.TotalChars {
		return "", model.Budget{}, gapi.Errf(gapi.ClassInvalid, "%s %d is past the end of %s, which has %d characters",
			param, offset, what, b.TotalChars)
	}
	b.HiddenRemoved = removed
	return shown, b, nil
}

// readPages reads a listing a page of a hundred at a time, up to
// maxPages; complete is false when there were more.
func readPages[T any](maxPages int, read func(gapi.ListOptions) ([]T, gapi.Page, error)) ([]T, bool, error) {
	return readPagesUntil(maxPages, read, nil)
}

// readPagesUntil is readPages that also stops once done says the rows
// read so far are enough; complete then says whether the listing ended.
func readPagesUntil[T any](maxPages int, read func(gapi.ListOptions) ([]T, gapi.Page, error), done func([]T) bool) ([]T, bool, error) {
	var all []T
	opts := gapi.ListOptions{PerPage: gapi.MaxPerPage}
	for range maxPages {
		rows, page, err := read(opts)
		if err != nil {
			return nil, false, err
		}
		all = append(all, rows...)
		if page.Complete() {
			return all, true, nil
		}
		if done != nil && done(all) {
			return all, false, nil
		}
		opts.PageToken = page.NextToken
	}
	return all, false, nil
}

// listing turns a client page into the model's.
func listing(returned int, p gapi.Page) model.Listing {
	l := model.Listing{Returned: returned, Complete: p.Complete()}
	if p.TotalKnown() {
		total := p.Total
		l.Total = &total
	}
	if !p.Complete() {
		tok := p.NextToken
		l.NextPageToken = &tok
	}
	return l
}

func users(in []gitlabUser) []model.User {
	out := make([]model.User, 0, len(in))
	for _, u := range in {
		out = append(out, model.User{Username: u.Username, Name: u.Name})
	}
	return out
}
