// Package indexing provides shared types and utilities for repository indexing.
package indexing

import "strings"

// IndexRequest is the payload sent to the Python Indexer service.
type IndexRequest struct {
	RepoID            string  `json:"repo_id"`
	RepoPath          string  `json:"repo_path"`
	Branch            string  `json:"branch"`
	HeadSHA           string  `json:"head_sha"`
	CollectionName    string  `json:"collection_name"`
	LastIndexedCommit *string `json:"last_indexed_commit"`
}

// IndexResult is the response from the Python Indexer service.
type IndexResult struct {
	CollectionName string `json:"collection_name"`
	FilesIndexed   int    `json:"files_indexed"`
	ChunksUpserted int    `json:"chunks_upserted"`
}

// SanitizeCollectionName returns a Qdrant-safe collection name for a repo+branch.
func SanitizeCollectionName(repoID, branch string) string {
	safe := strings.NewReplacer("/", "_", "-", "_", ".", "_").Replace(branch)
	return repoID + "_" + safe
}
