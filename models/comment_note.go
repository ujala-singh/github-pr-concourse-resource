package models

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/go-github/v60/github"
)

// The comment-trigger note is one comment the resource keeps on a PR,
// edited in place, shared by every PR job and resource that uses the same
// GitHub credentials, so a PR planned by several pipelines gets one note,
// not one each. It has a row per job linking its latest build and the
// trigger comment it picked up, and, hidden, the newest trigger comment
// each resource has handled (keyed by NoteKey, so one pipeline handling a
// comment never stops another from triggering on it). It lives on GitHub
// so every check can read it, whichever version Concourse hands that
// check; prlist's bookkeeping version only reaches a check while it's the
// resource's current version.

// noteTag identifies the note.
const noteTag = "<!-- git-pr:comment-triggers -->"

// noteMarkerRE is a resource's marker: its key, the newest trigger comment
// it handled, the exact version the build that handled it fetched
// (base64url JSON), and an HMAC of all of them, and the PR, with the
// resource's signing key (see markerSig). Only a marker whose signature
// verifies counts: the App also posts task-controlled comments (out's
// comment_file), and anyone with write access can edit the App's comment,
// but neither can sign.
var noteMarkerRE = regexp.MustCompile(`<!-- git-pr:comment-trigger:([0-9a-zA-Z_.-]+):handled=([0-9]+)(?::v=([A-Za-z0-9_-]+))?:sig=([0-9a-f]{32}) -->`)

// noteKeyRE is what a NoteKey may contain, so its marker reads back.
var noteKeyRE = regexp.MustCompile(`^[0-9a-zA-Z_.-]{1,64}$`)

// noteRowRE ends a job's row: the row's data, base64url JSON. The row's
// visible text is always rendered from this, never kept as found: anyone
// with write access can edit the App's comment, and the App mustn't
// re-post what they wrote.
var noteRowRE = regexp.MustCompile(`<!-- git-pr:row:([A-Za-z0-9_-]+) -->`)

// NoteKey identifies this resource's marker on a PR's note:
// source.comment_note_key if set, otherwise a hash of the source settings
// that decide which PRs and comments the resource sees (never its
// credentials). Two resources with identical settings share a key unless
// one sets comment_note_key.
func NoteKey(c CommonConfig, g GithubConfig) string {
	if c.CommentNoteKey != "" {
		return c.CommentNoteKey
	}
	identity, _ := json.Marshal(struct {
		Repository, BaseBranch                 string
		Paths, IgnorePaths                     []string
		Labels, States, Triggers               []string
		IgnoreDrafts, DisableFork, DisableSkip bool
		RequiredApprovals                      int
	}{c.Repository, c.BaseBranch, c.Paths, c.IgnorePaths, c.Labels, c.States, c.TriggerComments,
		c.IgnoreDrafts, c.DisableForks, c.DisableCISkip, g.RequiredReviewApprovals})
	sum := sha256.Sum256(identity)
	return hex.EncodeToString(sum[:])[:12]
}

func (gc *GithubClient) noteKey() string { return NoteKey(gc.Config, gc.GithubConfig) }

// TriggerState is what a PR's comments say about comment triggers.
type TriggerState struct {
	// LatestMatchID is the newest comment matching trigger_comments, 0 if
	// none, posted at LatestMatchAt.
	LatestMatchID int64
	LatestMatchAt time.Time
	// HasNote is whether this resource's note is on the PR, and HandledID
	// the newest trigger comment it records as picked up by a build;
	// HandledVersion is the exact version that build fetched, if recorded.
	HasNote        bool
	HandledID      int64
	HandledVersion *Version
}

// TriggerState reads a PR's comments once: its newest trigger comment and
// this resource's note, if any.
func (gc *GithubClient) TriggerState(ctx context.Context, prNumber int, patterns []string) (TriggerState, error) {
	comments, err := gc.listIssueComments(ctx, prNumber)
	if err != nil {
		return TriggerState{}, err
	}
	var st TriggerState
	if m := latestMatch(comments, patterns); m != nil {
		st.LatestMatchID, st.LatestMatchAt = m.ID, m.CreatedAt
	}
	notes, err := gc.ownNotes(ctx, comments)
	if err != nil {
		return TriggerState{}, err
	}
	n := gc.mergeNotes(notes, prNumber)
	st.HandledID, st.HasNote = n.handled[gc.noteKey()]
	st.HandledVersion = n.handledVersion
	return st, nil
}

// RecordHandledTrigger records on the PR's note that build picked up the
// trigger comments up to v.CommentID, v being the exact version it
// fetched: this resource's marker moves forward to that comment (never
// back), recording v, and its row links build. Several jobs
// and pipelines may write the note at once, so it re-reads the note after
// writing and retries if another write dropped its row or marker; it also
// folds any duplicate notes (two created at once) into the oldest.
func (gc *GithubClient) RecordHandledTrigger(ctx context.Context, prNumber int, patterns []string, v Version, build BuildLink) error {
	key := gc.noteKey()
	commentID := v.CommentID
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	// A row per resource, not per job: Concourse caches a get for the same
	// resource, version and params, so another job's get of this version
	// usually doesn't run in at all.
	job := key
	owner, repo := gc.Config.GetOwnerAndRepo()
	// Only writes are limited: the delayed second look after a write gets
	// its own read, so a note that settles just as writes run out isn't
	// reported as contended.
	writes, rechecked := 0, false
	for {
		comments, err := gc.listIssueComments(ctx, prNumber)
		if err != nil {
			return err
		}
		notes, err := gc.ownNotes(ctx, comments)
		if err != nil {
			return err
		}
		n := gc.mergeNotes(notes, prNumber)

		row, hasRow := n.rows[job]
		var trigger *issueComment
		for _, c := range comments {
			if c.ID == commentID {
				trigger = c
			}
		}
		want := newRow(build, trigger, matchedPattern(trigger, patterns), job, commentID)
		// A row already at this comment (or a newer one) is kept, whichever
		// build wrote it: two builds recording the same comment at once
		// would otherwise keep overwriting each other's link and never
		// settle.
		rowDone := hasRow && row.Comment >= commentID
		if n.handled[key] >= commentID && rowDone && len(notes) <= 1 {
			// One comment triggers every pipeline at once, so their gets
			// write together: an edit from a read just before ours could
			// still land after we've seen our own, dropping it. Look once
			// more, a little later.
			if writes > 0 && !rechecked {
				rechecked = true
				time.Sleep(NoteRecheckDelay)
				continue
			}
			return nil
		}
		if writes == maxNoteWrites {
			return fmt.Errorf("failed to record comment trigger %d on PR #%d's note: other jobs kept rewriting it", commentID, prNumber)
		}
		if writes == 0 {
			time.Sleep(time.Duration(rand.Int64N(int64(NoteWriteJitter) + 1))) // de-sync gets started together
		}
		writes++
		rechecked = false // every write, a repair too, gets its own second look

		if commentID > n.handled[key] {
			sig, err := markerSig(gc.Config, key, prNumber, commentID, encoded)
			if err != nil {
				return err
			}
			n.markers[key] = noteMarker{commentID, encoded, sig}
			n.handled[key] = commentID
		}
		if !rowDone {
			if !hasRow {
				n.order = append(n.order, job)
			}
			n.rows[job] = want
		}
		body := n.renderWithin(maxNoteChars, key)
		if len(notes) == 0 {
			_, _, err = gc.V3.Issues.CreateComment(ctx, owner, repo, prNumber, &github.IssueComment{Body: github.String(body)})
		} else {
			_, _, err = gc.V3.Issues.EditComment(ctx, owner, repo, notes[0].ID, &github.IssueComment{Body: github.String(body)})
			for _, dup := range notes[1:] {
				if err == nil {
					var resp *github.Response
					resp, err = gc.V3.Issues.DeleteComment(ctx, owner, repo, dup.ID)
					if resp != nil && resp.StatusCode == http.StatusNotFound {
						err = nil // another job folded it first
					}
				}
			}
		}
		if err != nil {
			return fmt.Errorf("failed to write comment-trigger note: %w", err)
		}
	}
}

// maxNoteWrites bounds how often one get rewrites the note while other
// jobs keep changing it.
const maxNoteWrites = 5

// NoteWriteJitter bounds the random wait before a get first writes the
// note, and NoteRecheckDelay how long after writing it looks again; see
// RecordHandledTrigger. Tests set both to 0.
var (
	NoteWriteJitter  = 2 * time.Second
	NoteRecheckDelay = 3 * time.Second
)

// BuildLink is the Concourse build a note's row links to.
type BuildLink struct {
	Title string // e.g. "terraform-pr / pr-plan #12"
	URL   string
}

// BuildLinkFromEnv reads the running build from Concourse's metadata;
// concourseURL, if set, replaces ATC_EXTERNAL_URL (see
// CommonConfig.ConcourseURL).
func BuildLinkFromEnv(concourseURL string) BuildLink {
	base := concourseURL
	if base == "" {
		base = os.Getenv("ATC_EXTERNAL_URL")
	}
	base = strings.TrimRight(base, "/")
	team, pipeline, job, name := os.Getenv("BUILD_TEAM_NAME"), os.Getenv("BUILD_PIPELINE_NAME"), os.Getenv("BUILD_JOB_NAME"), os.Getenv("BUILD_NAME")
	vars := os.Getenv("BUILD_PIPELINE_INSTANCE_VARS")

	var link BuildLink
	if pipeline != "" && job != "" {
		link.Title = pipeline + " / " + job
		if name != "" {
			link.Title += " #" + name
		}
	} else if id := os.Getenv("BUILD_ID"); id != "" {
		link.Title = "build " + id
	}
	switch {
	case base == "":
	case team != "" && pipeline != "" && job != "" && name != "":
		link.URL = fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s/builds/%s", base,
			url.PathEscape(team), url.PathEscape(pipeline), url.PathEscape(job), url.PathEscape(name))
		if vars != "" {
			link.URL += "?vars=" + url.QueryEscape(vars)
		}
	case os.Getenv("BUILD_ID") != "":
		link.URL = base + "/builds/" + url.PathEscape(os.Getenv("BUILD_ID"))
	}
	return link
}

// note is the parsed note: rows in order, and each resource's marker.
type note struct {
	order []string // rows' keys, in row order
	rows  map[string]noteRow
	// markers are every resource's marker, kept as found (they're only
	// trusted by the resource that can verify them); handled is this
	// resource's own, verified.
	markers map[string]noteMarker
	handled map[string]int64
	// handledVersion is the version recorded with this resource's verified
	// marker, if any.
	handledVersion *Version
}

type noteMarker struct {
	handled int64
	version string // base64url JSON of the handled version; "" if none
	sig     string
}

func (m noteMarker) line(key string) string {
	v := ""
	if m.version != "" {
		v = ":v=" + m.version
	}
	return fmt.Sprintf("<!-- git-pr:comment-trigger:%s:handled=%d%s:sig=%s -->", key, m.handled, v, m.sig)
}

// noteRow is a job's row: its latest build that picked up a trigger, and
// that trigger comment.
type noteRow struct {
	Job        string `json:"job"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Comment    int64  `json:"comment"`
	CommentURL string `json:"comment_url,omitempty"`
	Login      string `json:"login,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
}

func newRow(build BuildLink, trigger *issueComment, pattern, job string, commentID int64) noteRow {
	r := noteRow{Job: job, Title: build.Title, URL: build.URL, Comment: commentID, Pattern: pattern}
	if trigger != nil {
		r.CommentURL, r.Login = trigger.HTMLURL, trigger.User.Login
	}
	return r
}

// mergeNotes parses notes (oldest first) into one: rows in first-seen
// order, the newest row for each, and the highest marker for each
// resource; for this resource only markers whose signature verifies.
func (gc *GithubClient) mergeNotes(notes []*issueComment, pr int) note {
	own := gc.noteKey()
	n := note{rows: map[string]noteRow{}, markers: map[string]noteMarker{}, handled: map[string]int64{}}
	for _, c := range notes {
		for _, m := range noteRowRE.FindAllStringSubmatch(c.Body, -1) {
			raw, err := base64.RawURLEncoding.DecodeString(m[1])
			if err != nil {
				continue
			}
			var r noteRow
			if json.Unmarshal(raw, &r) != nil || r.Job == "" {
				continue
			}
			prev, seen := n.rows[r.Job]
			if !seen {
				n.order = append(n.order, r.Job)
			}
			if !seen || r.Comment >= prev.Comment {
				n.rows[r.Job] = r
			}
		}
		for _, m := range noteMarkerRE.FindAllStringSubmatch(c.Body, -1) {
			id, _ := strconv.ParseInt(m[2], 10, 64)
			if m[1] == own {
				if want, err := markerSig(gc.Config, own, pr, id, m[3]); err != nil || !hmac.Equal([]byte(want), []byte(m[4])) {
					continue
				}
				if id >= n.handled[own] {
					n.handled[own] = id
					n.handledVersion = decodeVersion(m[3])
				}
			}
			if id >= n.markers[m[1]].handled {
				n.markers[m[1]] = noteMarker{id, m[3], m[4]}
			}
		}
	}
	return n
}

// maxNoteChars bounds the note's size, below GitHub's 65,536-character
// comment limit with room to spare.
const maxNoteChars = 60000

// renderWithin renders the note within limit characters, dropping the
// oldest entries first, rows and markers alike, by the trigger comment
// they record, but never own's: entries accumulate, e.g. from resources
// whose source settings changed, and past GitHub's limit no get could
// record a trigger. A dropped row only leaves the note; a dropped marker
// makes that resource's next check without the bookkeeping table re-fire
// a version Concourse already built, which builds nothing.
func (n note) renderWithin(limit int, own string) string {
	body := n.render()
	if len(body) <= limit {
		return body
	}
	type entry struct {
		key     string
		row     bool
		comment int64
		size    int
	}
	var entries []entry
	for k, r := range n.rows {
		if k != own {
			entries = append(entries, entry{k, true, r.Comment, len(r.line()) + 1})
		}
	}
	for k, m := range n.markers {
		if k != own {
			entries = append(entries, entry{k, false, m.handled, len(m.line(k)) + 1})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].comment != entries[j].comment {
			return entries[i].comment < entries[j].comment
		}
		if entries[i].row != entries[j].row {
			return entries[i].row // a row before a marker
		}
		return entries[i].key < entries[j].key
	})
	n = n.clone()
	excess := len(body) - limit
	for len(entries) > 0 && len(body) > limit {
		for excess > 0 && len(entries) > 0 {
			e := entries[0]
			entries = entries[1:]
			if e.row {
				delete(n.rows, e.key)
			} else {
				delete(n.markers, e.key)
			}
			excess -= e.size
		}
		order := n.order[:0]
		for _, k := range n.order {
			if _, ok := n.rows[k]; ok {
				order = append(order, k)
			}
		}
		n.order = order
		body = n.render()
		excess = len(body) - limit
	}
	return body
}

func (n note) clone() note {
	c := note{order: append([]string(nil), n.order...), rows: map[string]noteRow{}, markers: map[string]noteMarker{}, handled: map[string]int64{}, handledVersion: n.handledVersion}
	for k, v := range n.rows {
		c.rows[k] = v
	}
	for k, v := range n.markers {
		c.markers[k] = v
	}
	for k, v := range n.handled {
		c.handled[k] = v
	}
	return c
}

// render is the note's body. Nothing in it is copied from a trigger
// comment's text, or from the note as found: GitHub renders markdown,
// links and @-mentions even inside HTML, so either could make the
// resource's own comment ping teams or carry a marker. Rows are rendered
// from their data, every field cleaned (see line).
func (n note) render() string {
	var s strings.Builder
	s.WriteString("### 🚀 Concourse comment triggers\n\n")
	s.WriteString("| Job | Picked up |\n|---|---|\n")
	for _, job := range n.order {
		s.WriteString(n.rows[job].line())
		s.WriteString("\n")
	}
	s.WriteString("\n<sub>Comment again to run them again. This comment updates in place.</sub>\n\n")
	s.WriteString(noteTag + "\n")
	keys := make([]string, 0, len(n.markers))
	for k := range n.markers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		s.WriteString(n.markers[k].line(k) + "\n")
	}
	return s.String()
}

// line renders the row, each field cleaned: text stripped of markdown and
// HTML, links only http(s), the author only a valid login, not mentioned.
func (r noteRow) line() string {
	buildCell := cleanText(r.Title)
	if buildCell == "" {
		buildCell = "A build"
	}
	if u := cleanURL(r.URL); u != "" {
		buildCell = fmt.Sprintf("[%s](%s)", buildCell, u)
	} else {
		// Unlinked, "#9" would render as a link to issue 9.
		buildCell = strings.ReplaceAll(buildCell, "#", "#\u200b")
	}
	label := "A trigger comment"
	if p := cleanText(r.Pattern); p != "" {
		label = "A `" + p + "` comment"
	}
	commentCell := label
	if u := cleanURL(r.CommentURL); u != "" {
		commentCell = fmt.Sprintf("[%s](%s)", label, u)
	}
	if loginRE.MatchString(r.Login) {
		commentCell += " by **" + r.Login + "**"
	}
	data, _ := json.Marshal(r)
	return fmt.Sprintf("| %s | %s <!-- git-pr:row:%s --> |", buildCell, commentCell, base64.RawURLEncoding.EncodeToString(data))
}

// cleanText keeps text safe to render: only letters, digits, spaces and
// "-_./#,+=" (all that Concourse names and trigger patterns need), so no
// markdown, HTML, mention, table break or link — not even an autolinked
// URL, which needs ":", or "www." — on one line, at most 100 characters.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || strings.ContainsRune("-_./#,+=", r) {
			return r
		}
		return -1
	}, s)
	s = strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "www.", "www\u200b.")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}

// safeURLRE is the only shape of link the note renders: http(s), a host,
// and a path, query or fragment of plain URL characters. Concourse build
// and GitHub comment links fit; anything markdown could read differently
// from a URL parser (brackets, pipes, parentheses, quotes, backslashes,
// emphasis or spaces) doesn't, so no link can break out of its cell.
var safeURLRE = regexp.MustCompile(`^https?://[A-Za-z0-9.-]+(?::[0-9]+)?(?:[/?#][A-Za-z0-9\-._~%/?=&+#:]*)?$`)

// cleanURL is u if it's a link of that shape, else "".
func cleanURL(u string) string {
	if !safeURLRE.MatchString(u) {
		return ""
	}
	return u
}

// issueComment is a PR comment as GitHub's REST API returns it, with
// performed_via_github_app, which go-github's IssueComment lacks.
type issueComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	PerformedViaGithubApp *struct {
		ID int64 `json:"id"`
	} `json:"performed_via_github_app"`
}

// loginRE is a GitHub login, optionally an App's "name[bot]".
var loginRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})(?:\[bot\])?$`)

// matchedPattern is the trigger_comments pattern c's body starts with.
func matchedPattern(c *issueComment, patterns []string) string {
	if c == nil {
		return ""
	}
	body := strings.ToLower(strings.TrimSpace(c.Body))
	for _, p := range patterns {
		if strings.HasPrefix(body, strings.ToLower(strings.TrimSpace(p))) {
			return p
		}
	}
	return ""
}

// latestMatch is the newest comment whose body starts with one of
// patterns (case-insensitive), or nil.
func latestMatch(comments []*issueComment, patterns []string) *issueComment {
	var latest *issueComment
	for _, c := range comments {
		if strings.Contains(c.Body, noteTag) {
			continue
		}
		body := strings.ToLower(strings.TrimSpace(c.Body))
		for _, p := range patterns {
			if strings.HasPrefix(body, strings.ToLower(strings.TrimSpace(p))) {
				if latest == nil || c.ID > latest.ID {
					latest = c
				}
				break
			}
		}
	}
	return latest
}

// ownNotes is every note on the PR posted with this resource's own
// credentials (normally one), oldest first; anyone else's is ignored, so
// pasting a marker can't silence triggers.
func (gc *GithubClient) ownNotes(ctx context.Context, comments []*issueComment) ([]*issueComment, error) {
	var notes []*issueComment
	for _, c := range comments {
		if !strings.Contains(c.Body, noteTag) {
			continue
		}
		own, err := gc.isOwn(ctx, c)
		if err != nil {
			return nil, err
		}
		if own {
			notes = append(notes, c)
		}
	}
	return notes, nil
}

// isOwn reports whether c was posted with this client's credentials: with
// a GitHub App, by that App (not just any bot, e.g. another App or
// Actions); with a personal access token, by that token's user.
func (gc *GithubClient) isOwn(ctx context.Context, c *issueComment) (bool, error) {
	if gc.Config.UsesGithubApp() {
		return c.PerformedViaGithubApp != nil && strconv.FormatInt(c.PerformedViaGithubApp.ID, 10) == gc.Config.GithubAppID, nil
	}
	gc.selfOnce.Do(func() {
		u, _, err := gc.V3.Users.Get(ctx, "")
		var ge *github.ErrorResponse
		switch {
		case errors.As(err, &ge) && ge.Response != nil && ge.Response.StatusCode == http.StatusForbidden:
			gc.selfBot = true
		case err != nil:
			gc.selfErr = fmt.Errorf("failed to identify the token's user: %w", err)
		default:
			gc.selfLogin = u.GetLogin()
		}
	})
	if gc.selfErr != nil {
		return false, gc.selfErr
	}
	if gc.selfBot {
		return false, fmt.Errorf("the access token can't identify its user (GET /user is forbidden); use github_app_* credentials")
	}
	return strings.EqualFold(c.User.Login, gc.selfLogin), nil
}

// listIssueComments returns every comment on the PR, oldest first.
func (gc *GithubClient) listIssueComments(ctx context.Context, prNumber int) ([]*issueComment, error) {
	owner, repo := gc.Config.GetOwnerAndRepo()
	var all []*issueComment
	for page := 1; page != 0; {
		req, err := gc.V3.NewRequest(http.MethodGet, fmt.Sprintf("repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repo, prNumber, page), nil)
		if err != nil {
			return nil, err
		}
		var batch []*issueComment
		resp, err := gc.V3.Do(ctx, req, &batch)
		if err != nil {
			return nil, fmt.Errorf("failed to list PR comments: %w", err)
		}
		all = append(all, batch...)
		page = resp.NextPage
	}
	return all, nil
}

// decodeVersion is a marker's recorded version, or nil.
func decodeVersion(encoded string) *Version {
	if encoded == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	var v Version
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}
