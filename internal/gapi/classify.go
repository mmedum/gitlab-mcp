package gapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mmedum/gitlab-mcp/internal/redact"
)

// envelope is what GitLab's error body said, from any of its four
// shapes (§2.14):
//
//	{"message": "404 Project Not Found"}
//	{"message": {"title": ["can't be blank"]}}
//	{"message": {"error": "This endpoint has been requested too many times."}}
//	{"error": "404 Not Found"}  // also parameter validation, and OAuth errors
type envelope struct {
	// message is the "message" key flattened to text.
	message string
	// messageObjectError is set when message came from {"message":{"error":…}}.
	messageObjectError bool
	// topError is the top-level "error" key, with its description.
	topError, description string
	// scope is the scope an insufficient_scope refusal names.
	scope string
	// json is false for a body that did not parse as a JSON object.
	json bool
}

// maxDetail bounds how much of GitLab's own text a message quotes.
const maxDetail = 300

func parseEnvelope(body []byte) envelope {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return envelope{}
	}
	env := envelope{json: true}
	if m, ok := raw["message"]; ok {
		env.message, env.messageObjectError = flattenMessage(m)
	}
	_ = json.Unmarshal(raw["error"], &env.topError)
	_ = json.Unmarshal(raw["error_description"], &env.description)
	_ = json.Unmarshal(raw["scope"], &env.scope)
	return env
}

// flattenMessage renders the message key: a string, a list of strings,
// {"error": "…"}, or field errors as "field: problem, problem".
func flattenMessage(m json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(m, &s) == nil {
		return s, false
	}
	var list []string
	if json.Unmarshal(m, &list) == nil {
		return strings.Join(list, "; "), false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(m, &obj) != nil {
		return "", false
	}
	if e, ok := obj["error"]; ok {
		var text string
		if json.Unmarshal(e, &text) == nil {
			return text, true
		}
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var problems []string
		if json.Unmarshal(obj[k], &problems) != nil {
			var one string
			if json.Unmarshal(obj[k], &one) != nil {
				continue
			}
			problems = []string{one}
		}
		parts = append(parts, k+": "+strings.Join(problems, ", "))
	}
	return strings.Join(parts, "; "), false
}

// detail is what GitLab said, for quoting, or the status text.
func (e envelope) detail(status int) string {
	d := e.message
	if d == "" {
		d = e.topError
		if e.description != "" {
			d += ": " + e.description
		}
	}
	if d == "" {
		d = http.StatusText(status)
	}
	return redact.Truncate(stripURL(strings.Join(strings.Fields(d), " ")), maxDetail)
}

// staleFile is the Files and Commits API refusing a last_commit_id that
// is no longer the file's last commit. It answers 400, not 409 (§2.10).
var staleFile = regexp.MustCompile(`(?i)has changed since you started editing it`)

// alreadyExists is a create refused because its name is taken: a branch,
// or a file a commit would create. GitLab answers 400, not 409.
var alreadyExists = regexp.MustCompile(`(?i)already exists`)

// notFoundWhat pulls the resource out of "404 Project Not Found".
var notFoundWhat = regexp.MustCompile(`(?i)^404\s+(.*?)\s*not\s+found$`)

// scopeInHeader reads the scope from a WWW-Authenticate challenge.
var scopeInHeader = regexp.MustCompile(`scope="([^"]*)"`)

// answer is a failed response being classified.
type answer struct {
	call       Call
	name       string
	repeatable bool
	status     int
	h          http.Header
	env        envelope
	detail     string
}

func (a answer) fail(c Class, format string, args ...any) verdict {
	return verdict{err: &Error{Class: c, Status: a.status, Message: fmt.Sprintf(format, args...)}}
}

// classifyStatus classifies an answer that was not a success.
func classifyStatus(call Call, name string, repeatable bool, status int, h http.Header, body []byte) verdict {
	a := answer{call: call, name: name, repeatable: repeatable, status: status, h: h, env: parseEnvelope(body)}
	a.detail = a.env.detail(status)
	if !a.env.json && status != http.StatusTooManyRequests {
		a.detail = describeBody(status, h, body)
	}
	switch {
	case status == http.StatusTooManyRequests:
		return a.rateLimited()
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return a.refused()
	case status == http.StatusNotFound:
		return a.notFound()
	case status >= 500:
		return a.serverError()
	case status >= 400:
		return a.clientError()
	default:
		return a.fail(ClassUnexpected, "GitLab answered %s with status %d: %s", name, status, a.detail)
	}
}

// rateLimited: GitLab did not act, so any method may try again, after
// at least the wait it asked for. A plain-text body is the instance
// throttle; JSON is one endpoint's application limit (§2.13).
func (a answer) rateLimited() verdict {
	after := parseRetryAfter(a.h.Get("Retry-After"), time.Now())
	which := "the instance's request throttle"
	if a.env.json {
		which = "an application limit on this endpoint"
	}
	wait := "wait a minute and retry"
	if after > 0 {
		wait = fmt.Sprintf("retry after %s", roundUp(after))
	}
	v := a.fail(ClassRateLimited, "GitLab is rate limiting this account (%s) on %s; %s", which, a.name, wait)
	v.after = after
	v.retry = after <= maxRetryAfter
	v.throttle = !a.env.json
	return v
}

// refused tells a bad token and a missing scope, both [auth], from a
// role that refuses, [forbidden] (§3.2).
//
// A 401 is the token only when GitLab marks it invalid_token: expired,
// revoked or unknown, which a new token may clear, so the verdict says
// to try one. GitLab also answers 401 for a merge the account may not
// make (§2.15), where the token is fine and a new one changes nothing.
func (a answer) refused() verdict {
	if a.status == http.StatusUnauthorized && invalidToken(a.env, a.h) {
		v := a.fail(ClassAuth, "GitLab refused the access token for %s: it may have expired or been revoked. Run `gitlab-mcp login`. GitLab said: %s", a.name, a.detail)
		v.reauth = true
		return v
	}
	if a.status == http.StatusUnauthorized {
		return a.fail(ClassAuth, "GitLab refused %s for this account without calling the token invalid, as it does for an action the account's role does not allow, such as a merge. If every call is refused this way, run `gitlab-mcp login`. GitLab said: %s", a.name, a.detail)
	}
	if !insufficientScope(a.env, a.h) {
		return a.fail(ClassForbidden, "GitLab refused %s for this account: %s", a.name, a.detail)
	}
	scope := a.env.scope
	if scope == "" {
		if m := scopeInHeader.FindStringSubmatch(a.h.Get("WWW-Authenticate")); m != nil {
			scope = m[1]
		}
	}
	if scope == "" {
		return a.fail(ClassAuth, "the signed-in token lacks a scope %s needs: run `gitlab-mcp login` in a mode that grants it", a.name)
	}
	return a.fail(ClassAuth, "the signed-in token lacks the %q scope %s needs: run `gitlab-mcp login` in a mode that grants it", scope, a.name)
}

// notFound tells the catch-all route, {"error": "404 Not Found"}, from a
// missing resource. The route missing means a tier or edition the
// account lacks (§2.14, spike F); a resource missing may be one the
// token cannot see, since GitLab answers 404 for both (§2.15).
func (a answer) notFound() verdict {
	if a.env.message == "" && strings.EqualFold(strings.TrimSpace(a.env.topError), "404 Not Found") {
		return a.fail(ClassUnsupported, "GitLab has no API route for %s here: it may need an edition or tier this account lacks", a.name)
	}
	what := "the resource"
	if m := notFoundWhat.FindStringSubmatch(strings.TrimSpace(a.env.message)); m != nil && m[1] != "" {
		what = "the " + strings.ToLower(m[1])
	}
	return a.fail(ClassNotFound, "%s was not found, or you do not have access to it (GitLab answers 404 for both)", what)
}

// serverError: a call that may repeat is retried; one that may not is
// ambiguous, because the write may have landed before the failure.
func (a answer) serverError() verdict {
	if !a.repeatable {
		return a.fail(ClassAmbiguousOutcome, ambiguousText+" (status %d)", a.name, a.status)
	}
	v := a.fail(ClassUnavailable, "GitLab failed on %s (status %d); retry shortly", a.name, a.status)
	v.after = parseRetryAfter(a.h.Get("Retry-After"), time.Now())
	v.retry = v.after <= maxRetryAfter
	return v
}

func (a answer) clientError() verdict {
	switch {
	case a.status == http.StatusConflict && a.call.Witness != "":
		return a.fail(ClassStale, "the %s you passed is no longer current: read it again and retry. GitLab said: %s", a.call.Witness, a.detail)
	case a.status == http.StatusConflict:
		return a.fail(ClassConflict, "GitLab refused %s in the current state: %s", a.name, a.detail)
	case a.status == http.StatusBadRequest && staleFile.MatchString(a.env.message):
		// The Files and Commits APIs refuse a moved last_commit_id with
		// 400, not 409 (§2.10).
		return a.fail(ClassStale, "the file changed since its last_commit_id was read: read it again and retry. GitLab said: %s", a.detail)
	case a.status == http.StatusBadRequest && alreadyExists.MatchString(a.env.message):
		return a.fail(ClassConflict, "GitLab refused %s because the name is taken: %s", a.name, a.detail)
	case a.status == http.StatusPreconditionFailed:
		return a.fail(ClassStale, "this changed since it was read: read it again and retry. GitLab said: %s", a.detail)
	case a.status == http.StatusMethodNotAllowed && a.call.Method == http.MethodGet:
		// Offset pagination past 50,000 rows on an endpoint with keyset
		// (§2.12).
		return a.fail(ClassInvalid, "GitLab refuses to page this deep with offsets: narrow the filters. GitLab said: %s", a.detail)
	case a.status == http.StatusMethodNotAllowed:
		// A moved project refuses every method but GET (§2.15).
		return a.fail(ClassInvalid, "GitLab does not allow %s here; if the project was renamed or moved, address it by its numeric id or new path. GitLab said: %s", a.name, a.detail)
	case a.status == http.StatusRequestEntityTooLarge:
		return a.fail(ClassInvalid, "%s is larger than GitLab accepts: %s", a.name, a.detail)
	default:
		return a.fail(ClassInvalid, "GitLab refused %s as invalid: %s", a.name, a.detail)
	}
}

// invalidToken reports a refusal of the token itself, which GitLab marks
// invalid_token in the body or the challenge.
func invalidToken(env envelope, h http.Header) bool {
	if strings.EqualFold(env.topError, "invalid_token") {
		return true
	}
	return strings.Contains(strings.ToLower(h.Get("WWW-Authenticate")), "invalid_token")
}

func insufficientScope(env envelope, h http.Header) bool {
	if strings.EqualFold(env.topError, "insufficient_scope") {
		return true
	}
	return strings.Contains(strings.ToLower(h.Get("WWW-Authenticate")), "insufficient_scope")
}
