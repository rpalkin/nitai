package reporules

import (
	"fmt"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"gopkg.in/yaml.v3"
)

type ReviewRules struct {
	Instructions  []string
	IgnoreGlobs   []string
	RulesModified bool
}

type reviewRulesFile struct {
	Instructions []string `yaml:"instructions"`
	Ignore       []string `yaml:"ignore"`
}

const rulesFileName = ".review-rules.yaml"

func ReadRepoRules(repoPath string, headSHA string, changedFiles []string) (ReviewRules, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return ReviewRules{}, fmt.Errorf("opening repo: %w", err)
	}

	commit, err := repo.CommitObject(plumbing.NewHash(headSHA))
	if err != nil {
		return ReviewRules{}, nil
	}

	tree, err := commit.Tree()
	if err != nil {
		return ReviewRules{}, nil
	}

	file, err := tree.File(rulesFileName)
	if err != nil {
		return ReviewRules{}, nil
	}

	content, err := file.Contents()
	if err != nil {
		return ReviewRules{}, fmt.Errorf("reading %s: %w", rulesFileName, err)
	}

	var rules reviewRulesFile
	if err := yaml.Unmarshal([]byte(content), &rules); err != nil {
		return ReviewRules{}, fmt.Errorf("parsing %s: %w", rulesFileName, err)
	}

	modified := false
	for _, f := range changedFiles {
		if f == rulesFileName {
			modified = true
			break
		}
	}

	return ReviewRules{
		Instructions:  rules.Instructions,
		IgnoreGlobs:   rules.Ignore,
		RulesModified: modified,
	}, nil
}
