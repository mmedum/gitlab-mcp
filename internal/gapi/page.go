package gapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Pagination (§2.12, §7.1). GitLab pages by offset or by keyset; both
// advertise the next page in a Link header, offset also in X-Next-Page.
// The client turns the next page into an opaque token bound to the query
// it came from, so a token is never replayed against another query and
// never names a URL the client would call.

// DefaultPerPage and MaxPerPage are GitLab's own.
const (
	DefaultPerPage = 20
	MaxPerPage     = 100
)

// ListOptions select one page of a listing.
type ListOptions struct {
	// PerPage is the page size, 1 to 100; zero takes 20.
	PerPage int
	// PageToken is the NextToken of the previous page, or empty for the
	// first.
	PageToken string
}

// Page describes one page of a listing.
type Page struct {
	// NextToken continues the listing; empty means it is complete.
	NextToken string
	// Total is X-Total, or -1 when GitLab did not say. It stops saying
	// once a count passes 10,000, and never says on keyset endpoints.
	Total int
}

// Complete reports whether there is no further page.
func (p Page) Complete() bool { return p.NextToken == "" }

// TotalKnown reports whether Total is GitLab's count.
func (p Page) TotalKnown() bool { return p.Total >= 0 }

// paginationKeys are the query parameters that move a listing forward.
// They are left out of a token's binding, so the binding names the
// query and not the position in it.
var paginationKeys = []string{"page", "per_page", "cursor", "id_after", "id_before", "page_token"}

// pageToken is what a NextToken encodes.
type pageToken struct {
	// B binds the token to the query it was issued for.
	B string `json:"b"`
	// P are the parameters that select the next page.
	P map[string]string `json:"p"`
}

// binding hashes a call's method, path and query, less the pagination
// parameters, which are all a token may set.
func binding(call Call, path string) string {
	q := url.Values{}
	for k, v := range call.Query {
		if slices.Contains(paginationKeys, k) {
			continue
		}
		q[k] = v
	}
	sum := sha256.Sum256([]byte(call.Method + " " + strconv.Itoa(int(call.Root)) + " " + path + "?" + q.Encode()))
	return hex.EncodeToString(sum[:12])
}

// list sends a GET for one page and returns where the next one is.
func (c *Client) list(ctx context.Context, call Call, opts ListOptions, out any) (Page, error) {
	if call.Method != http.MethodGet {
		return Page{}, Errf(ClassUnexpected, "a listing must be a GET")
	}
	path, err := fillPath(call.Path, call.Args)
	if err != nil {
		return Page{}, err
	}
	q := url.Values{}
	maps.Copy(q, call.Query)
	call.Query = q

	if opts.PageToken != "" {
		tok, err := decodePageToken(opts.PageToken)
		if err != nil {
			return Page{}, err
		}
		if tok.B != binding(call, path) {
			return Page{}, Errf(ClassInvalid,
				"page_token was issued for a different query: repeat the call that returned it with the same arguments, or start again without it")
		}
		for k, v := range tok.P {
			q.Set(k, v)
		}
	}
	perPage := opts.PerPage
	switch {
	case perPage <= 0:
		perPage = DefaultPerPage
	case perPage > MaxPerPage:
		perPage = MaxPerPage
	}
	q.Set("per_page", strconv.Itoa(perPage))

	res, err := c.do(ctx, call, out)
	if err != nil {
		return Page{}, err
	}
	page := Page{Total: -1}
	if n, err := strconv.Atoi(strings.TrimSpace(res.header.Get("X-Total"))); err == nil && n >= 0 {
		page.Total = n
	}
	next, err := c.nextParams(res, q)
	if err != nil {
		return Page{}, err
	}
	if len(next) > 0 {
		tok := pageToken{B: binding(call, path), P: next}
		raw, _ := json.Marshal(tok)
		page.NextToken = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

// nextParams reads the next page from Link rel="next", or from
// X-Next-Page when there is no Link. A Link is followed only when it is
// on this instance under the API root and names the same endpoint; the
// parameters it changes are what the token carries.
func (c *Client) nextParams(res *response, sent url.Values) (map[string]string, error) {
	if raw := linkNext(res.header.Values("Link")); raw != "" {
		u, err := res.request.Parse(raw)
		// Compared decoded: GitLab may spell a %2F-encoded project path
		// differently in the link than the request did.
		if err != nil || !c.inst.UnderAPIRoot(u) || u.Path != res.request.Path {
			return nil, Errf(ClassUnexpected,
				"GitLab offered a next page outside this endpoint, which is never followed; the listing stops here incomplete")
		}
		next := map[string]string{}
		for k, vs := range u.Query() {
			// Only a pagination parameter may move: a Link that changed a
			// filter would page through a different query.
			if k == "per_page" || len(vs) != 1 || !slices.Contains(paginationKeys, k) {
				continue
			}
			if sent.Get(k) != vs[0] || !sent.Has(k) {
				next[k] = vs[0]
			}
		}
		if len(next) == 0 {
			return nil, Errf(ClassUnexpected, "GitLab's next page is the page just read; the listing stops here incomplete")
		}
		return next, nil
	}
	if p := strings.TrimSpace(res.header.Get("X-Next-Page")); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return map[string]string{"page": p}, nil
		}
	}
	return nil, nil
}

// linkNext finds rel="next" in RFC 8288 Link headers.
func linkNext(values []string) string {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
			if !ok {
				continue
			}
			target = strings.TrimSpace(target)
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			for p := range strings.SplitSeq(params, ";") {
				k, val, _ := strings.Cut(strings.TrimSpace(p), "=")
				if strings.EqualFold(k, "rel") && slices.Contains(strings.Fields(strings.ToLower(strings.Trim(val, `"`))), "next") {
					return target[1 : len(target)-1]
				}
			}
		}
	}
	return ""
}

func decodePageToken(s string) (pageToken, error) {
	var tok pageToken
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(raw, &tok)
	}
	if err != nil || tok.B == "" || len(tok.P) == 0 {
		return pageToken{}, Errf(ClassInvalid, "page_token is not one this server issued: pass it exactly as returned, or start again without it")
	}
	for k := range tok.P {
		if k == "per_page" || !slices.Contains(paginationKeys, k) {
			return pageToken{}, Errf(ClassInvalid, "page_token is not one this server issued")
		}
	}
	return tok, nil
}
