package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
	"github.com/ujala-singh/github-pr-concourse-resource/pr"
	"github.com/ujala-singh/github-pr-concourse-resource/prlist"
)

// Request is used to determine the mode (PR list vs single PR)
type Request struct {
	Source struct {
		Number int `json:"number"`
	} `json:"source"`
}

func main() {
	log.SetOutput(os.Stderr)

	stdin, err := io.ReadAll(os.Stdin)
	if err != nil {
		log.Fatalf("failed to read stdin: %v", err)
	}

	// Determine mode based on presence of source.number
	var modeRequest Request
	if err := json.Unmarshal(stdin, &modeRequest); err != nil {
		log.Fatalf("failed to unmarshal request: %v", err)
	}

	if modeRequest.Source.Number == 0 {
		// PR List mode
		checkPRList(stdin)
	} else {
		// Single PR mode
		checkPR(stdin)
	}
}

func checkPRList(stdin []byte) {
	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()

	var request prlist.CheckRequest
	if err := decoder.Decode(&request); err != nil {
		log.Fatalf("failed to unmarshal PR list request: %v", err)
	}

	if err := request.Source.Validate(); err != nil {
		log.Fatalf("invalid source configuration: %v", err)
	}

	github, err := models.NewGithubClient(request.Source.CommonConfig, request.Source.GithubConfig)
	if err != nil {
		log.Fatalf("failed to create github client: %v", err)
	}

	response, err := prlist.Check(request, github)
	if err != nil {
		log.Fatalf("check failed: %v", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		log.Fatalf("failed to marshal response: %v", err)
	}
}

func checkPR(stdin []byte) {
	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()

	var request pr.CheckRequest
	if err := decoder.Decode(&request); err != nil {
		log.Fatalf("failed to unmarshal PR request: %v", err)
	}

	if err := request.Source.Validate(); err != nil {
		log.Fatalf("invalid source configuration: %v", err)
	}

	github, err := models.NewGithubClient(request.Source.CommonConfig, request.Source.GithubConfig)
	if err != nil {
		log.Fatalf("failed to create github client: %v", err)
	}

	response, err := pr.Check(request, github)
	if err != nil {
		log.Fatalf("check failed: %v", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		log.Fatalf("failed to marshal response: %v", err)
	}
}
