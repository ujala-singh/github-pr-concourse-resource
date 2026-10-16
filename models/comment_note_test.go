package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-github/v60/github"
	"github.com/shurcooL/githubv4"
)

func init() { NoteWriteJitter, NoteRecheckDelay = 0, 0 }

// fakePRComments is PR 7's comments on a fake GitHub that keeps what's
// posted (as GitHub App 1), edited and deleted.
type fakePRComments struct {
	mu       sync.Mutex
	comments []map[string]any
	nextID   int64
	writes   int
	// beforeEdit, if set, runs once before an edit is applied, to
	// simulate another job writing the note in between.
	beforeEdit func()
	// onGet, if set, runs (locked) before each listing, numbered from 1.
	onGet func(n int)
	gets  int
}

func (f *fakePRComments) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodGet:
		f.gets++
		if f.onGet != nil {
			f.onGet(f.gets)
		}
		_ = json.NewEncoder(w).Encode(f.comments)
	case http.MethodPost:
		var c struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&c)
		f.nextID++
		f.writes++
		f.comments = append(f.comments, map[string]any{"id": 1000 + f.nextID, "body": c.Body, "user": bot, "performed_via_github_app": viaApp})
		_, _ = io.WriteString(w, `{}`)
	case http.MethodPatch, http.MethodDelete:
		id, _ := strconv.ParseInt(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], 10, 64)
		if r.Method == http.MethodPatch && f.beforeEdit != nil {
			hook := f.beforeEdit
			f.beforeEdit = nil
			f.mu.Unlock()
			hook()
			f.mu.Lock()
		}
		for i, c := range f.comments {
			if toInt(c["id"]) != id {
				continue
			}
			f.writes++
			if r.Method == http.MethodDelete {
				f.comments = append(f.comments[:i], f.comments[i+1:]...)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			var body struct{ Body string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			c["body"] = body.Body
			_, _ = io.WriteString(w, `{}`)
			return
		}
		http.NotFound(w, r)
	}
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func (f *fakePRComments) notes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.comments {
		if b := c["body"].(string); strings.Contains(b, noteTag) {
			out = append(out, b)
		}
	}
	return out
}

func newNoteClient(t *testing.T, f *fakePRComments, config CommonConfig) *GithubClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/issues/7/comments", f.handler)
	mux.HandleFunc("/repos/owner/repo/issues/comments/", f.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	config.Repository = "owner/repo"
	return &GithubClient{V3: v3, Config: config}
}

var (
	appConfig   = CommonConfig{GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key", TriggerComments: []string{"concourse plan"}, Paths: []string{"aws/**"}}
	otherConfig = CommonConfig{GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "test-key", TriggerComments: []string{"concourse plan"}, Paths: []string{"gcp/**"}}
	bot         = map[string]any{"login": "concourse-ci[bot]", "type": "Bot"}
	viaApp      = map[string]any{"id": 1}
)

func trigger(id int64) map[string]any {
	return map[string]any{"id": id, "body": "concourse plan --all", "html_url": "https://github.com/owner/repo/pull/7#issuecomment-" + strconv.FormatInt(id, 10),
		"user": map[string]any{"login": "ujala-singh", "type": "User"}}
}

func link(job string, n int) BuildLink {
	return BuildLink{Title: job + " #" + strconv.Itoa(n), URL: "https://ci/" + strings.ReplaceAll(job, " ", "") + "/" + strconv.Itoa(n)}
}

func record(t *testing.T, gc *GithubClient, commentID int64, build BuildLink) {
	t.Helper()
	if err := gc.RecordHandledTrigger(context.Background(), 7, gc.Config.TriggerComments, Version{PR: "7", Commit: "sha7", CommentID: commentID, CommentBaseline: true}, build); err != nil {
		t.Fatal(err)
	}
}

func state(t *testing.T, gc *GithubClient) TriggerState {
	t.Helper()
	st, err := gc.TriggerState(context.Background(), 7, gc.Config.TriggerComments)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNote_FirstTriggerCreatesIt(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5)}}
	gc := newNoteClient(t, f, appConfig)
	record(t, gc, 5, link("infra-aws / pr", 9))

	notes := f.notes()
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(notes))
	}
	for _, want := range []string{
		"### 🚀 Concourse comment triggers",
		"| [infra-aws / pr #9](https://ci/infra-aws/pr/9) | [A `concourse plan` comment](https://github.com/owner/repo/pull/7#issuecomment-5) by **ujala-singh**",
		"<!-- git-pr:comment-trigger:" + gc.noteKey() + ":handled=5:v=",
	} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("note missing %q:\n%s", want, notes[0])
		}
	}
	if st := state(t, gc); !st.HasNote || st.HandledID != 5 || st.LatestMatchID != 5 {
		t.Fatalf("state = %+v", st)
	}
}

// Several pipelines share one note: a row and a marker per resource. Two
// jobs of one resource share its row (Concourse caches the get, so the
// second usually doesn't run in at all); when both do record the same
// comment, the first build's row stays, so concurrent writers settle.
func TestNote_PipelinesShareOneNote(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5)}}
	aws := newNoteClient(t, f, appConfig)
	gcp := newNoteClient(t, f, otherConfig)
	record(t, aws, 5, link("infra-aws / pr", 9))
	record(t, aws, 5, link("infra-aws / pr-lint", 3)) // same resource, another job
	record(t, gcp, 5, link("infra-gcp / pr", 4))      // another pipeline's resource

	notes := f.notes()
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want one shared note:\n%s", len(notes), strings.Join(notes, "\n---\n"))
	}
	for _, want := range []string{"infra-aws / pr #9", "infra-gcp / pr #4"} {
		if strings.Count(notes[0], want) != 1 {
			t.Errorf("want one row for %q:\n%s", want, notes[0])
		}
	}
	if strings.Contains(notes[0], "pr-lint") {
		t.Errorf("a second build recording the same comment shouldn't replace the row:\n%s", notes[0])
	}
	for _, gc := range []*GithubClient{aws, gcp} {
		if st := state(t, gc); !st.HasNote || st.HandledID != 5 {
			t.Errorf("%s: state = %+v", gc.Config.Paths, st)
		}
	}
}

// A job's next build replaces its row; the marker only moves forward, and
// an older comment never takes a row back.
func TestNote_RowsUpdateInPlaceAndOnlyForward(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5), trigger(9)}}
	gc := newNoteClient(t, f, appConfig)
	record(t, gc, 5, link("infra-aws / pr", 1))
	record(t, gc, 9, link("infra-aws / pr", 2))
	record(t, gc, 5, link("infra-aws / pr", 3)) // a rerun of the older version

	note := f.notes()[0]
	if !strings.Contains(note, "pr #2") || strings.Contains(note, "pr #1") || strings.Contains(note, "pr #3") {
		t.Fatalf("want only the row for build 2 (comment 9):\n%s", note)
	}
	if st := state(t, gc); st.HandledID != 9 {
		t.Fatalf("handled = %d, want 9", st.HandledID)
	}

	writes := f.writes
	record(t, gc, 9, link("infra-aws / pr", 2)) // nothing new
	if f.writes != writes {
		t.Fatal("re-recording the same build rewrote the note")
	}
}

// One comment triggers every pipeline at once, so another job's edit,
// made from a read before ours, can land right after we've seen our own
// write — dropping our row and marker. Looking again a little later, the
// job notices and writes them back.
func TestNote_RewritesWhatALateConcurrentEditDropped(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5), trigger(8)}}
	aws := newNoteClient(t, f, appConfig)
	gcp := newNoteClient(t, f, otherConfig)
	record(t, aws, 5, link("infra-aws / pr", 9))
	record(t, gcp, 5, link("infra-gcp / pr", 4))
	stale := f.notes()[0] // what a job reading now would base its edit on

	f.gets = 0
	f.onGet = func(n int) {
		// aws: 1 read, (write), 2 sees its write, 3 looks again. Just
		// before 3, another job's edit from the stale read lands.
		if n == 3 {
			for _, c := range f.comments {
				if strings.Contains(c["body"].(string), noteTag) {
					c["body"] = stale
				}
			}
		}
	}
	record(t, aws, 8, link("infra-aws / pr", 10))

	note := f.notes()[0]
	for _, want := range []string{"infra-aws / pr #10", "infra-gcp / pr #4"} {
		if !strings.Contains(note, want) {
			t.Errorf("lost %q:\n%s", want, note)
		}
	}
	if st := state(t, aws); st.HandledID != 8 {
		t.Errorf("aws handled = %d, want 8", st.HandledID)
	}
	if st := state(t, gcp); st.HandledID != 5 {
		t.Errorf("gcp handled = %d, want 5", st.HandledID)
	}
}

// Two notes created at once are folded into the oldest.
func TestNote_FoldsDuplicates(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	a := note{order: []string{"main/a"}, rows: map[string]noteRow{"main/a": newRow(link("a", 1), nil, "", "main/a", 5)}, markers: map[string]noteMarker{"x": {5, "", strings.Repeat("a", 32)}}}
	b := note{order: []string{"main/b"}, rows: map[string]noteRow{"main/b": newRow(link("b", 1), nil, "", "main/b", 6)}, markers: map[string]noteMarker{"x": {6, "", strings.Repeat("b", 32)}, gc.noteKey(): signed(t, gc, 2)}}
	f.comments = []map[string]any{
		trigger(6),
		{"id": int64(40), "body": a.render(), "user": bot, "performed_via_github_app": viaApp},
		{"id": int64(41), "body": b.render(), "user": bot, "performed_via_github_app": viaApp},
	}
	record(t, gc, 6, link("c", 1))

	notes := f.notes()
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(notes))
	}
	for _, want := range []string{"a #1", "b #1", "c #1", "x:handled=6:sig=" + strings.Repeat("b", 32), gc.noteKey() + ":handled=6:v="} {
		if !strings.Contains(notes[0], want) {
			t.Errorf("merged note missing %q:\n%s", want, notes[0])
		}
	}
	if toInt(f.comments[1]["id"]) != 40 {
		t.Error("the oldest note should be kept")
	}
}

// Notes that aren't this resource's own are ignored: posted by another
// App, or by a person pasting one.
func TestNote_IgnoresNotesThatArentOurs(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	forged := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{gc.noteKey(): signed(t, gc, 99)}}.render()
	f.comments = []map[string]any{
		trigger(5),
		{"id": int64(40), "body": forged, "user": bot, "performed_via_github_app": map[string]any{"id": 2}},
		{"id": int64(41), "body": forged, "user": map[string]any{"login": "mallory", "type": "User"}},
	}
	if st := state(t, gc); st.HasNote {
		t.Fatalf("state = %+v, want no note of ours", st)
	}
	record(t, gc, 5, link("a", 1))
	if n := len(f.notes()); n != 3 {
		t.Fatalf("got %d notes, want ours created alongside the two ignored ones", n)
	}
	if st := state(t, gc); st.HandledID != 5 {
		t.Fatalf("handled = %d, want 5", st.HandledID)
	}
}

// Nothing in the note comes from a trigger comment's text, so a comment
// can't put a marker, markdown or an @-mention in it.
func TestNote_TriggerTextCantGetIn(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	forged := "concourse plan @dbt-labs/everyone [x](https://evil) | <!-- git-pr:comment-trigger:" + gc.noteKey() + ":handled=99999:sig=" + strings.Repeat("0", 32) + " -->"
	f.comments = []map[string]any{{"id": int64(5), "body": forged, "user": map[string]any{"login": "mallory](https://evil)", "type": "User"}}}
	record(t, gc, 5, link("a", 1))
	note := f.notes()[0]
	for _, bad := range []string{"@dbt-labs", "evil", "99999", "mallory"} {
		if strings.Contains(note, bad) {
			t.Fatalf("note carries %q from the comment:\n%s", bad, note)
		}
	}
	if st := state(t, gc); st.HandledID != 5 {
		t.Fatalf("handled = %d, want 5", st.HandledID)
	}
}

// With a personal access token, only that token's user's notes count.
func TestNote_TokenOwnsItsUsersNotes(t *testing.T) {
	cfg := CommonConfig{Repository: "owner/repo", AccessToken: "x", TriggerComments: appConfig.TriggerComments}
	sig, err := markerSig(cfg, NoteKey(cfg, GithubConfig{}), 7, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	body := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{NoteKey(cfg, GithubConfig{}): {5, "", sig}}}.render()
	f := &fakePRComments{comments: []map[string]any{trigger(5), {"id": int64(40), "body": body, "user": map[string]any{"login": "ci-user", "type": "User"}}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"login": "CI-User"}`) })
	mux.HandleFunc("/repos/owner/repo/issues/7/comments", f.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &GithubClient{V3: v3, Config: cfg}

	if st := state(t, gc); !st.HasNote || st.HandledID != 5 {
		t.Fatalf("got %+v, want the token user's note at 5", st)
	}
}

func TestNoteKey(t *testing.T) {
	base := CommonConfig{Repository: "o/r", Paths: []string{"a/**"}, TriggerComments: []string{"concourse plan"}}
	withCreds := base
	withCreds.AccessToken, withCreds.GithubAppPrivateKey, withCreds.CheckConcurrency = "secret", "pem", 9
	if NoteKey(base, GithubConfig{}) != NoteKey(withCreds, GithubConfig{}) {
		t.Error("credentials and tuning must not change the key")
	}
	otherPaths := base
	otherPaths.Paths = []string{"b/**"}
	if NoteKey(base, GithubConfig{}) == NoteKey(otherPaths, GithubConfig{}) {
		t.Error("resources watching different paths need different keys")
	}
	named := base
	named.CommentNoteKey = "staging-plan"
	if NoteKey(named, GithubConfig{}) != "staging-plan" {
		t.Errorf("comment_note_key should be used as is, got %q", NoteKey(named, GithubConfig{}))
	}
}

func TestBuildLinkFromEnv(t *testing.T) {
	for k, v := range map[string]string{
		"ATC_EXTERNAL_URL": "https://ci.example.com/", "BUILD_TEAM_NAME": "main", "BUILD_PIPELINE_NAME": "terraform pr",
		"BUILD_JOB_NAME": "pr-plan", "BUILD_NAME": "12", "BUILD_ID": "345", "BUILD_PIPELINE_INSTANCE_VARS": "",
	} {
		t.Setenv(k, v)
	}
	got := BuildLinkFromEnv("")
	want := BuildLink{Title: "terraform pr / pr-plan #12", URL: "https://ci.example.com/teams/main/pipelines/terraform%20pr/jobs/pr-plan/builds/12"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := BuildLinkFromEnv("https://proxy.example.com"); !strings.HasPrefix(got.URL, "https://proxy.example.com/teams/") {
		t.Errorf("concourse_url should replace ATC_EXTERNAL_URL, got %q", got.URL)
	}
}

// Someone with write access editing the note's visible text: the App
// never re-posts it, since rows are rendered from their data, cleaned.
func TestNote_HandEditsArentRepublished(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5), trigger(9)}}
	gc := newNoteClient(t, f, appConfig)
	record(t, gc, 5, link("infra-aws / pr", 1))
	for _, c := range f.comments {
		if b := c["body"].(string); strings.Contains(b, noteTag) {
			c["body"] = strings.Replace(b, "| Job |", "@dbt-labs/everyone [click](https://evil)\n| Job |", 1)
		}
	}
	// A row whose data was edited to carry markup is cleaned when rendered.
	evil := newRow(BuildLink{Title: "x](https://evil) @team", URL: "javascript:alert(1)"}, nil, "`@team`", "main/evil", 1)
	for _, c := range f.comments {
		if b := c["body"].(string); strings.Contains(b, noteTag) {
			c["body"] = strings.Replace(b, noteTag, evil.line()+"\n"+noteTag, 1)
		}
	}
	record(t, gc, 9, link("infra-aws / pr", 2))
	note := f.notes()[0]
	visible := noteRowRE.ReplaceAllString(note, "")
	for _, bad := range []string{"@", "https://evil", "evil)", "javascript", "click"} {
		if strings.Contains(visible, bad) {
			t.Errorf("note re-posts %q:\n%s", bad, note)
		}
	}
}

// Only plain http(s) links are rendered: anything markdown could parse
// differently from a URL parser is dropped, leaving the text unlinked.
func TestCleanURL(t *testing.T) {
	for _, ok := range []string{
		"https://ci.example.com/teams/main/pipelines/infra/jobs/pr/builds/9",
		"https://ci.example.com/teams/main/pipelines/infra/jobs/pr/builds/9?vars=%7B%22env%22%3A%22prod%22%7D",
		"https://github.com/dbt-labs/repo/pull/14#issuecomment-6194873524",
		"http://localhost:8080/builds/3",
	} {
		if cleanURL(ok) != ok {
			t.Errorf("cleanURL(%q) dropped a plain link", ok)
		}
	}
	for _, bad := range []string{
		"javascript:alert(1)",
		"https://ci/x|y",
		"https://ci/x](https://evil)",
		"https://ci/x)(",
		"https://ci/x y",
		"https://ci/*bold*",
		`https://ci/x\`,
		`https://ci/x"onclick`,
		"https://ci/x<y>",
		"https://ci/[x]",
		"https://user@evil.com/",
		"//evil.com",
		"",
	} {
		if got := cleanURL(bad); got != "" {
			t.Errorf("cleanURL(%q) = %q, want it dropped", bad, got)
		}
	}
}

func TestRowLineWithoutALinkDoesntReferenceIssues(t *testing.T) {
	line := noteRow{Job: "j", Title: "infra / pr #9", URL: "not a url", Comment: 1}.line()
	if strings.Contains(line, "#9") {
		t.Fatalf("an unlinked #9 would link issue 9: %s", line)
	}
}

func signed(t *testing.T, gc *GithubClient, handled int64) noteMarker {
	t.Helper()
	sig, err := markerSig(gc.Config, gc.noteKey(), 7, handled, "")
	if err != nil {
		t.Fatal(err)
	}
	return noteMarker{handled, "", sig}
}

// The App also posts task-controlled comments (out's comment_file), so a
// note it posted isn't enough: a marker counts only if it's signed with
// this resource's key. A forged one, even in the App's own note, is
// ignored, and doesn't hide the real one.
func TestNote_UnsignedMarkersDontCount(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	forged := strings.Repeat("0", 32)
	other := newNoteClient(t, &fakePRComments{}, CommonConfig{GithubAppID: "1", GithubAppInstallationID: "2", GithubAppPrivateKey: "another-key", TriggerComments: appConfig.TriggerComments, Paths: appConfig.Paths})
	f.comments = []map[string]any{
		trigger(5),
		{"id": int64(40), "body": note{rows: map[string]noteRow{}, markers: map[string]noteMarker{gc.noteKey(): {99, "", forged}}}.render(), "user": bot, "performed_via_github_app": viaApp},
		{"id": int64(41), "body": note{rows: map[string]noteRow{}, markers: map[string]noteMarker{gc.noteKey(): signed(t, other, 98)}}.render(), "user": bot, "performed_via_github_app": viaApp},
	}
	if st := state(t, gc); st.HasNote {
		t.Fatalf("state = %+v, want no verified marker", st)
	}
	record(t, gc, 5, link("a", 1))
	if st := state(t, gc); !st.HasNote || st.HandledID != 5 {
		t.Fatalf("state = %+v, want handled 5 despite the forged 99", st)
	}
}

func TestDefuseNoteMarkers(t *testing.T) {
	in := "plan output <!-- git-pr:comment-triggers --> <!-- git-pr:comment-trigger:k:handled=9:sig=" + strings.Repeat("0", 32) + " --> <!-- other -->"
	out := DefuseNoteMarkers(in)
	if strings.Contains(out, "<!-- git-pr:") || noteMarkerRE.MatchString(out) || !strings.Contains(out, "<!-- other -->") {
		t.Fatalf("got %q", out)
	}
}

func TestCommentNoteKeyIsValidated(t *testing.T) {
	for key, ok := range map[string]bool{"staging-plan": true, "infra_aws.v2": true, "infra/aws": false, "a b": false, strings.Repeat("a", 65): false} {
		c := CommonConfig{Repository: "o/r", AccessToken: "x", CommentNoteKey: key}
		if err := c.Validate(); (err == nil) != ok {
			t.Errorf("comment_note_key %q: err = %v, want ok=%v", key, err, ok)
		}
	}
}

func TestNoteKeyIncludesEveryEligibilityFilter(t *testing.T) {
	base := CommonConfig{Repository: "o/r"}
	skip := base
	skip.DisableCISkip = true
	if NoteKey(base, GithubConfig{}) == NoteKey(skip, GithubConfig{}) {
		t.Error("disable_ci_skip changes which PRs the resource sees")
	}
	if NoteKey(base, GithubConfig{}) == NoteKey(base, GithubConfig{RequiredReviewApprovals: 2}) {
		t.Error("required_review_approvals changes which PRs the resource sees")
	}
}

// delete_previous_comments never deletes the comment-trigger note.
func TestDeletePreviousCommentsKeepsTheNote(t *testing.T) {
	var deleted []string
	mux := http.NewServeMux()
	mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "viewer") {
			_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"concourse-ci"}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"repository":{"pullRequest":{"comments":{"edges":[
			{"node":{"databaseId":1,"body":"plan output","author":{"login":"concourse-ci"}}},
			{"node":{"databaseId":2,"body":"### 🚀 Concourse comment triggers\n<!-- git-pr:comment-triggers -->","author":{"login":"concourse-ci"}}},
			{"node":{"databaseId":3,"body":"lgtm","author":{"login":"someone"}}}]}}}}}`)
	})
	mux.HandleFunc("/repos/owner/repo/issues/comments/", func(w http.ResponseWriter, r *http.Request) {
		deleted = append(deleted, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &GithubClient{V3: v3, V4: githubv4.NewEnterpriseClient(server.URL+"/graphql", nil), Config: CommonConfig{Repository: "owner/repo"}}
	if err := gc.DeletePreviousComments(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if strings.Join(deleted, ",") != "1" {
		t.Fatalf("deleted %v, want only comment 1", deleted)
	}
}

// A marker copied from another PR's note doesn't verify: the PR is part of
// what's signed.
func TestNote_MarkerFromAnotherPRDoesntCount(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	sig, err := markerSig(gc.Config, gc.noteKey(), 8, 999, "")
	if err != nil {
		t.Fatal(err)
	}
	f.comments = []map[string]any{
		trigger(5),
		{"id": int64(40), "body": note{rows: map[string]noteRow{}, markers: map[string]noteMarker{gc.noteKey(): {999, "", sig}}}.render(), "user": bot, "performed_via_github_app": viaApp},
	}
	if st := state(t, gc); st.HasNote {
		t.Fatalf("state = %+v: PR 8's marker counted on PR 7", st)
	}
}

// Two builds of one resource recording the same comment at once: the
// note already holds an older comment (4), and after every write this
// build makes, the other build's write of comment 5 — with its own build
// link — lands on top. They settle: this get succeeds on the other build's
// row at comment 5, instead of rewriting a row that differs only by its
// link until both gets run out of attempts.
func TestNote_ConcurrentRecordsOfTheSameCommentSettle(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(4), trigger(5)}}
	gc := newNoteClient(t, f, appConfig)
	record(t, gc, 4, link("infra-aws / pr", 8))

	other := note{
		order:   []string{gc.noteKey()},
		rows:    map[string]noteRow{gc.noteKey(): newRow(link("infra-aws / pr-lint", 3), nil, "concourse plan", gc.noteKey(), 5)},
		markers: map[string]noteMarker{gc.noteKey(): signed(t, gc, 5)},
	}.render()
	competing := 0
	f.gets = 0
	f.onGet = func(n int) {
		if n >= 2 { // every read after this build's first write
			competing++
			for _, c := range f.comments {
				if strings.Contains(c["body"].(string), noteTag) {
					c["body"] = other
				}
			}
		}
	}
	writes := f.writes
	record(t, gc, 5, link("infra-aws / pr", 9))
	if competing == 0 {
		t.Fatal("the competing writer never ran: this test isn't exercising a concurrent write")
	}
	if got := f.writes - writes; got != 1 {
		t.Fatalf("wrote %d times, want 1: a row at comment 5 from the other build should be accepted", got)
	}

	note := f.notes()[0]
	if strings.Count(note, "git-pr:row:") != 1 || !strings.Contains(note, "pr-lint #3") {
		t.Fatalf("want the other build's row at comment 5:\n%s", note)
	}
	if st := state(t, gc); st.HandledID != 5 {
		t.Fatalf("handled = %d, want 5", st.HandledID)
	}
}

// A note crowded with other resources' rows and markers (old ones from
// changed source settings never leave) stays under GitHub's comment limit:
// the oldest entries go first, rows and markers alike, never this
// resource's, so a get can always record its trigger.
func TestNote_StaysUnderGitHubsCommentLimit(t *testing.T) {
	f := &fakePRComments{}
	gc := newNoteClient(t, f, appConfig)
	crowded := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{}}
	for i := range 600 {
		key := fmt.Sprintf("old-resource-%03d", i)
		crowded.order = append(crowded.order, key)
		crowded.rows[key] = newRow(BuildLink{Title: "some-long-pipeline-name / pr #" + strconv.Itoa(i), URL: "https://ci.example.com/teams/main/pipelines/some-long-pipeline-name/jobs/pr/builds/" + strconv.Itoa(i)}, nil, "concourse plan", key, int64(1000+i))
		crowded.markers[key] = noteMarker{int64(1000 + i), "", strings.Repeat("a", 32)}
	}
	if len(crowded.render()) <= maxNoteChars {
		t.Fatalf("fixture too small: %d chars", len(crowded.render()))
	}
	f.comments = []map[string]any{trigger(5000), {"id": int64(40), "body": crowded.render(), "user": bot, "performed_via_github_app": viaApp}}
	record(t, gc, 5000, link("infra-aws / pr", 9))

	note := f.notes()[0]
	if len(note) > maxNoteChars {
		t.Fatalf("note is %d chars, over %d", len(note), maxNoteChars)
	}
	if !strings.Contains(note, "infra-aws / pr #9") || !strings.Contains(note, gc.noteKey()+":handled=5000:") {
		t.Fatal("this resource's row and marker must be kept")
	}
	if strings.Contains(note, "pr #0]") || !strings.Contains(note, "pr #599]") ||
		strings.Contains(note, "old-resource-000:") || !strings.Contains(note, "old-resource-599:") {
		t.Error("the oldest rows and markers should go first, the newest stay")
	}
	if st := state(t, gc); st.HandledID != 5000 {
		t.Fatalf("handled = %d, want 5000", st.HandledID)
	}
}

// Competing edits drop this resource's entry for its next three reads;
// the fourth write sticks and the note settles. The delayed second look
// gets its own read, so the get succeeds instead of running out of
// attempts on a note that has settled.
func TestNote_SettlingOnTheLastWriteSucceeds(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5)}}
	gc := newNoteClient(t, f, appConfig)
	stale := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{}}.render()
	f.comments = append(f.comments, map[string]any{"id": int64(40), "body": stale, "user": bot, "performed_via_github_app": viaApp})
	f.onGet = func(n int) {
		if n >= 2 && n <= 4 { // another job's edit lands right after each of our writes
			for _, c := range f.comments {
				if strings.Contains(c["body"].(string), noteTag) {
					c["body"] = stale
				}
			}
		}
	}
	record(t, gc, 5, link("infra-aws / pr", 9))
	if st := state(t, gc); st.HandledID != 5 {
		t.Fatalf("handled = %d, want 5", st.HandledID)
	}
}

// A note that never settles still fails the get after a bounded number of
// writes.
func TestNote_EndlessContentionFails(t *testing.T) {
	f := &fakePRComments{comments: []map[string]any{trigger(5)}}
	gc := newNoteClient(t, f, appConfig)
	stale := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{}}.render()
	f.comments = append(f.comments, map[string]any{"id": int64(40), "body": stale, "user": bot, "performed_via_github_app": viaApp})
	f.onGet = func(int) {
		for _, c := range f.comments {
			if strings.Contains(c["body"].(string), noteTag) {
				c["body"] = stale
			}
		}
	}
	err := gc.RecordHandledTrigger(context.Background(), 7, gc.Config.TriggerComments, Version{PR: "7", CommentID: 5}, link("a", 1))
	if err == nil || !strings.Contains(err.Error(), "kept rewriting") {
		t.Fatalf("err = %v, want contention reported", err)
	}
	if f.writes != maxNoteWrites {
		t.Fatalf("wrote %d times, want %d", f.writes, maxNoteWrites)
	}
}

// access_token with an incomplete App configuration (only github_app_id):
// the client authenticates with the token, so the token user's notes are
// this resource's own, not the App's, and recording a trigger edits that
// note instead of creating new ones until it fails.
func TestNote_IncompleteAppConfigUsesTheToken(t *testing.T) {
	cfg := CommonConfig{Repository: "owner/repo", AccessToken: "x", GithubAppID: "1", TriggerComments: appConfig.TriggerComments}
	if cfg.UsesGithubApp() {
		t.Fatal("an App ID alone isn't App authentication")
	}
	sig, err := markerSig(cfg, NoteKey(cfg, GithubConfig{}), 7, 5, "")
	if err != nil {
		t.Fatal(err)
	}
	body := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{NoteKey(cfg, GithubConfig{}): {5, "", sig}}}.render()
	f := &fakePRComments{comments: []map[string]any{trigger(5), trigger(9), {"id": int64(40), "body": body, "user": map[string]any{"login": "ci-user", "type": "User"}}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"login": "ci-user"}`) })
	mux.HandleFunc("/repos/owner/repo/issues/7/comments", f.handler)
	mux.HandleFunc("/repos/owner/repo/issues/comments/", f.handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	v3 := github.NewClient(&http.Client{Transport: &http.Transport{}})
	baseURL, _ := url.Parse(server.URL + "/")
	v3.BaseURL = baseURL
	gc := &GithubClient{V3: v3, Config: cfg}

	if st := state(t, gc); !st.HasNote || st.HandledID != 5 {
		t.Fatalf("state = %+v, want the token user's note at 5", st)
	}
	record(t, gc, 9, link("a", 1))
	if n := len(f.notes()); n != 1 {
		t.Fatalf("got %d notes, want the one edited", n)
	}
	if st := state(t, gc); st.HandledID != 9 {
		t.Fatalf("handled = %d, want 9", st.HandledID)
	}
}
