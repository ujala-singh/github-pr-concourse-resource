package models

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// GitHost is the host git clones from: hosting_endpoint's, or github.com.
func GitHost(c CommonConfig) string {
	if c.HostingEndpoint == "" {
		return "github.com"
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(c.HostingEndpoint, "https://"), "http://"), "/")
}

// UseGitToken makes every git command this process runs from now on send
// token to https://host/ as an HTTP Authorization header, through git's
// GIT_CONFIG_COUNT environment, which child processes (submodule clones)
// inherit. Nothing is written to disk: a token in the clone URL would be
// saved in .git/config, FETCH_HEAD and the reflog, where any later task
// could read it. The header is scoped to host, so a submodule elsewhere
// never gets it.
func UseGitToken(host, token string) error {
	n := 0
	if s := os.Getenv("GIT_CONFIG_COUNT"); s != "" {
		var err error
		if n, err = strconv.Atoi(s); err != nil {
			return fmt.Errorf("invalid GIT_CONFIG_COUNT %q: %w", s, err)
		}
	}
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	for k, v := range map[string]string{
		fmt.Sprintf("GIT_CONFIG_KEY_%d", n):   "http.https://" + host + "/.extraheader",
		fmt.Sprintf("GIT_CONFIG_VALUE_%d", n): "Authorization: Basic " + basic,
		"GIT_CONFIG_COUNT":                    strconv.Itoa(n + 1),
	} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}
