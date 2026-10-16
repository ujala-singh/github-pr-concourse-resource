package prlist

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

func init() { models.NoteWriteJitter, models.NoteRecheckDelay = 0, 0 }

// fakeNoteGitHub serves PR 7 (GraphQL), its comments, and records notes
// posted to it; failPost makes posting fail.
func fakeNoteGitHub(t *testing.T, failPost bool) (*models.GithubClient, *[]string, *int) {
	t.Helper()
	var posted []string
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{
			"number": 7, "title": "t", "url": "https://github.com/owner/repo/pull/7", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "head-now",
			"repository": {"url": "u"}, "headRepository": {"url": "u"},
			"author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "head-now", "committedDate": "2026-10-02T00:00:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}}}}}`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/7/comments", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodPost {
			if failPost {
				http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
				return
			}
			var c struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&c)
			posted = append(posted, c.Body)
			_, _ = io.WriteString(w, `{"id": 99}`)
			return
		}
		comments := []map[string]any{{"id": 5, "body": "concourse plan", "html_url": "https://github.com/owner/repo/pull/7#issuecomment-5", "user": map[string]any{"login": "dev", "type": "User"}}}
		for i, body := range posted {
			comments = append(comments, map[string]any{"id": 100 + i, "body": body, "user": map[string]any{"type": "Bot"}, "performed_via_github_app": map[string]any{"id": 1}})
		}
		_ = json.NewEncoder(w).Encode(comments)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	return &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key", TriggerComments: []string{"concourse plan"}},
	}, &posted, &calls
}

func getVersion(t *testing.T, gc *models.GithubClient, v models.Version) (string, error) {
	t.Helper()
	dir := t.TempDir()
	_, err := In(InRequest{Source: Source{CommonConfig: gc.Config}, Version: v, Params: InParams{SkipDownload: true}}, gc, dir)
	return dir, err
}

// A get of a version carrying a trigger comment records it on the PR's
// note, linking the comment and the build.
func TestIn_RecordsTheTriggerOnTheNote(t *testing.T) {
	t.Setenv("ATC_EXTERNAL_URL", "https://ci.example.com")
	t.Setenv("BUILD_TEAM_NAME", "main")
	t.Setenv("BUILD_PIPELINE_NAME", "terraform-pr")
	t.Setenv("BUILD_JOB_NAME", "pr-plan")
	t.Setenv("BUILD_NAME", "3")
	gc, posted, _ := fakeNoteGitHub(t, false)
	if _, err := getVersion(t, gc, models.Version{PR: "7", Commit: "planned", CommentID: 5, CommentBaseline: true}); err != nil {
		t.Fatal(err)
	}
	if len(*posted) != 1 {
		t.Fatalf("posted %d notes, want 1", len(*posted))
	}
	for _, want := range []string{
		"| [terraform-pr / pr-plan #3](https://ci.example.com/teams/main/pipelines/terraform-pr/jobs/pr-plan/builds/3) | [A `concourse plan` comment](https://github.com/owner/repo/pull/7#issuecomment-5) by **dev**",
		":handled=5:v=",
	} {
		if !strings.Contains((*posted)[0], want) {
			t.Errorf("note missing %q:\n%s", want, (*posted)[0])
		}
	}
}

// Without a trigger comment on the version, or without trigger_comments,
// get doesn't touch comments.
func TestIn_NoTriggerNoNote(t *testing.T) {
	gc, _, calls := fakeNoteGitHub(t, false)
	if _, err := getVersion(t, gc, models.Version{PR: "7", Commit: "planned", CommentBaseline: true}); err != nil {
		t.Fatal(err)
	}
	gc.Config.TriggerComments = nil
	if _, err := getVersion(t, gc, models.Version{PR: "7", Commit: "planned", CommentID: 5}); err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("made %d comment API calls, want none", *calls)
	}
}

// Failing to record the trigger fails the get, naming the permission.
func TestIn_FailsWhenTheNoteCantBeWritten(t *testing.T) {
	gc, _, _ := fakeNoteGitHub(t, true)
	_, err := getVersion(t, gc, models.Version{PR: "7", Commit: "planned", CommentID: 5, CommentBaseline: true})
	if err == nil || !strings.Contains(err.Error(), "Pull requests: write") {
		t.Fatalf("err = %v, want one naming the permission", err)
	}
}

// head_sha is the commit the version checks out, not the PR's head now.
func TestIn_HeadSHAIsTheVersionsCommit(t *testing.T) {
	gc, _, _ := fakeNoteGitHub(t, false)
	gc.Config.TriggerComments = nil
	dir, err := getVersion(t, gc, models.Version{PR: "7", Commit: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := readFile(dir + "/.git/resource/head_sha"); b != "planned" {
		t.Fatalf("head_sha = %q, want planned", b)
	}
}

// fakeListGitHub serves an open-PR list (GraphQL) and every PR's full
// diff as matchingFiles.
func fakeListGitHub(t *testing.T, prs ...int) *models.GithubClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, n := range prs {
			edges = append(edges, fmt.Sprintf(`{"node":{"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
				"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": "2026-10-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}}}`, n, n, n))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, strings.Join(edges, ","))
	})
	for _, n := range prs {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", n), func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
		})
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/compare/sha%d...sha%d", n, n), func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("PR %d's head was diffed against itself", n)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	return &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}},
	}
}

// A closed PR's version is never echoed back (a PR closed without
// merging kept being returned as current), nor is the
// bookkeeping version; an open one is.
func TestCheck_EchoesOnlyAnOpenPR(t *testing.T) {
	gc := fakeListGitHub(t) // no open PRs
	for _, last := range []models.Version{{PR: "43", Commit: "d4225b4"}, {PR: sentinelPR, CommentBaseline: true}} {
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
		if err != nil {
			t.Fatal(err)
		}
		if len(versions) != 0 {
			t.Errorf("last %+v: got %+v, want nothing", last, versions)
		}
	}
}

// The current PR, not pushed to since, is checked against its full diff
// rather than its head against itself, so it keeps its version.
func TestCheck_UnchangedCurrentPRKeepsMatching(t *testing.T) {
	gc := fakeListGitHub(t, 6)
	last := models.Version{PR: "6", Commit: "sha6", CommittedDate: "2026-10-01T00:00:00Z"}
	versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].PR != "6" || versions[0].Commit != "sha6" {
		t.Fatalf("got %+v, want PR 6 at sha6", versions)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// A get signs the version it fetched, so this resource's put can trust it.
func TestIn_SignsTheVersion(t *testing.T) {
	gc, _, _ := fakeNoteGitHub(t, false)
	gc.Config.TriggerComments = nil
	v := models.Version{PR: "7", Commit: "planned", CommentID: 5, CommentBaseline: true}
	dir, err := getVersion(t, gc, v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := models.ReadSignedVersion(dir+"/.git/resource", gc.Config)
	if err != nil || !reflect.DeepEqual(got, v) {
		t.Fatalf("got %+v, %v; want %+v verified", got, err, v)
	}
}

// Seen live on infra-core-bootstrap-aws-common-layer: PR 14 was built (its
// commit newer than the others'), then PR 6 and PR 15 got comments. They
// triggered, but one check later, with the table recording them, ordering
// by commit date alone put PR 14 back after them; version: every builds
// what comes after the last built version, so they were never built. Their
// comments are their latest events, so they stay after PR 14.
func TestCheck_CommentedPRsStayAfterAnAlreadyBuiltNewerCommit(t *testing.T) {
	type pr struct {
		number            int
		committed, commAt string
		commentID         int64
	}
	prs := []pr{
		{6, "2026-10-13T18:17:09Z", "2026-10-15T18:26:20Z", 6199229161},
		{14, "2026-10-15T15:15:51Z", "2026-10-15T18:09:40Z", 6198949345},
		{15, "2026-10-15T06:17:56Z", "2026-10-15T18:26:35Z", 6199234387},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, p := range prs {
			edges = append(edges, fmt.Sprintf(`{"node":{"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
				"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": %q, "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}}}`, p.number, p.number, p.number, p.committed))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, strings.Join(edges, ","))
	})
	for _, p := range prs {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", p.number), func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
		})
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", p.number), func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan", "created_at": %q}]`, p.commentID, p.commAt)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}

	// The check after the triggers: the table already records every comment,
	// so nothing triggers in it.
	last := models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: map[string]int64{"6": 6199229161, "14": 6198949345, "15": 6199234387}}
	versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, v := range versions {
		order = append(order, v.PR)
	}
	if got := strings.Join(order, " "); got != "14 6 15" {
		t.Fatalf("order = %s, want 14 6 15 (each PR by its latest event)", got)
	}
}

// A comment at 10:00 on PR A and a commit at 10:01 on PR B: the order is
// the same whether the check is the one where A's comment triggers or the
// next, once it's recorded, so a version waiting to build is never moved
// behind one already built. A comment in the same second as its PR's head
// still fires.
func TestCheck_OrderDoesntChangeOnceATriggerIsRecorded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, p := range []struct {
			n  int
			at string
		}{{1, "2026-10-07T09:00:00Z"}, {2, "2026-10-07T10:01:00Z"}, {3, "2026-10-07T11:00:00Z"}} {
			edges = append(edges, fmt.Sprintf(`{"node":{"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
				"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": %q, "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}}}`, p.n, p.n, p.n, p.at))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, strings.Join(edges, ","))
	})
	for n := 1; n <= 3; n++ {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", n), func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
		})
	}
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	mux.HandleFunc("/repos/owner/repo/issues/3/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id": 300, "body": "concourse plan", "created_at": "2026-10-07T11:00:00Z"}]`) // same second as its head
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	order := func(last *models.Version) string {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: last}, gc)
		if err != nil {
			t.Fatal(err)
		}
		var prs []string
		for _, v := range versions {
			if v.PR != sentinelPR {
				prs = append(prs, v.PR)
			}
		}
		return strings.Join(prs, " ")
	}
	triggering := order(&models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: map[string]int64{"1": 0, "2": 0, "3": 0}})
	recorded := order(&models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: map[string]int64{"1": 100, "2": 0, "3": 300}})
	if triggering != "1 2 3" || recorded != triggering {
		t.Fatalf("triggering check %q, next check %q; want both \"1 2 3\"", triggering, recorded)
	}

	// Without the table or a note, PR 3's comment (same second as its head)
	// fires.
	versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &models.Version{PR: "2", Commit: "sha2"}}, gc)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		if v.PR == "3" && v.CommentID == 300 {
			return
		}
	}
	t.Fatalf("PR 3's same-second comment didn't fire: %+v", versions)
}

// A PR excluded by path filters, with a trigger comment posted in the same
// second as its head commit (both second-precision), no bookkeeping table
// and no note: it fires. Only a strictly older comment is a baseline, and
// no commit build would pick this one up otherwise.
func TestCheck_SameSecondCommentOnAPathExcludedPRFires(t *testing.T) {
	for created, wantFired := range map[string]bool{
		"2026-01-01T00:00:00Z": true,  // same second as the head commit
		"2025-12-31T23:59:59Z": false, // older: superseded by the push
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, pullRequestsGraphQLResponse(5, "aaa111")) // committed 2026-01-01T00:00:00Z
		})
		mux.HandleFunc("/repos/owner/repo/pulls/5/files", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"filename": "docs/readme.md"}]`) // outside paths
		})
		mux.HandleFunc("/repos/owner/repo/issues/5/comments", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"id": 200, "body": "concourse plan", "created_at": %q}]`, created)
		})
		server := httptest.NewServer(mux)
		v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
		baseURL, _ := url.Parse(server.URL + "/")
		v3.BaseURL = baseURL
		gc := &models.GithubClient{
			V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
			Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
				GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
		}
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &models.Version{PR: "20", Commit: "zzz"}}, gc)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		fired := false
		for _, v := range versions {
			if v.PR == "5" && v.CommentID == 200 {
				fired = true
			}
		}
		if fired != wantFired {
			t.Errorf("comment at %s: fired = %v, want %v (%+v)", created, fired, wantFired, versions)
		}
	}
}

// Two trigger comments in the same second, on PR #2 (id 100, built first)
// and PR #1 (id 101): they sort by comment ID, the order they were
// posted, on every check — never by PR number, which would put #1's
// unbuilt version before #2's built one.
func TestCheck_SameSecondCommentsSortByCommentID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, n := range []int{1, 2} {
			edges = append(edges, fmt.Sprintf(`{"node":{"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
				"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": "2026-10-01T00:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}}}`, n, n, n))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, strings.Join(edges, ","))
	})
	for n, id := range map[int]int{1: 101, 2: 100} {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", n), func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
		})
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/issues/%d/comments", n), func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"id": %d, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}]`, id)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	for _, table := range []map[string]int64{{"1": 0, "2": 0}, {"1": 0, "2": 100}, {"1": 101, "2": 100}} {
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: table}}, gc)
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, v := range versions {
			if v.PR != sentinelPR {
				order = append(order, v.PR)
			}
		}
		if got := strings.Join(order, " "); got != "2 1" {
			t.Errorf("table %v: order %q, want \"2 1\" (by comment ID)", table, got)
		}
	}
}

// check → get → check: PR 1 doesn't match paths and gets a trigger comment
// newer than its head; PR 2 matches paths. After a get records PR 1's
// comment on the note, the next check still returns PR 1's same version, in
// the same place, so another version: every job that hasn't built it yet
// doesn't skip it. PR 3's old comment, only a baseline, never gets one.
func TestCheck_HandledCommentVersionStaysInTheSnapshot(t *testing.T) {
	var noteBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		var edges []string
		for _, n := range []int{1, 2, 3} {
			edges = append(edges, fmt.Sprintf(`{"node":{"number": %d, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
				"baseRefName": "main", "headRefName": "f", "headRefOid": "sha%d",
				"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
				"commits": {"nodes": [{"commit": {"oid": "sha%d", "committedDate": "2026-10-07T09:00:00Z", "additions": 1, "deletions": 0}}]},
				"reviews": {"nodes": []}}}`, n, n, n))
		}
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, strings.Join(edges, ","))
	})
	files := map[int]string{1: "docs/a.md", 2: "terraform/main.tf", 3: "docs/b.md"}
	for n, f := range files {
		mux.HandleFunc(fmt.Sprintf("/repos/owner/repo/pulls/%d/files", n), func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `[{"filename": %q}]`, f)
		})
	}
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		comments := `{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}`
		if noteBody != "" {
			b, _ := json.Marshal(noteBody)
			comments += fmt.Sprintf(`, {"id": 150, "body": %s, "user": {"type": "Bot"}, "performed_via_github_app": {"id": 1}}`, b)
		}
		_, _ = fmt.Fprintf(w, "[%s]", comments)
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	mux.HandleFunc("/repos/owner/repo/issues/3/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id": 30, "body": "concourse plan", "created_at": "2026-10-07T09:30:00Z"}]`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	check := func(table map[string]int64) []models.Version {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: table}}, gc)
		if err != nil {
			t.Fatal(err)
		}
		var real []models.Version
		for _, v := range versions {
			if v.PR != sentinelPR {
				real = append(real, v)
			}
		}
		return real
	}

	// PR 3's comment was there when it was first seen: a baseline.
	first := check(map[string]int64{"1": 0, "2": 0, "3": 30})
	// A get of PR 1's version records the comment on its note.
	marker, err := models.NoteMarker(gc.Config, gc.GithubConfig, 1, first[len(first)-1])
	if err != nil {
		t.Fatal(err)
	}
	noteBody = "### 🚀 Concourse comment triggers\n\n<!-- git-pr:comment-triggers -->\n" + marker
	next := check(map[string]int64{"1": 100, "2": 0, "3": 30})

	if !reflect.DeepEqual(next, first) {
		t.Fatalf("after the get, the check changed:\n first %+v\n next  %+v", first, next)
	}
	var prs []string
	for _, v := range next {
		prs = append(prs, v.PR)
	}
	if got := strings.Join(prs, " "); got != "2 1" {
		t.Fatalf("versions %q, want \"2 1\" (PR 1's comment version kept, last; PR 3's baseline makes none)", got)
	}
}

// After a build handles PR 1's comment (PR 1 doesn't match paths; PR 2
// does), the check keeps returning the exact version that was built —
// not one rebuilt from the PR now. An approval doesn't make a new version,
// and a push (even of a commit dated before the comment) ends it: a new
// head needs a matching change or a fresh comment of its own.
func TestCheck_HandledVersionIsTheOneBuilt(t *testing.T) {
	head, committed, approvals := "sha1", "2026-10-07T09:00:00Z", 0
	var noteBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		pr1 := fmt.Sprintf(`{"node":{"number": 1, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": %q,
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": %q, "committedDate": %q, "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": [%s]}}}`, head, head, committed, strings.TrimSuffix(strings.Repeat(`{"state": "APPROVED", "author": {"login": "r"}},`, approvals), ","))
		pr2 := `{"node":{"number": 2, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "sha2",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "sha2", "committedDate": "2026-10-07T09:30:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}}}`
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s,%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, pr1, pr2)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/1/files", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[{"filename": "docs/a.md"}]`) })
	mux.HandleFunc("/repos/owner/repo/pulls/2/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/compare/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"files": [{"filename": "docs/a.md"}]}`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		comments := `{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}`
		if noteBody != "" {
			b, _ := json.Marshal(noteBody)
			comments += fmt.Sprintf(`, {"id": 150, "body": %s, "user": {"type": "Bot"}, "performed_via_github_app": {"id": 1}}`, b)
		}
		_, _ = fmt.Fprintf(w, "[%s]", comments)
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	pr1 := func(table map[string]int64) *models.Version {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: table}}, gc)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if v.PR == "1" {
				return &v
			}
		}
		return nil
	}

	built := pr1(map[string]int64{"1": 0, "2": 0}) // the comment triggers
	if built == nil || built.ApprovedReviewCount != 0 {
		t.Fatalf("triggered version = %+v", built)
	}
	marker, err := models.NoteMarker(gc.Config, gc.GithubConfig, 1, *built) // its get records it
	if err != nil {
		t.Fatal(err)
	}
	noteBody = "### 🚀 Concourse comment triggers\n\n<!-- git-pr:comment-triggers -->\n" + marker
	recorded := map[string]int64{"1": 100, "2": 0}

	approvals = 1
	if got := pr1(recorded); got == nil || !reflect.DeepEqual(*got, *built) {
		t.Fatalf("after an approval got %+v, want exactly the built %+v", got, built)
	}

	head, committed = "sha1-b", "2026-10-07T09:45:00Z" // pushed after the comment, dated before it
	if got := pr1(recorded); got != nil {
		t.Fatalf("after a push that doesn't match paths got %+v, want no version", got)
	}
}

// PR 1 (not matching paths) has a comment version built with no
// approvals; then it's approved, and its marker is lost to a concurrent
// note edit while another PR's version is current (no bookkeeping table).
// The comment fires again, as the same version that was built, so nothing
// builds twice.
func TestCheck_RefiredCommentAfterAnApprovalIsTheSameVersion(t *testing.T) {
	approvals := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		pr1 := fmt.Sprintf(`{"node":{"number": 1, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "sha1",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "sha1", "committedDate": "2026-10-07T09:00:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": [%s]}}}`, strings.TrimSuffix(strings.Repeat(`{"state": "APPROVED", "author": {"login": "r"}},`, approvals), ","))
		pr2 := `{"node":{"number": 2, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "sha2",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "sha2", "committedDate": "2026-10-07T09:30:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}}}`
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s,%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, pr1, pr2)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/1/files", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[{"filename": "docs/a.md"}]`) })
	mux.HandleFunc("/repos/owner/repo/pulls/2/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/compare/", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"files": []}`) })
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}]`) // no note: its marker was lost
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	pr1 := func(last models.Version) *models.Version {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if v.PR == "1" {
				return &v
			}
		}
		return nil
	}

	built := pr1(models.Version{PR: sentinelPR, CommentBaseline: true, CommentWatermarks: map[string]int64{"1": 0, "2": 0}})
	if built == nil {
		t.Fatal("the comment didn't trigger")
	}
	approvals = 1
	refired := pr1(models.Version{PR: "2", Commit: "sha2", CommittedDate: "2026-10-07T09:30:00Z"}) // table lost, marker lost
	if refired == nil || !reflect.DeepEqual(*refired, *built) {
		t.Fatalf("re-fired %+v, want exactly the built %+v", refired, built)
	}
}

// PR A is approved and has an earlier change matching paths, then a
// docs-only push and a trigger comment; PR B matches paths. Diffed from
// its last commit, A doesn't match and gets a comment version; next check
// A's version is current with its head unchanged, so it's checked against
// its full diff and matches. Its version must be the same in both, with
// no new commit, comment or approval: a comment version, whichever way A
// is matched.
func TestCheck_CommentVersionIsTheSameWhetherOrNotThePRMatchesPaths(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		a := `{"node":{"number": 1, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "a-new",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "a-new", "committedDate": "2026-10-07T09:00:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": [{"state": "APPROVED", "author": {"login": "r"}}]}}}`
		b := `{"node":{"number": 2, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "b",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "b", "committedDate": "2026-10-07T09:30:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}}}`
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s,%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, a, b)
	})
	// A's whole diff matches paths (its earlier change); its latest push doesn't.
	mux.HandleFunc("/repos/owner/repo/pulls/1/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}, {"filename": "docs/a.md"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/compare/a-old...a-new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"files": [{"filename": "docs/a.md"}]}`)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/2/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/b.tf"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &models.GithubClient{
		V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil),
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	versionOfA := func(last models.Version) models.Version {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if v.PR == "1" {
				return v
			}
		}
		t.Fatalf("no version for A: %+v", versions)
		return models.Version{}
	}

	// A's earlier version is current: diffed from it, A's push doesn't match.
	first := versionOfA(models.Version{PR: "1", Commit: "a-old", CommittedDate: "2026-10-06T00:00:00Z", ApprovedReviewCount: 1, CommentBaseline: true})
	// That version is current next: A's head is unchanged, so its full diff, which matches.
	second := versionOfA(first)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("A's version changed without a new commit, comment or approval:\n first  %+v\n second %+v", first, second)
	}
	if first.ApprovedReviewCount != 0 || first.CommentID != 100 || first.Commit != "a-new" {
		t.Fatalf("A's version = %+v, want the comment version at a-new", first)
	}
}

// As above, but A's note records an earlier handled comment, and A's
// docs-only push comes after its new trigger comment. The comment isn't
// A's latest event, so A's version keeps its approval count whichever way
// A is matched: the same version from both checks.
func TestCheck_TriggerVersionKeepsApprovalsWhenAPushFollowsTheComment(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		a := `{"node":{"number": 1, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "a-new",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "a-new", "committedDate": "2026-10-07T11:00:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": [{"state": "APPROVED", "author": {"login": "r"}}]}}}`
		b := `{"node":{"number": 2, "title": "t", "url": "u", "state": "OPEN", "isDraft": false,
			"baseRefName": "main", "headRefName": "f", "headRefOid": "b",
			"repository": {"url": "u"}, "headRepository": {"url": "u"}, "author": {"login": "a", "avatarUrl": ""}, "labels": {"nodes": []},
			"commits": {"nodes": [{"commit": {"oid": "b", "committedDate": "2026-10-07T09:30:00Z", "additions": 1, "deletions": 0}}]},
			"reviews": {"nodes": []}}}`
		_, _ = fmt.Fprintf(w, `{"data":{"repository":{"pullRequests":{"edges":[%s,%s],"pageInfo":{"endCursor":"","hasNextPage":false}}}}}`, a, b)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/1/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/main.tf"}, {"filename": "docs/a.md"}]`)
	})
	mux.HandleFunc("/repos/owner/repo/compare/a-old...a-new", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"files": [{"filename": "docs/a.md"}]}`)
	})
	mux.HandleFunc("/repos/owner/repo/pulls/2/files", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"filename": "terraform/b.tf"}]`)
	})
	gc := &models.GithubClient{
		Config: models.CommonConfig{Repository: "owner/repo", Paths: []string{"terraform/**"}, TriggerComments: []string{"concourse plan"},
			GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key"},
	}
	// A's note: comment 50 was handled, at A's earlier commit.
	marker, err := models.NoteMarker(gc.Config, gc.GithubConfig, 1, models.Version{PR: "1", Commit: "a-old", CommittedDate: "2026-10-06T00:00:00Z", CommentID: 50, CommentBaseline: true})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := json.Marshal("### 🚀 Concourse comment triggers\n\n<!-- git-pr:comment-triggers -->\n" + marker)
	mux.HandleFunc("/repos/owner/repo/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `[{"id": 50, "body": "concourse plan", "created_at": "2026-10-06T10:00:00Z"},
			{"id": 100, "body": "concourse plan", "created_at": "2026-10-07T10:00:00Z"},
			{"id": 150, "body": %s, "user": {"type": "Bot"}, "performed_via_github_app": {"id": 1}}]`, note)
	})
	mux.HandleFunc("/repos/owner/repo/issues/2/comments", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `[]`) })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc.V3, gc.V4 = v3, githubv4.NewEnterpriseClient(server.URL+"/graphql", nil)
	versionOfA := func(last models.Version) models.Version {
		t.Helper()
		versions, err := Check(CheckRequest{Source: Source{CommonConfig: gc.Config}, Version: &last}, gc)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions {
			if v.PR == "1" {
				return v
			}
		}
		t.Fatalf("no version for A: %+v", versions)
		return models.Version{}
	}

	first := versionOfA(models.Version{PR: "1", Commit: "a-old", CommittedDate: "2026-10-06T00:00:00Z", ApprovedReviewCount: 1, CommentBaseline: true})
	second := versionOfA(first)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("A's version changed without a new commit, comment or approval:\n first  %+v\n second %+v", first, second)
	}
	if first.ApprovedReviewCount != 1 || first.CommentID != 100 || first.Commit != "a-new" {
		t.Fatalf("A's version = %+v, want comment 100's version at a-new with its approval count", first)
	}
}
