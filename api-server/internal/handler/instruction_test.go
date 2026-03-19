package handler

import (
	"testing"

	"ai-reviewer/api-server/internal/db"
)

func TestMatchInstruction(t *testing.T) {
	tests := []struct {
		name         string
		instruction  db.InstructionRow
		repoID       string
		changedFiles []string
		wantMatch    bool
	}{
		{
			name: "no filters matches all",
			instruction: db.InstructionRow{
				RepoFilter:        []string{},
				FilePatternFilter: []string{},
			},
			repoID:       "repo-1",
			changedFiles: []string{"README.md"},
			wantMatch:    true,
		},
		{
			name: "repo filter match",
			instruction: db.InstructionRow{
				RepoFilter:        []string{"repo-1", "repo-2"},
				FilePatternFilter: []string{},
			},
			repoID:       "repo-1",
			changedFiles: []string{"any.go"},
			wantMatch:    true,
		},
		{
			name: "repo filter mismatch",
			instruction: db.InstructionRow{
				RepoFilter:        []string{"repo-2"},
				FilePatternFilter: []string{},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go"},
			wantMatch:    false,
		},
		{
			name: "file pattern match",
			instruction: db.InstructionRow{
				RepoFilter:        []string{},
				FilePatternFilter: []string{"*.go"},
			},
			repoID:       "any-repo",
			changedFiles: []string{"main.go", "README.md"},
			wantMatch:    true,
		},
		{
			name: "file pattern mismatch",
			instruction: db.InstructionRow{
				RepoFilter:        []string{},
				FilePatternFilter: []string{"*.go"},
			},
			repoID:       "any-repo",
			changedFiles: []string{"README.md", "Makefile"},
			wantMatch:    false,
		},
		{
			name: "combined filters both match",
			instruction: db.InstructionRow{
				RepoFilter:        []string{"repo-1"},
				FilePatternFilter: []string{"*.go"},
			},
			repoID:       "repo-1",
			changedFiles: []string{"main.go", "README.md"},
			wantMatch:    true,
		},
		{
			name: "combined filters repo mismatch",
			instruction: db.InstructionRow{
				RepoFilter:        []string{"repo-1"},
				FilePatternFilter: []string{"*.go"},
			},
			repoID:       "repo-2",
			changedFiles: []string{"main.go"},
			wantMatch:    false,
		},
		{
			name: "combined filters file mismatch",
			instruction: db.InstructionRow{
				RepoFilter:        []string{"repo-1"},
				FilePatternFilter: []string{"*.go"},
			},
			repoID:       "repo-1",
			changedFiles: []string{"README.md"},
			wantMatch:    false,
		},
		{
			name: "multiple file patterns any match",
			instruction: db.InstructionRow{
				RepoFilter:        []string{},
				FilePatternFilter: []string{"*.go", "*.ts"},
			},
			repoID:       "any-repo",
			changedFiles: []string{"index.ts"},
			wantMatch:    true,
		},
		{
			name: "file pattern matches any file",
			instruction: db.InstructionRow{
				RepoFilter:        []string{},
				FilePatternFilter: []string{"src/*"},
			},
			repoID:       "any-repo",
			changedFiles: []string{"src/main.go", "other.go"},
			wantMatch:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchInstruction(tt.instruction, tt.repoID, tt.changedFiles)
			if got != tt.wantMatch {
				t.Errorf("matchInstruction() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}
