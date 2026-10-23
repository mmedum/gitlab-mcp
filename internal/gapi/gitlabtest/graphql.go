package gitlabtest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/mmedum/gitlab-mcp/v4/internal/gitlab"
)

// The GraphQL endpoint knows the one operation the server sends (§8a):
// mergeRequestReviewerRereview. As GitLab does, it answers 200 with a
// top-level error for a project or merge request the user may not change,
// whichever it is, and with the mutation's own error for a user who is
// not a reviewer. What a re-request does is rerequestReview's.

// graphQLNoAccess is GitLab's top-level error for a resource the user
// may not reach.
const graphQLNoAccess = "The resource that you are attempting to access does not exist or you don't have permission " +
	"to perform this action"

func (s *Server) serveGraphQL(w http.ResponseWriter, r *http.Request) {
	tok, status, body := s.authenticate(r)
	if tok == nil {
		writeJSON(w, status, body)
		return
	}
	if r.Method != http.MethodPost {
		routeNotFound(w)
		return
	}
	if !tok.has("api") {
		insufficientScope(w)
		return
	}
	var req struct {
		Query     string `json:"query"`
		Variables struct {
			ProjectPath string `json:"projectPath"`
			IID         string `json:"iid"`
			UserID      string `json:"userId"`
		} `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !strings.Contains(req.Query, "mergeRequestReviewerRereview(") {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]any{{"message": "unknown operation"}}})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	refused := map[string]any{"data": map[string]any{"mergeRequestReviewerRereview": nil},
		"errors": []map[string]any{{"message": graphQLNoAccess, "path": []string{"mergeRequestReviewerRereview"}}}}
	v := req.Variables
	p := s.projectByPath(v.ProjectPath)
	if p == nil || !s.visible(p, tok.user) {
		writeJSON(w, http.StatusOK, refused)
		return
	}
	mr := findMR(p, v.IID)
	if mr == nil || !s.mrUpdatable(p, mr, tok.user) {
		writeJSON(w, http.StatusOK, refused)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(v.UserID, "gid://gitlab/User/"), 10, 64)
	reviewer, ok := s.userByID(id)
	if err != nil || !ok {
		writeJSON(w, http.StatusOK, map[string]any{"errors": []map[string]any{{"message": "No object found for `userId: " +
			strconv.Quote(v.UserID) + "`"}}})
		return
	}
	if !slices.ContainsFunc(mr.Reviewers, func(u gitlab.UserBasic) bool { return u.ID == reviewer.ID }) {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
			"mergeRequestReviewerRereview": map[string]any{"errors": []string{"Reviewer not found"}}}})
		return
	}
	s.rerequestReview(p, mr, reviewer.Username)
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"mergeRequestReviewerRereview": map[string]any{"errors": []string{}}}})
}
