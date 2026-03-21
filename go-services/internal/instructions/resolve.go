package instructions

import (
	"path/filepath"

	"ai-reviewer/go-services/internal/db"
)

// Resolve returns the content strings of instructions that match the given
// repoID and changedFiles. Empty repo_filter means "all repos"; empty
// file_pattern_filter means "all files". Both filters use AND logic.
func Resolve(rows []db.InstructionRow, repoID string, changedFiles []string) []string {
	var result []string
	for _, row := range rows {
		if matchInstruction(row, repoID, changedFiles) {
			result = append(result, row.Content)
		}
	}
	return result
}

// matchInstruction checks if an instruction matches the given repo and changed files.
// It mirrors the logic in api-server/internal/handler/instruction.go:matchInstruction.
func matchInstruction(inst db.InstructionRow, repoID string, changedFiles []string) bool {
	// Check repo filter (empty = match all)
	if len(inst.RepoFilter) > 0 {
		found := false
		for _, id := range inst.RepoFilter {
			if id == repoID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	// Check file pattern filter (empty = match all)
	if len(inst.FilePatternFilter) > 0 {
		found := false
		for _, pattern := range inst.FilePatternFilter {
			for _, file := range changedFiles {
				if matched, err := filepath.Match(pattern, file); err == nil && matched {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Merge combines API-sourced instructions with YAML-sourced instructions.
// API instructions come first (org-level), then YAML instructions (repo-level).
// Returns nil if both inputs are empty.
func Merge(apiInstructions, yamlInstructions []string) []string {
	if len(apiInstructions) == 0 && len(yamlInstructions) == 0 {
		return nil
	}
	result := make([]string, 0, len(apiInstructions)+len(yamlInstructions))
	result = append(result, apiInstructions...)
	result = append(result, yamlInstructions...)
	return result
}
