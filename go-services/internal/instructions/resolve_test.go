package instructions

import (
	"testing"

	"ai-reviewer/go-services/internal/db"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		name         string
		rows         []db.InstructionRow
		repoID       string
		changedFiles []string
		want         []string
	}{
		{
			name: "no_filters_matches_everything",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Always check for nil", RepoFilter: nil, FilePatternFilter: nil},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go", "README.md"},
			want:         []string{"Always check for nil"},
		},
		{
			name: "repo_filter_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Check Go code", RepoFilter: []string{"repo-1"}, FilePatternFilter: nil},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go"},
			want:         []string{"Check Go code"},
		},
		{
			name: "repo_filter_no_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Check Go code", RepoFilter: []string{"other-repo"}, FilePatternFilter: nil},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go"},
			want:         nil,
		},
		{
			name: "file_pattern_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Go patterns", RepoFilter: nil, FilePatternFilter: []string{"*.go"}},
			},
			repoID:       "repo-1",
			changedFiles: []string{"handler.go", "README.md"},
			want:         []string{"Go patterns"},
		},
		{
			name: "file_pattern_no_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Go patterns", RepoFilter: nil, FilePatternFilter: []string{"*.go"}},
			},
			repoID:       "repo-1",
			changedFiles: []string{"README.md", "docs/usage.md"},
			want:         nil,
		},
		{
			name: "both_filters_repo_match_file_no_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Go patterns", RepoFilter: []string{"repo-1"}, FilePatternFilter: []string{"*.go"}},
			},
			repoID:       "repo-1",
			changedFiles: []string{"README.md"},
			want:         nil,
		},
		{
			name: "both_filters_both_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Go patterns", RepoFilter: []string{"repo-1"}, FilePatternFilter: []string{"*.go"}},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go", "handler.go"},
			want:         []string{"Go patterns"},
		},
		{
			name:         "empty_rows",
			rows:         nil,
			repoID:       "repo-1",
			changedFiles: []string{"main.go"},
			want:         nil,
		},
		{
			name: "multiple_instructions_all_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Instruction A", RepoFilter: nil, FilePatternFilter: nil},
				{ID: "2", Content: "Instruction B", RepoFilter: []string{"repo-1"}, FilePatternFilter: nil},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go"},
			want:         []string{"Instruction A", "Instruction B"},
		},
		{
			name: "multiple_instructions_some_match",
			rows: []db.InstructionRow{
				{ID: "1", Content: "Instruction A", RepoFilter: nil, FilePatternFilter: nil},
				{ID: "2", Content: "Instruction B", RepoFilter: []string{"other-repo"}, FilePatternFilter: nil},
				{ID: "3", Content: "Instruction C", RepoFilter: []string{"repo-1"}, FilePatternFilter: []string{"*.go"}},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go", "handler.go"},
			want:         []string{"Instruction A", "Instruction C"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.rows, tt.repoID, tt.changedFiles)
			if len(got) != len(tt.want) {
				t.Errorf("Resolve() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Resolve()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestMerge(t *testing.T) {
	tests := []struct {
		name             string
		apiInstructions  []string
		yamlInstructions []string
		want             []string
	}{
		{
			name:             "both_empty",
			apiInstructions:  nil,
			yamlInstructions: nil,
			want:             nil,
		},
		{
			name:             "only_api",
			apiInstructions:  []string{"API instruction 1", "API instruction 2"},
			yamlInstructions: nil,
			want:             []string{"API instruction 1", "API instruction 2"},
		},
		{
			name:             "only_yaml",
			apiInstructions:  nil,
			yamlInstructions: []string{"YAML instruction 1", "YAML instruction 2"},
			want:             []string{"YAML instruction 1", "YAML instruction 2"},
		},
		{
			name:             "both_present",
			apiInstructions:  []string{"API instruction 1"},
			yamlInstructions: []string{"YAML instruction 1"},
			want:             []string{"API instruction 1", "YAML instruction 1"},
		},
		{
			name:             "api_first_then_yaml",
			apiInstructions:  []string{"API first", "API second"},
			yamlInstructions: []string{"YAML first"},
			want:             []string{"API first", "API second", "YAML first"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Merge(tt.apiInstructions, tt.yamlInstructions)
			if len(got) != len(tt.want) {
				t.Errorf("Merge() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Merge()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
