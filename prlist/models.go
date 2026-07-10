package prlist

import (
	"github.com/ujala-singh/github-pr-concourse-resource/models"
)

// CheckRequest represents the request for the check operation
type CheckRequest struct {
	Source  Source          `json:"source"`
	Version *models.Version `json:"version"`
}

// Source contains the configuration for PR list mode
type Source struct {
	models.CommonConfig
	models.GithubConfig
}

// Validate validates the source configuration
func (s *Source) Validate() error {
	return s.CommonConfig.Validate()
}

// InRequest represents the request for the in operation
type InRequest struct {
	Source  Source         `json:"source"`
	Version models.Version `json:"version"`
	Params  InParams       `json:"params"`
}

// InParams contains parameters for the in operation
type InParams struct {
	SkipDownload bool `json:"skip_download"`
	GitDepth     int  `json:"git_depth"`
}

// InResponse represents the response for the in operation
type InResponse struct {
	Version  models.Version    `json:"version"`
	Metadata []models.Metadata `json:"metadata"`
}
