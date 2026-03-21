package localmerge

import (
	"fmt"
	"os/exec"
	"strings"
)

type Result struct {
	MergeSHA     string
	HasConflicts bool
}

func CreateMergeCommit(repoPath, targetSHA, sourceSHA string) (Result, error) {
	mergeTree := exec.Command("git", "--git-dir", repoPath,
		"merge-tree", "--write-tree", targetSHA, sourceSHA)
	treeOut, err := mergeTree.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Exit code 1 with stdout = conflicts (tree SHA is still output).
			// Exit code 1 without stdout = invalid ref / not a merge-able object.
			stdout := strings.TrimSpace(string(treeOut))
			if stdout == "" {
				stderr := string(exitErr.Stderr)
				return Result{}, fmt.Errorf("git merge-tree: %s", strings.TrimSpace(stderr))
			}
			return Result{HasConflicts: true}, nil
		}
		return Result{}, fmt.Errorf("git merge-tree: %w", err)
	}
	treeSHA := strings.TrimSpace(string(treeOut))

	commitTree := exec.Command("git", "--git-dir", repoPath,
		"commit-tree", treeSHA,
		"-p", targetSHA, "-p", sourceSHA,
		"-m", "local merge for review")
	commitTree.Env = append(commitTree.Environ(),
		"GIT_AUTHOR_NAME=ai-reviewer",
		"GIT_AUTHOR_EMAIL=noreply@ai-reviewer",
		"GIT_COMMITTER_NAME=ai-reviewer",
		"GIT_COMMITTER_EMAIL=noreply@ai-reviewer",
	)
	commitOut, err := commitTree.Output()
	if err != nil {
		return Result{}, fmt.Errorf("git commit-tree: %w", err)
	}

	return Result{
		MergeSHA: strings.TrimSpace(string(commitOut)),
	}, nil
}
