package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
	"github.com/ujala-singh/github-pr-concourse-resource/pr"
)

func main() {
	log.SetOutput(os.Stderr)

	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <sources>", os.Args[0])
	}
	sources := os.Args[1]

	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read stdin: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()

	var request pr.OutRequest
	if err := decoder.Decode(&request); err != nil {
		log.Fatalf("failed to unmarshal request: %v", err)
	}

	// Skip number validation for out operations - the PR number will be read from the path
	// Only validate CommonConfig (repository, auth, etc.)
	if err := request.Source.CommonConfig.Validate(); err != nil {
		log.Fatalf("invalid source configuration: %v", err)
	}

	github, err := models.NewGithubClient(request.Source.CommonConfig, request.Source.GithubConfig)
	if err != nil {
		log.Fatalf("failed to create github client: %v", err)
	}

	response, err := pr.Out(request, github, sources)
	if err != nil {
		log.Fatalf("out failed: %v", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		log.Fatalf("failed to marshal response: %v", err)
	}
}
