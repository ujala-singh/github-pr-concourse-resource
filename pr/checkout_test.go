package pr

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// originWithPR builds a bare repo shaped like GitHub's: branch main, plus
// refs/pull/1/head for a PR that changes a.txt. After the PR branched, main
// moved on (b.txt), so the base tip is NOT an ancestor of the PR head.
func originWithPR(t *testing.T) (originURL, baseTip, prHead string) {
	t.Helper()
	root := t.TempDir()
	origin, dev := filepath.Join(root, "origin.git"), filepath.Join(root, "dev")
	git(t, root, "init", "-q", "--bare", "-b", "main", origin)
	git(t, root, "clone", "-q", origin, dev)
	git(t, dev, "checkout", "-q", "-b", "main")
	write(t, dev, "a.txt", "1")
	write(t, dev, "b.txt", "1")
	git(t, dev, "add", "-A")
	git(t, dev, "commit", "-q", "-m", "base")
	git(t, dev, "push", "-q", "origin", "main")
	git(t, dev, "checkout", "-q", "-b", "feature")
	write(t, dev, "a.txt", "2")
	git(t, dev, "commit", "-q", "-am", "pr: change a")
	prHead = git(t, dev, "rev-parse", "HEAD")
	git(t, dev, "push", "-q", "origin", "HEAD:refs/pull/1/head")
	git(t, dev, "checkout", "-q", "main")
	write(t, dev, "b.txt", "2")
	git(t, dev, "commit", "-q", "-am", "main moved")
	git(t, dev, "push", "-q", "origin", "main")
	baseTip = git(t, dev, "rev-parse", "main")
	return "file://" + origin, baseTip, prHead
}

func TestCheckoutPRRecordsBaseBranchTipAsBaseSHA(t *testing.T) {
	for _, tool := range []string{"merge", "rebase", "checkout"} {
		t.Run(tool, func(t *testing.T) {
			url, baseTip, prHead := originWithPR(t)
			dest := filepath.Join(t.TempDir(), "pr")

			baseSHA, gotTool, err := checkoutPR(url, "main", 1, prHead, InParams{IntegrationTool: tool}, dest)
			if err != nil {
				t.Fatal(err)
			}
			if gotTool != tool {
				t.Fatalf("integration tool = %q, want %q", gotTool, tool)
			}
			if baseSHA != baseTip {
				t.Fatalf("base_sha = %s, want the base branch tip %s (PR head is %s)", baseSHA, baseTip, prHead)
			}
			// The PR's own change must be present in the result for every tool.
			if b, _ := os.ReadFile(filepath.Join(dest, "a.txt")); strings.TrimSpace(string(b)) != "2" {
				t.Fatalf("a.txt = %q, want the PR's change", b)
			}
		})
	}
}

func TestCheckoutPRDefaultsToMerge(t *testing.T) {
	url, baseTip, prHead := originWithPR(t)
	dest := filepath.Join(t.TempDir(), "pr")
	baseSHA, tool, err := checkoutPR(url, "main", 1, prHead, InParams{}, dest)
	if err != nil {
		t.Fatal(err)
	}
	if tool != "merge" || baseSHA != baseTip {
		t.Fatalf("tool=%q base_sha=%s, want merge and %s", tool, baseSHA, baseTip)
	}
	// merge keeps main's later change too.
	if b, _ := os.ReadFile(filepath.Join(dest, "b.txt")); strings.TrimSpace(string(b)) != "2" {
		t.Fatalf("b.txt = %q, want main's change after merge", b)
	}
}

// Concourse parses the `in` script's stdout as the JSON response, so git's
// own output (e.g. merge's "Updating ... Fast-forward") must go to stderr.
func TestCheckoutPRWritesNothingToStdout(t *testing.T) {
	url, _, prHead := originWithPR(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	_, _, cerr := checkoutPR(url, "main", 1, prHead, InParams{IntegrationTool: "merge"}, filepath.Join(t.TempDir(), "pr"))
	os.Stdout = orig
	w.Close()
	captured, _ := io.ReadAll(r)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if len(captured) != 0 {
		t.Fatalf("wrote to stdout (would corrupt the JSON response): %q", captured)
	}
}

func TestCheckoutPRRejectsUnknownIntegrationTool(t *testing.T) {
	url, _, prHead := originWithPR(t)
	if _, _, err := checkoutPR(url, "main", 1, prHead, InParams{IntegrationTool: "squash"}, filepath.Join(t.TempDir(), "pr")); err == nil {
		t.Fatal("expected an error for an unknown integration_tool")
	}
}
