package service

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/gapi"
	"github.com/mmedum/gitlab-mcp/internal/gitlab"
	"github.com/mmedum/gitlab-mcp/internal/instance"
	"github.com/mmedum/gitlab-mcp/internal/model"
	"github.com/mmedum/gitlab-mcp/internal/scopes"
)

// Me reads who is signed in and what this server and instance support
// (§7.9). The account is required; the instance metadata and the token
// details are reported as unknown when they cannot be read, because the
// rest of the answer is still worth having. The reads are independent
// and run at once, and metadata read here is kept for the next call.
func (s *Service) Me(ctx context.Context) (model.Me, error) {
	c, err := s.api()
	if err != nil {
		return model.Me{}, err
	}
	var (
		wg      sync.WaitGroup
		u       *gitlab.User
		meta    = s.metadata()
		metaErr error
		info    *gitlab.TokenInfo
		infoErr error
	)
	wg.Go(func() { u, err = c.GetCurrentUser(ctx) })
	if meta.Version.Major == 0 {
		wg.Go(func() { meta, metaErr = s.readMetadata(ctx, c) })
	}
	wg.Go(func() { info, infoErr = c.TokenInfo(ctx) })
	wg.Wait()
	if err != nil {
		return model.Me{}, err
	}
	if metaErr != nil && !soft(metaErr) {
		return model.Me{}, metaErr
	}
	if infoErr != nil && !soft(infoErr) {
		return model.Me{}, infoErr
	}
	me := model.Me{
		User:     model.MeUser{ID: u.ID, Username: u.Username, Name: u.Name, State: u.State, Bot: u.Bot, Admin: u.IsAdmin},
		Instance: model.InstanceInfo{URL: c.Instance().String()},
		Token:    model.TokenInfo{Kind: "oauth", Scopes: slices.Clone(s.granted)},
	}

	if meta.Version.Major != 0 {
		me.Instance.Known = true
		me.Instance.Version = meta.Version.String()
		me.Instance.Edition = meta.Edition()
	}

	if infoErr == nil {
		me.Token.Scopes = info.Scope
		if info.ExpiresIn != nil {
			at := info.Created().Add(time.Duration(*info.ExpiresIn) * time.Second)
			me.Token.ExpiresAt = &at
		}
	} else {
		me.Notes = append(me.Notes, "the token's details could not be read; the scopes shown are the ones granted at sign-in")
	}

	s.mu.RLock()
	registered := slices.Clone(s.registered)
	s.mu.RUnlock()
	me.Registered = model.Registration{ReadOnly: s.cfg.ReadOnly, Tools: len(registered),
		Toolsets: slices.Clone(s.cfg.Toolsets)}
	for _, k := range scopes.Kinds() {
		if slices.ContainsFunc(registered, func(r Registered) bool { return r.Kind == k }) {
			me.Registered.Kinds = append(me.Registered.Kinds, string(k))
		}
	}
	me.WriteScope = model.WriteScope{Confined: len(s.cfg.WriteNamespaces) > 0, Namespaces: slices.Clone(s.cfg.WriteNamespaces)}

	if r := c.RateReading(); r.Known {
		me.Rate = model.RateReading{Known: true, Limit: r.Limit, Remaining: r.Remaining}
		if !r.Reset.IsZero() {
			reset := r.Reset
			me.Rate.Reset = &reset
		}
		if !r.Observed.IsZero() {
			observed := r.Observed
			me.Rate.Observed = &observed
		}
	}
	return me, nil
}

// metadata is the instance's version and edition as far as they are
// known: from the first get_me that read them.
func (s *Service) metadata() instance.Metadata {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.meta
}

// readMetadata reads the instance's version and edition, and keeps them
// once they are known.
func (s *Service) readMetadata(ctx context.Context, c *gapi.Client) (instance.Metadata, error) {
	raw, err := c.GetMetadata(ctx)
	if err != nil {
		return instance.Metadata{}, err
	}
	meta, _ := instance.NewMetadata(raw.Version, raw.Revision, raw.Enterprise)
	if meta.Version.Major != 0 {
		s.mu.Lock()
		s.meta = meta
		s.mu.Unlock()
	}
	return meta, nil
}

// soft reports a failure of a secondary read that the answer can do
// without. A failure that says the sign-in itself is broken is not
// soft: the caller must hear it.
func soft(err error) bool {
	c, _ := gapi.ClassOf(err)
	return c != gapi.ClassAuth && c != gapi.ClassRateLimited
}
