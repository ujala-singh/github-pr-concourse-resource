package models

import "testing"

func TestDraftOnlyRequested(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		want   bool
	}{
		{
			name:   "unset states (OPEN default) is not draft-only",
			states: nil,
			want:   false,
		},
		{
			name:   "OPEN alone is not draft-only",
			states: []string{"OPEN"},
			want:   false,
		},
		{
			name:   "DRAFT alone is draft-only",
			states: []string{"DRAFT"},
			want:   true,
		},
		{
			name:   "DRAFT alongside OPEN is not draft-only — OPEN already includes drafts",
			states: []string{"OPEN", "DRAFT"},
			want:   false,
		},
		{
			name:   "DRAFT alongside MERGED (no OPEN) is draft-only",
			states: []string{"DRAFT", "MERGED"},
			want:   true,
		},
		{
			name:   "MERGED/CLOSED alone, no DRAFT, is not draft-only",
			states: []string{"MERGED", "CLOSED"},
			want:   false,
		},
		{
			name:   "case-insensitive",
			states: []string{"draft"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := draftOnlyRequested(tt.states); got != tt.want {
				t.Errorf("draftOnlyRequested(%v) = %v, want %v", tt.states, got, tt.want)
			}
		})
	}
}

func TestShouldSkipPR_DraftOnlyState(t *testing.T) {
	tests := []struct {
		name   string
		states []string
		pr     *PullRequest
		want   bool // true = skipped
	}{
		{
			name:   "states=[DRAFT]: non-draft OPEN PR is skipped",
			states: []string{"DRAFT"},
			pr:     &PullRequest{State: "OPEN", IsDraft: false},
			want:   true,
		},
		{
			name:   "states=[DRAFT]: draft OPEN PR is kept",
			states: []string{"DRAFT"},
			pr:     &PullRequest{State: "OPEN", IsDraft: true},
			want:   false,
		},
		{
			name:   "states=[DRAFT,MERGED]: a MERGED PR is kept regardless of IsDraft — draft-only filtering only applies to the OPEN bucket",
			states: []string{"DRAFT", "MERGED"},
			pr:     &PullRequest{State: "MERGED", IsDraft: false},
			want:   false,
		},
		{
			name:   "states=[OPEN,DRAFT]: a non-draft OPEN PR is kept — OPEN was also requested",
			states: []string{"OPEN", "DRAFT"},
			pr:     &PullRequest{State: "OPEN", IsDraft: false},
			want:   false,
		},
		{
			name:   "states unset (default OPEN): a non-draft OPEN PR is kept, unaffected",
			states: nil,
			pr:     &PullRequest{State: "OPEN", IsDraft: false},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gc := &GithubClient{Config: CommonConfig{States: tt.states}}
			if got := gc.shouldSkipPR(tt.pr); got != tt.want {
				t.Errorf("shouldSkipPR() = %v, want %v (pr=%+v)", got, tt.want, tt.pr)
			}
		})
	}
}

// TestShouldSkipPR_IgnoreDraftsStillWorksWithDraftState guards against a
// regression where draft-only support could accidentally override
// ignore_drafts: a config combining them is self-contradictory (asking for
// ONLY drafts while ALSO asking to exclude drafts), and ignore_drafts, the
// older and more explicit setting, must still win — a draft PR is skipped
// either way, not accidentally let through.
func TestShouldSkipPR_IgnoreDraftsStillWorksWithDraftState(t *testing.T) {
	gc := &GithubClient{Config: CommonConfig{States: []string{"DRAFT"}, IgnoreDrafts: true}}
	pr := &PullRequest{State: "OPEN", IsDraft: true}
	if !gc.shouldSkipPR(pr) {
		t.Error("expected the draft PR to be skipped — ignore_drafts must still apply")
	}
}
