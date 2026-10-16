package models

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A get signs the version it fetched, in .git/resource/version.sig next to
// version.json, with a key derived from the resource's own credentials
// (the GitHub App private key, or access token), which tasks don't get.
// out acts only on a version whose signature verifies: everything else in
// the build's working copy can be changed by tasks, including a PR's own
// plan code, which could otherwise point a put at another PR and set this
// pipeline's status there.

const versionSigFile = "version.sig"

func versionKey(c CommonConfig) ([]byte, error) {
	secret := c.AccessToken
	if c.UsesGithubApp() {
		secret = c.GithubAppPrivateKey
	}
	if secret == "" {
		return nil, errors.New("no credentials to sign the version with")
	}
	sum := sha256.Sum256([]byte("git-pr/version-signature\x00" + c.Repository + "\x00" + secret))
	return sum[:], nil
}

func signVersion(c CommonConfig, v Version) (string, error) {
	key, err := versionKey(c)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v) // map[string]string: keys sorted, so deterministic
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// WriteVersionSignature writes the signature of v, the version a get just
// wrote to destDir/.git/resource/version.json.
func WriteVersionSignature(destDir string, c CommonConfig, v Version) error {
	sig, err := signVersion(c, v)
	if err != nil {
		return fmt.Errorf("failed to sign version: %w", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, ".git", "resource", versionSigFile), []byte(sig), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", versionSigFile, err)
	}
	return nil
}

// ReadSignedVersion reads the version a get of this resource fetched into
// resourceDir (a checkout's .git/resource), failing unless its signature
// verifies with this resource's credentials.
func ReadSignedVersion(resourceDir string, c CommonConfig) (Version, error) {
	raw, err := os.ReadFile(filepath.Join(resourceDir, "version.json"))
	if err != nil {
		return Version{}, fmt.Errorf("failed to read version.json (put needs a checkout made by this resource's get): %w", err)
	}
	var v Version
	if err := json.Unmarshal(raw, &v); err != nil {
		return Version{}, fmt.Errorf("failed to parse version.json: %w", err)
	}
	sig, err := os.ReadFile(filepath.Join(resourceDir, versionSigFile))
	if err != nil {
		return Version{}, fmt.Errorf("failed to read %s (put needs a checkout made by this resource's get): %w", versionSigFile, err)
	}
	want, err := signVersion(c, v)
	if err != nil {
		return Version{}, err
	}
	if !hmac.Equal([]byte(strings.TrimSpace(string(sig))), []byte(want)) {
		return Version{}, errors.New("version.json's signature doesn't verify: the checkout was changed after get, or was made by another resource")
	}
	return v, nil
}

// markerSig signs a comment-trigger note marker: key's newest handled
// trigger comment on PR pr and, if recorded, the version (base64url JSON)
// the build that handled it fetched. The PR is part of what's signed, so a
// marker copied from another PR's note (with higher comment IDs) doesn't
// verify.
func markerSig(c CommonConfig, key string, pr int, handled int64, version string) (string, error) {
	k, err := versionKey(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, k)
	fmt.Fprintf(mac, "comment-trigger\x00%s\x00%d\x00%d", key, pr, handled)
	if version != "" {
		fmt.Fprintf(mac, "\x00v=%s", version)
	}
	return hex.EncodeToString(mac.Sum(nil))[:32], nil
}

// DefuseNoteMarkers keeps text the resource posts on someone else's behalf
// (out's comment and comment_file) from carrying a comment-trigger note's
// hidden markers: "<!-- git-pr:" is shown as text instead.
func DefuseNoteMarkers(s string) string {
	return strings.ReplaceAll(s, "<!-- git-pr:", "&lt;!-- git-pr:")
}

// NoteMarker is the hidden marker PR pr's comment-trigger note carries for
// the resource with this config: its newest handled trigger comment and the
// version v that build fetched (CommentID is the comment), signed.
func NoteMarker(c CommonConfig, g GithubConfig, pr int, v Version) (string, error) {
	key := NoteKey(c, g)
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	sig, err := markerSig(c, key, pr, v.CommentID, encoded)
	if err != nil {
		return "", err
	}
	return noteMarker{v.CommentID, encoded, sig}.line(key), nil
}
