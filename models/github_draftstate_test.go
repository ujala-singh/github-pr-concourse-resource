package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shurcooL/githubv4"
)

// TestGetPullRequests_DraftOnlyState is an end-to-end regression test for
// DRAFT support: GitHub's real PullRequestState GraphQL enum has no DRAFT
// value (draft-ness is the separate isDraft field on an OPEN PR), so
// requesting states: ["DRAFT"] must query OPEN under the hood and filter
// the result down to only draft PRs client-side.
func TestGetPullRequests_DraftOnlyState(t *testing.T) {
	var capturedStates []string

	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables struct {
				States []string `json:"states"`
			} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode GraphQL request body: %v", err)
		}
		capturedStates = body.Variables.States

		_, _ = fmt.Fprint(w, `{"data":{"repository":{"pullRequests":{"edges":[
			{"node":{
				"number": 1, "title": "draft PR", "url": "u", "state": "OPEN", "isDraft": true,
				"baseRefName": "main", "headRefName": "f1", "headRefOid": "sha1",
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha1", "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}},
			{"node":{
				"number": 2, "title": "ready PR", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f2", "headRefOid": "sha2",
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha2", "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}}
		],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	gc := &GithubClient{
		V4:     githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: CommonConfig{Repository: "owner/repo", States: []string{"DRAFT"}},
	}

	prs, err := gc.GetPullRequests(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedStates) != 1 || capturedStates[0] != "OPEN" {
		t.Errorf("GraphQL states variable = %v, want [\"OPEN\"] (DRAFT has no real GraphQL value)", capturedStates)
	}

	if len(prs) != 1 {
		t.Fatalf("got %d PRs, want exactly 1 (the draft), got: %+v", len(prs), prs)
	}
	if prs[0].Number != 1 || !prs[0].IsDraft {
		t.Errorf("returned PR = %+v, want PR #1 (the draft one)", prs[0])
	}
}

// TestGetPullRequests_OpenAndDraftState_ReturnsAllOpenPRs verifies that
// combining OPEN and DRAFT keeps the current, long-standing default
// behavior: OPEN alone already includes drafts, so adding DRAFT alongside
// it must not filter anything out.
func TestGetPullRequests_OpenAndDraftState_ReturnsAllOpenPRs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"repository":{"pullRequests":{"edges":[
			{"node":{
				"number": 1, "title": "draft PR", "url": "u", "state": "OPEN", "isDraft": true,
				"baseRefName": "main", "headRefName": "f1", "headRefOid": "sha1",
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha1", "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}},
			{"node":{
				"number": 2, "title": "ready PR", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f2", "headRefOid": "sha2",
				"repository": {"url": "u"}, "headRepository": {"url": "u"},
				"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha2", "committedDate": "2026-01-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}
			}}
		],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	gc := &GithubClient{
		V4:     githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: CommonConfig{Repository: "owner/repo", States: []string{"OPEN", "DRAFT"}},
	}

	prs, err := gc.GetPullRequests(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(prs) != 2 {
		t.Fatalf("got %d PRs, want 2 (both the draft and the ready PR), got: %+v", len(prs), prs)
	}
}
