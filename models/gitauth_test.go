package models

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// UseGitToken's header reaches git, scoped to the host, and appends to
// any git config already in the environment.
func TestUseGitToken(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.pager")
	t.Setenv("GIT_CONFIG_VALUE_0", "cat")
	t.Setenv("GIT_CONFIG_KEY_1", "")
	t.Setenv("GIT_CONFIG_VALUE_1", "")
	if err := UseGitToken("github.com", "ghs_secret"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "config", "--get-urlmatch", "http.extraheader", "https://github.com/o/r.git").CombinedOutput()
	if err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "Authorization: Basic eC1hY2Nlc3MtdG9rZW46Z2hzX3NlY3JldA==" {
		t.Errorf("header = %q", got)
	}
	if out, _ := exec.Command("git", "config", "--get-urlmatch", "http.extraheader", "https://gitlab.example.com/x.git").CombinedOutput(); len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("header leaks to another host: %q", out)
	}
	if out, _ := exec.Command("git", "config", "core.pager").Output(); strings.TrimSpace(string(out)) != "cat" {
		t.Errorf("existing GIT_CONFIG entries were overwritten: %q", out)
	}
}

// A clone made this way leaves no trace of the token in the checkout.
func TestUseGitTokenLeavesNothingOnDisk(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "")
	t.Setenv("GIT_CONFIG_KEY_0", "")
	t.Setenv("GIT_CONFIG_VALUE_0", "")
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "-b", "main", origin)
	run(origin, "commit", "-q", "--allow-empty", "-m", "x")
	if err := UseGitToken("github.com", "ghs_secret"); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	run(root, "clone", "-q", "file://"+origin, checkout)
	run(checkout, "fetch", "-q", "origin", "main")
	err := filepath.Walk(filepath.Join(checkout, ".git"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "ghs_secret") || strings.Contains(string(b), "eC1hY2Nlc3MtdG9rZW46Z2hzX3NlY3JldA==") {
			t.Errorf("token found in %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
