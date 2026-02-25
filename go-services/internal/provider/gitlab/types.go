package gitlab

// gitlabProject maps a project item from GET /api/v4/projects.
type gitlabProject struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
}

// gitlabMR maps the response from GET /api/v4/projects/:id/merge_requests/:iid.
type gitlabMR struct {
	Title        string `json:"title"`
	Description  string `json:"description"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
	Draft        bool   `json:"draft"`
}

// gitlabMRChanges maps the response from GET /api/v4/projects/:id/merge_requests/:iid/changes.
type gitlabMRChanges struct {
	Changes []gitlabDiffChange `json:"changes"`
}

// gitlabDiffChange is a single file entry within the changes response.
type gitlabDiffChange struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	Diff        string `json:"diff"`
	NewFile     bool   `json:"new_file"`
	DeletedFile bool   `json:"deleted_file"`
	RenamedFile bool   `json:"renamed_file"`
}

// gitlabNote maps the response from POST /api/v4/projects/:id/merge_requests/:iid/notes.
type gitlabNote struct {
	ID int `json:"id"`
}

// gitlabDiscussion maps the response from POST /api/v4/projects/:id/merge_requests/:iid/discussions.
type gitlabDiscussion struct {
	ID string `json:"id"`
}

// gitlabMRVersion maps an item from GET /api/v4/projects/:id/merge_requests/:iid/versions.
type gitlabMRVersion struct {
	ID       int    `json:"id"`
	HeadSHA  string `json:"head_commit_sha"`
	BaseSHA  string `json:"base_commit_sha"`
	StartSHA string `json:"start_commit_sha"`
}
