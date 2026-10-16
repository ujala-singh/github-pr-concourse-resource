package pr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

type fakeCommit struct {
	sha, date string // date is RFC3339
	files     []string
}

// fakeSinglePR serves single PR mode's GitHub calls for PR 5: its commits
// (oldest first, the last is the head), the files each one changes
// against the commit before it, and its comments as {id, body} pairs.
func fakeSinglePR(t *testing.T, paths []string, commits []fakeCommit, comments ...[2]any) *models.GithubClient {
	t.Helper()
	head := commits[len(commits)-1]
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{
			"number": 5, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": %q,
			"repository": {"url": "u"}, "headRepository": {"url": "u"},
			"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": %q, "committedDate": %q, "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}
		}}}}`, head.sha, head.sha, head.date)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/5/commits", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, c := range commits {
			out = append(out, map[string]any{"sha": c.sha, "commit": map[string]any{"committer": map[string]any{"date": c.date}}})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	for k := 0; k < len(commits)-1; k++ {
		files := []map[string]string{}
		for _, c := range commits[k+1:] {
			for _, f := range c.files {
				files = append(files, map[string]string{"filename": f})
			}
		}
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/%s...%s", commits[k].sha, head.sha), func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
		})
	}
	mux.HandleFunc("/repos/owner/repo/git/commits/", func(w http.ResponseWriter, r *http.Request) {
		for _, c := range commits {
			if r.URL.Path == "/repos/owner/repo/git/commits/"+c.sha {
				_ = json.NewEncoder(w).Encode(map[string]any{"sha": c.sha, "committer": map[string]any{"date": c.date}})
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/5/files", func(w http.ResponseWriter, r *http.Request) {
		files := []map[string]string{}
		for _, c := range commits {
			for _, f := range c.files {
				files = append(files, map[string]string{"filename": f})
			}
		}
		_ = json.NewEncoder(w).Encode(files)
	})
	mux.HandleFunc("/repos/owner/repo/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, c := range comments {
			out = append(out, map[string]any{"id": c[0], "body": c[1]})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	v3.BaseURL = baseURL
	return &models.GithubClient{
		V3: v3,
		V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{
			Repository:      "owner/repo",
			AccessToken:     "test-token",
			Paths:           paths,
			TriggerComments: []string{"concourse plan"},
		},
	}
}

func singleCheck(t *testing.T, gc *models.GithubClient, last *models.Version) []models.Version {
	t.Helper()
	versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config, Number: 5}, Version: last}, gc)
	if err != nil {
		t.Fatal(err)
	}
	return versions
}

var (
	built     = fakeCommit{sha: "c1", date: "2026-10-01T00:00:00Z", files: []string{"terraform/main.tf"}}
	docsPush  = fakeCommit{sha: "c2", date: "2026-10-02T00:00:00Z", files: []string{"docs/readme.md"}}
	tfPush    = fakeCommit{sha: "c3", date: "2026-10-03T00:00:00Z", files: []string{"terraform/vars.tf"}}
	tfPaths   = []string{"terraform/**"}
	lastBuilt = models.Version{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z", CommentID: 100, CommentBaseline: true}
)

// assertOnlyNew checks every returned version is either last (unchanged,
// so Concourse doesn't build it) or one of want, and that each of want is
// returned, the last of them last.
func assertOnlyNew(t *testing.T, got []models.Version, last models.Version, want ...models.Version) {
	t.Helper()
	var fresh []models.Version
	for _, v := range got {
		if !reflect.DeepEqual(v, last) {
			fresh = append(fresh, v)
		}
	}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("new versions = %+v\nwant %+v\n(all returned: %+v)", fresh, want, got)
	}
	if len(want) > 0 && !reflect.DeepEqual(got[len(got)-1], want[len(want)-1]) {
		t.Fatalf("the newest version must be last: %+v", got)
	}
}

func TestCheck_NothingNewReturnsOnlyTheLastVersion(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built}, [2]any{100, "concourse plan"})
	assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt)
}

func TestCheck_NewCommentBuildsTheHead(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built}, [2]any{100, "concourse plan"}, [2]any{101, "Concourse plan"})
	assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt,
		models.Version{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z", CommentID: 101, CommentBaseline: true})
}

// A push that doesn't match paths, then a comment: only the head is
// built, never the last version's old commit again.
func TestCheck_CommentAfterPushNotMatchingPathsBuildsOnlyTheHead(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built, docsPush}, [2]any{100, "concourse plan"}, [2]any{101, "concourse plan"})
	assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt,
		models.Version{PR: "5", Commit: "c2", CommittedDate: "2026-10-02T00:00:00Z", CommentID: 101, CommentBaseline: true})
}

// A push that doesn't match paths, with no comment, builds nothing.
func TestCheck_PushNotMatchingPathsBuildsNothing(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built, docsPush}, [2]any{100, "concourse plan"})
	assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt)
}

// A push that matches paths builds the new commit, carrying the comment
// watermark forward so the old comment doesn't fire again.
func TestCheck_PushMatchingPathsBuildsTheNewCommit(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built, docsPush, tfPush}, [2]any{100, "concourse plan"})
	got := singleCheck(t, gc, &lastBuilt)
	if v := got[len(got)-1]; v.Commit != "c3" || v.CommentID != 100 {
		t.Fatalf("last version = %+v, want c3 with comment 100", v)
	}
}

// Deleting comments never builds: the newest trigger comment going away
// (an older one, or none, is now the newest) isn't a new trigger.
func TestCheck_DeletingCommentsBuildsNothing(t *testing.T) {
	for name, comments := range map[string][][2]any{
		"newest deleted": {{99, "concourse plan"}},
		"all deleted":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			gc := fakeSinglePR(t, tfPaths, []fakeCommit{built}, comments...)
			assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt)
		})
	}
}

// After deleting comments, a new comment still triggers, once.
func TestCheck_NewCommentAfterDeletionTriggersOnce(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built}, [2]any{99, "concourse plan"})
	first := singleCheck(t, gc, &lastBuilt)
	current := first[len(first)-1]

	gc = fakeSinglePR(t, tfPaths, []fakeCommit{built}, [2]any{99, "concourse plan"}, [2]any{102, "concourse plan"})
	second := singleCheck(t, gc, &current)
	assertOnlyNew(t, second, current,
		models.Version{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z", CommentID: 102, CommentBaseline: true})

	again := singleCheck(t, gc, &second[len(second)-1])
	assertOnlyNew(t, again, second[len(second)-1])
}

// The first check only records existing comments; it doesn't build them.
func TestCheck_FirstCheckDoesNotFireExistingComments(t *testing.T) {
	gc := fakeSinglePR(t, tfPaths, []fakeCommit{built}, [2]any{100, "concourse plan"})
	got := singleCheck(t, gc, nil)
	want := []models.Version{{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z", CommentID: 100, CommentBaseline: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// writeCheckout lays out what In leaves for Out: pr, head_sha and, unless
// version is nil, version.json and its signature (made with gc's
// credentials).
func writeCheckout(t *testing.T, gc *models.GithubClient, version *models.Version) string {
	t.Helper()
	sources := t.TempDir()
	dir := filepath.Join(sources, "pull-request", ".git", "resource")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"pr": "5", "head_sha": "c1"}
	if version != nil {
		files["head_sha"] = version.Commit
		b, err := version.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		files["version.json"] = string(b)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if version != nil {
		if err := models.WriteVersionSignature(filepath.Join(sources, "pull-request"), gc.Config, *version); err != nil {
			t.Fatal(err)
		}
	}
	return sources
}

// A put reports on the version that triggered the build and returns it
// unchanged, even though the PR has since moved on (a new head, a newer
// commit date, an approval), so the job isn't triggered again by its own
// put. Comment versions from list mode round-trip the same way.
func TestOut_ReturnsTheTriggeringVersionUnchanged(t *testing.T) {
	gc := fakeSinglePR(t, nil, []fakeCommit{built, tfPush}, [2]any{100, "concourse plan"})
	for _, triggering := range []models.Version{
		lastBuilt,
		{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z", ApprovedReviewCount: 2, CommentID: 7, CommentBaseline: true},
		{PR: "5", Commit: "c1", CommittedDate: "2026-10-01T00:00:00Z"},
	} {
		got, err := Out(OutRequest{Source: Source{CommonConfig: gc.Config}, Params: OutParams{Path: "pull-request"}}, gc, writeCheckout(t, gc, &triggering))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Version, triggering) {
			t.Errorf("Out version = %+v, want the triggering %+v", got.Version, triggering)
		}
	}
}

// A put trusts only the signed version from its get: a checkout without
// one, a version.json changed after the get (so its signature no longer
// verifies), or one signed with another resource's credentials fails,
// before any status is set.
func TestOut_RequiresTheGetsSignedVersion(t *testing.T) {
	gc := fakeSinglePR(t, nil, []fakeCommit{built, tfPush}, [2]any{100, "concourse plan"})
	out := func(sources string) error {
		_, err := Out(OutRequest{Source: Source{CommonConfig: gc.Config}, Params: OutParams{Path: "pull-request", Status: "success"}}, gc, sources)
		return err
	}
	if err := out(writeCheckout(t, gc, nil)); err == nil || !strings.Contains(err.Error(), "version.json") {
		t.Errorf("no version.json: err = %v", err)
	}

	sources := writeCheckout(t, gc, &lastBuilt)
	forged, _ := json.Marshal(models.Version{PR: "6", Commit: "c3", CommentID: 1 << 62})
	if err := os.WriteFile(filepath.Join(sources, "pull-request", ".git", "resource", "version.json"), forged, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := out(sources); err == nil || !strings.Contains(err.Error(), "signature doesn't verify") {
		t.Errorf("changed version.json: err = %v", err)
	}

	otherConfig := gc.Config
	otherConfig.AccessToken = "another-resource"
	other := &models.GithubClient{V3: gc.V3, V4: gc.V4, Config: otherConfig}
	if err := out(writeCheckout(t, other, &lastBuilt)); err == nil || !strings.Contains(err.Error(), "signature doesn't verify") {
		t.Errorf("another resource's signature: err = %v", err)
	}

	// Single PR mode: the signed version must be the resource's PR.
	pr6 := models.Version{PR: "6", Commit: "c1"}
	if _, err := Out(OutRequest{Source: Source{CommonConfig: gc.Config, Number: 5}, Params: OutParams{Path: "pull-request"}}, gc, writeCheckout(t, gc, &pr6)); err == nil || !strings.Contains(err.Error(), "this resource is for PR #5") {
		t.Errorf("another PR's version: err = %v", err)
	}
}

// The pr and head_sha files are ignored: the put reports on the signed
// version's PR and commit even if a task changed them.
func TestOut_IgnoresChangedPRAndHeadFiles(t *testing.T) {
	gc := fakeSinglePR(t, nil, []fakeCommit{built, tfPush}, [2]any{100, "concourse plan"})
	sources := writeCheckout(t, gc, &lastBuilt)
	for name, body := range map[string]string{"pr": "6", "head_sha": "c3"} {
		if err := os.WriteFile(filepath.Join(sources, "pull-request", ".git", "resource", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Out(OutRequest{Source: Source{CommonConfig: gc.Config}, Params: OutParams{Path: "pull-request"}}, gc, sources)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Version, lastBuilt) {
		t.Fatalf("got %+v, want the signed %+v", got.Version, lastBuilt)
	}
	for _, m := range got.Metadata {
		if m.Name == "head_sha" && m.Value != "c1" {
			t.Fatalf("head_sha metadata = %q, want the signed commit c1", m.Value)
		}
	}
}

// Without path filters the commit scan also returns the last version's own
// commit; deleting the newest trigger comment mustn't re-stamp it into a
// new version.
func TestCheck_DeletingCommentsWithoutPathFiltersBuildsNothing(t *testing.T) {
	gc := fakeSinglePR(t, nil, []fakeCommit{built}, [2]any{99, "concourse plan"})
	assertOnlyNew(t, singleCheck(t, gc, &lastBuilt), lastBuilt)
}
