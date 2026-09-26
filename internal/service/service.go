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
	"github.com/mmedum/gitlab-mcp/internal/scopes"
)

// Options are what the service needs.
type Options struct {
	// Client is nil when nobody is signed in; every call then answers
	// [auth].
	Client   *gapi.Client
	Config   config.Config
	Metadata instance.Metadata
	Granted  []string
}

// Registered is one tool the server registered, for get_me and
// resolve_url to report the surface that exists rather than a second
// description of the rules that built it.
type Registered struct {
	Name    string
	Kind    scopes.Kind
	Toolset string // "" for the default set
}

// Service holds what every call shares.
type Service struct {
	client  *gapi.Client
	inst    instance.Instance
	cfg     config.Config
	meta    instance.Metadata
	granted []string

	mu         sync.RWMutex
	registered []Registered
}

// New builds a Service. The instance comes from the client, or from the
// configuration when there is no client, so resolve_url can still say
// what a URL is before anyone signs in.
func New(o Options) *Service {
	s := &Service{client: o.Client, cfg: o.Config, meta: o.Metadata, granted: o.Granted}
	if o.Client != nil {
		s.inst = o.Client.Instance()
	} else if o.Config.Instance != "" {
		s.inst, _ = instance.Parse(o.Config.Instance, o.Config.AllowHTTP)
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

// project resolves the caller's project to its id and path with one
// read, and reports a move GitLab answered with (§6.1, §2.15).
func (s *Service) project(ctx context.Context, raw string) (gapi.Project, model.ProjectRef, error) {
	c, err := s.api()
	if err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	p, err := s.parseProject(raw)
	if err != nil {
		return gapi.Project{}, model.ProjectRef{}, err
	}
	if p.ID() == 0 {
		p, err = c.ResolveProject(ctx, p)
		if err != nil {
			return gapi.Project{}, model.ProjectRef{}, err
		}
		return p, projectRef(ctx, p.ID(), p.Path()), nil
	}
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
