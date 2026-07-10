package pr

import (
	"fmt"

	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// CheckRequest represents the request for the check operation in single PR mode
type CheckRequest struct {
	Source  Source          `json:"source"`
	Version *models.Version `json:"version"`
}

// Source contains the configuration for single PR mode
type Source struct {
	models.CommonConfig
	models.GithubConfig
	Number int `json:"number"` // The PR number to track
}

// Validate validates the source configuration
func (s *Source) Validate() error {
	if err := s.CommonConfig.Validate(); err != nil {
		return err
	}
	if s.Number <= 0 {
		return fmt.Errorf("number must be a positive integer")
	}
	return nil
}

// InRequest represents the request for the in operation
type InRequest struct {
	Source  Source         `json:"source"`
	Version models.Version `json:"version"`
	Params  InParams       `json:"params"`
}

// InParams contains parameters for the in operation
type InParams struct {
	SkipDownload     bool   `json:"skip_download"`
	IntegrationTool  string `json:"integration_tool"` // merge, rebase, or checkout
	GitDepth         int    `json:"git_depth"`
	Submodules       bool   `json:"submodules"`
	ListChangedFiles bool   `json:"list_changed_files"`
	FetchTags        bool   `json:"fetch_tags"`
}

// InResponse represents the response for the in operation
type InResponse struct {
	Version  models.Version    `json:"version"`
	Metadata []models.Metadata `json:"metadata"`
}

// OutRequest represents the request for the out operation
type OutRequest struct {
	Source Source    `json:"source"`
	Params OutParams `json:"params"`
}

// OutParams contains parameters for the out operation
type OutParams struct {
	Path          string `json:"path"`
	Status        string `json:"status"` // success, failure, error, pending
	Context       string `json:"context"`
	TargetURL     string `json:"target_url"`
	Description   string `json:"description"`
	Comment       string `json:"comment"`
	CommentFile   string `json:"comment_file"`
	DeleteComment string `json:"delete_previous_comments"`
}

// OutResponse represents the response for the out operation
type OutResponse struct {
	Version  models.Version    `json:"version"`
	Metadata []models.Metadata `json:"metadata"`
}
