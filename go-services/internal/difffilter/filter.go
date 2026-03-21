package difffilter

import (
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type FilterResult struct {
	Diff         string
	ChangedFiles []string
	ChangedLines int
}

func FilterDiff(unifiedDiff string, changedFiles []string, ignoreGlobs []string) FilterResult {
	if len(ignoreGlobs) == 0 {
		return FilterResult{
			Diff:         unifiedDiff,
			ChangedFiles: changedFiles,
			ChangedLines: countChangedLines(unifiedDiff),
		}
	}

	isIgnored := func(path string) bool {
		for _, glob := range ignoreGlobs {
			if matched, _ := doublestar.Match(glob, path); matched {
				return true
			}
		}
		return false
	}

	filteredFiles := make([]string, 0, len(changedFiles))
	for _, f := range changedFiles {
		if !isIgnored(f) {
			filteredFiles = append(filteredFiles, f)
		}
	}

	if len(filteredFiles) == 0 {
		return FilterResult{
			Diff:         "",
			ChangedFiles: nil,
			ChangedLines: 0,
		}
	}

	sections := splitDiffSections(unifiedDiff)
	filteredSections := make([]string, 0, len(sections))
	for _, section := range sections {
		filePath := extractFilePathFromSection(section)
		if filePath != "" && !isIgnored(filePath) {
			filteredSections = append(filteredSections, section)
		}
	}

	filteredDiff := strings.Join(filteredSections, "")

	return FilterResult{
		Diff:         filteredDiff,
		ChangedFiles: filteredFiles,
		ChangedLines: countChangedLines(filteredDiff),
	}
}

func splitDiffSections(unifiedDiff string) []string {
	if unifiedDiff == "" {
		return nil
	}

	var sections []string
	var currentSection strings.Builder
	lines := strings.Split(unifiedDiff, "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git a/") {
			if currentSection.Len() > 0 {
				sections = append(sections, currentSection.String())
				currentSection.Reset()
			}
		}
		currentSection.WriteString(line)
		currentSection.WriteString("\n")
	}

	if currentSection.Len() > 0 {
		sections = append(sections, currentSection.String())
	}

	return sections
}

func extractFilePathFromSection(section string) string {
	lines := strings.Split(section, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git a/") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				bPart := parts[3]
				if strings.HasPrefix(bPart, "b/") {
					return strings.TrimPrefix(bPart, "b/")
				}
			}
		}
	}
	return ""
}

func countChangedLines(unifiedDiff string) int {
	if unifiedDiff == "" {
		return 0
	}

	count := 0
	lines := strings.Split(unifiedDiff, "\n")
	for _, line := range lines {
		if len(line) > 0 && (line[0] == '+' || line[0] == '-') {
			if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
				continue
			}
			count++
		}
	}
	return count
}
