package models

import (
	"encoding/json"
	"testing"
)

func TestCommonConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  CommonConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
			},
			wantErr: false,
		},
		{
			name: "missing repository",
			config: CommonConfig{
				AccessToken: "token123",
			},
			wantErr: true,
		},
		{
			name: "missing access token",
			config: CommonConfig{
				Repository: "owner/repo",
			},
			wantErr: true,
		},
		{
			name: "invalid repository format",
			config: CommonConfig{
				Repository:  "invalid",
				AccessToken: "token123",
			},
			wantErr: true,
		},
		{
			name: "partial endpoint configuration",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				V3Endpoint:  "https://api.github.com",
			},
			wantErr: true,
		},
		{
			name: "complete endpoint configuration",
			config: CommonConfig{
				Repository:      "owner/repo",
				AccessToken:     "token123",
				V3Endpoint:      "https://api.github.com",
				V4Endpoint:      "https://api.github.com/graphql",
				HostingEndpoint: "https://github.com",
			},
			wantErr: false,
		},
		{
			name: "invalid state",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				States:      []string{"INVALID"},
			},
			wantErr: true,
		},
		{
			name: "valid states",
			config: CommonConfig{
				Repository:  "owner/repo",
				AccessToken: "token123",
				States:      []string{"OPEN", "MERGED", "CLOSED"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("CommonConfig.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCommonConfig_GetOwnerAndRepo(t *testing.T) {
	config := CommonConfig{
		Repository: "myorg/myrepo",
	}

	owner, repo := config.GetOwnerAndRepo()

	if owner != "myorg" {
		t.Errorf("Expected owner to be 'myorg', got '%s'", owner)
	}

	if repo != "myrepo" {
		t.Errorf("Expected repo to be 'myrepo', got '%s'", repo)
	}
}

func TestPathMatches(t *testing.T) {
	cases := []struct {
		pattern string
		file    string
		want    bool
	}{
		// ** recursive glob
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/terraform/US-EAST-1/EKS-Resources/main.tf", true},
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/terraform/AZURE/networking/main.tf", true},
		{"concourse-demo-setup/terraform/**", "concourse-demo-setup/helm/values.yaml", false},
		{"**/*.tf", "a/b/c/main.tf", true},
		{"**/*.tf", "main.tf", true},
		{"**/*.tf", "main.yaml", false},
		// single * does not cross path separators
		{"src/*.go", "src/main.go", true},
		{"src/*.go", "src/sub/main.go", false},
		// plain prefix matching (no glob chars)
		{"concourse-demo-setup/terraform", "concourse-demo-setup/terraform/US-EAST-1/main.tf", true},
		{"concourse-demo-setup/terraform", "concourse-demo-setup/helm/values.yaml", false},
		// exact match
		{"Makefile", "Makefile", true},
		{"Makefile", "src/Makefile", false},
	}
	for _, c := range cases {
		got := pathMatches(c.pattern, c.file)
		if got != c.want {
			t.Errorf("pathMatches(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

// TestVersion_MarshalJSON_IsProtocolCompliant guards against regressing to
// native bool/number values in the wire format. Concourse's ATC decodes a
// resource's returned versions as map[string]string; a field that
// serializes as a JSON bool or number (e.g. a populated
// ApprovedReviewCount, CommentID, or a true CommentBaseline) makes check
// fail entirely on the ATC side with "json: cannot unmarshal <type> into Go
// value of type string" — this exact bug shipped once already.
func TestVersion_MarshalJSON_IsProtocolCompliant(t *testing.T) {
	tests := []struct {
		name    string
		version Version
	}{
		{
			name:    "zero-value version",
			version: Version{PR: "42"},
		},
		{
			name:    "with approved review count",
			version: Version{PR: "42", Commit: "abc123", ApprovedReviewCount: 2},
		},
		{
			name:    "with comment id",
			version: Version{PR: "42", Commit: "abc123", CommentID: 987654321},
		},
		{
			name:    "with comment baseline true and zero comment id",
			version: Version{PR: "42", Commit: "abc123", CommentBaseline: true},
		},
		{
			name:    "with every field populated",
			version: Version{PR: "42", Commit: "abc123", CommittedDate: "2026-01-01T00:00:00Z", ApprovedReviewCount: 3, CommentID: 555, CommentBaseline: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.version)
			if err != nil {
				t.Fatalf("MarshalJSON returned error: %v", err)
			}

			// This is the exact contract Concourse's ATC enforces: every
			// value in a version object must decode as a string.
			var wire map[string]string
			if err := json.Unmarshal(data, &wire); err != nil {
				t.Fatalf("version JSON %s is not map[string]string-compatible (this is the bug that broke check on real Concourse): %v", data, err)
			}
		})
	}
}

// TestVersion_JSONRoundTrip verifies that MarshalJSON followed by
// UnmarshalJSON reproduces every field exactly, including the zero/false
// values that must NOT appear on the wire (they're simply absent from the
// map) but must still decode back to their Go zero value.
func TestVersion_JSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Version
	}{
		{
			name: "minimal version",
			in:   Version{PR: "1"},
		},
		{
			name: "commit and committed date only",
			in:   Version{PR: "1", Commit: "sha123", CommittedDate: "2026-01-01T00:00:00Z"},
		},
		{
			name: "approved review count",
			in:   Version{PR: "1", Commit: "sha123", ApprovedReviewCount: 5},
		},
		{
			name: "comment id without baseline",
			in:   Version{PR: "1", Commit: "sha123", CommentID: 42},
		},
		{
			name: "comment baseline established, no matching comment yet (CommentID stays 0)",
			in:   Version{PR: "1", Commit: "sha123", CommentBaseline: true},
		},
		{
			name: "comment baseline established with a matching comment",
			in:   Version{PR: "1", Commit: "sha123", CommentID: 42, CommentBaseline: true},
		},
		{
			name: "every field populated",
			in:   Version{PR: "1", Commit: "sha123", CommittedDate: "2026-01-01T00:00:00Z", ApprovedReviewCount: 5, CommentID: 42, CommentBaseline: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}

			var out Version
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}

			if out != tt.in {
				t.Errorf("round trip mismatch: in = %+v, out = %+v (wire = %s)", tt.in, out, data)
			}
		})
	}
}

// TestVersion_UnmarshalJSON_PreFeatureVersion verifies that a version JSON
// blob shaped like what an older binary (predating comment_id /
// comment_baseline) would have emitted still decodes cleanly, with the new
// fields defaulting to their zero values. This is the exact "upgrade from
// old version" case the comment-trigger baseline logic depends on.
func TestVersion_UnmarshalJSON_PreFeatureVersion(t *testing.T) {
	data := []byte(`{"pr":"42","commit":"abc123","committed":"2026-01-01T00:00:00Z"}`)

	var v Version
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unexpected error decoding pre-feature version: %v", err)
	}

	want := Version{PR: "42", Commit: "abc123", CommittedDate: "2026-01-01T00:00:00Z"}
	if v != want {
		t.Errorf("decoded = %+v, want %+v", v, want)
	}
	if v.CommentBaseline {
		t.Errorf("pre-feature version must decode with CommentBaseline = false")
	}
	if v.CommentID != 0 {
		t.Errorf("pre-feature version must decode with CommentID = 0")
	}
}

// TestVersion_UnmarshalJSON_InvalidNumericFields verifies that a corrupt or
// hand-edited version (non-numeric approved_review_count / comment_id)
// fails decode with a clear error instead of silently truncating to 0 or
// panicking.
func TestVersion_UnmarshalJSON_InvalidNumericFields(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "non-numeric approved_review_count",
			data: `{"pr":"42","approved_review_count":"not-a-number"}`,
		},
		{
			name: "non-numeric comment_id",
			data: `{"pr":"42","comment_id":"not-a-number"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v Version
			if err := json.Unmarshal([]byte(tt.data), &v); err == nil {
				t.Fatalf("expected an error decoding %s, got none (v = %+v)", tt.data, v)
			}
		})
	}
}

// TestVersion_UnmarshalJSON_RejectsNonStringWireValues documents that if
// something upstream of us (e.g. a hand-edited pipeline or a future
// regression) ever emits a version with a native bool/number value again,
// our own decode fails loudly rather than silently coercing — matching how
// Concourse's ATC would also reject it.
func TestVersion_UnmarshalJSON_RejectsNonStringWireValues(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "boolean comment_baseline",
			data: `{"pr":"42","comment_baseline":true}`,
		},
		{
			name: "numeric comment_id",
			data: `{"pr":"42","comment_id":42}`,
		},
		{
			name: "numeric approved_review_count",
			data: `{"pr":"42","approved_review_count":3}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v Version
			if err := json.Unmarshal([]byte(tt.data), &v); err == nil {
				t.Fatalf("expected an error decoding non-string wire value %s, got none (v = %+v)", tt.data, v)
			}
		})
	}
}
