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

	if len(os.Args) < 2 {
		log.Fatalf("usage: %s <destination>", os.Args[0])
	}
	destination := os.Args[1]

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
		inPRList(stdin, destination)
	} else {
		// Single PR mode
		inPR(stdin, destination)
	}
}

func inPRList(stdin []byte, destination string) {
	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()

	var request prlist.InRequest
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

	response, err := prlist.In(request, github, destination)
	if err != nil {
		log.Fatalf("in failed: %v", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		log.Fatalf("failed to marshal response: %v", err)
	}
}

func inPR(stdin []byte, destination string) {
	decoder := json.NewDecoder(bytes.NewReader(stdin))
	decoder.DisallowUnknownFields()

	var request pr.InRequest
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

	response, err := pr.In(request, github, destination)
	if err != nil {
		log.Fatalf("in failed: %v", err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		log.Fatalf("failed to marshal response: %v", err)
	}
}
