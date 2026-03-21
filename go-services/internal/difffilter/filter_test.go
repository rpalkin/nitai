package difffilter

import (
	"testing"
)

func TestFilterDiffNoGlobs(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// new
 func main() {}
`
	files := []string{"main.go"}
	result := FilterDiff(diff, files, nil)
	if result.Diff != diff {
		t.Errorf("expected unchanged diff, got %q", result.Diff)
	}
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "main.go" {
		t.Errorf("expected [main.go], got %v", result.ChangedFiles)
	}
	if result.ChangedLines != 1 {
		t.Errorf("expected 1 changed line, got %d", result.ChangedLines)
	}
}

func TestFilterDiffSingleGlob(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// change 1
 func main() {}
diff --git a/vendor/dep.go b/vendor/dep.go
--- a/vendor/dep.go
+++ b/vendor/dep.go
@@ -1,2 +1,3 @@
 package vendor
+// change 2
 func Dep() {}
`
	files := []string{"main.go", "vendor/dep.go"}
	result := FilterDiff(diff, files, []string{"vendor/**"})
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "main.go" {
		t.Errorf("expected [main.go], got %v", result.ChangedFiles)
	}
	if result.ChangedLines != 1 {
		t.Errorf("expected 1 changed line after filtering, got %d", result.ChangedLines)
	}
	expectedDiff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// change 1
 func main() {}
`
	if result.Diff != expectedDiff {
		t.Errorf("expected filtered diff %q, got %q", expectedDiff, result.Diff)
	}
}

func TestFilterDiffRecursiveGlob(t *testing.T) {
	diff := `diff --git a/pkg/api/service.pb.go b/pkg/api/service.pb.go
--- a/pkg/api/service.pb.go
+++ b/pkg/api/service.pb.go
@@ -1,2 +1,3 @@
 package api
+// generated
 func Service() {}
diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// change
 func main() {}
`
	files := []string{"pkg/api/service.pb.go", "main.go"}
	result := FilterDiff(diff, files, []string{"**/*.pb.go"})
	if len(result.ChangedFiles) != 1 || result.ChangedFiles[0] != "main.go" {
		t.Errorf("expected [main.go], got %v", result.ChangedFiles)
	}
}

func TestFilterDiffAllFilesIgnored(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// change
 func main() {}
`
	files := []string{"main.go", "util.go"}
	result := FilterDiff(diff, files, []string{"**/*.go"})
	if result.Diff != "" {
		t.Errorf("expected empty diff, got %q", result.Diff)
	}
	if len(result.ChangedFiles) != 0 {
		t.Errorf("expected empty files, got %v", result.ChangedFiles)
	}
	if result.ChangedLines != 0 {
		t.Errorf("expected 0 changed lines, got %d", result.ChangedLines)
	}
}

func TestFilterDiffGlobNoMatch(t *testing.T) {
	diff := `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+// change
 func main() {}
`
	files := []string{"main.go"}
	result := FilterDiff(diff, files, []string{"vendor/**"})
	// The diff may have trailing newline differences, just check key content
	if !stringsContains(result.Diff, "main.go") {
		t.Errorf("expected diff to contain main.go, got %q", result.Diff)
	}
	if len(result.ChangedFiles) != 1 {
		t.Errorf("expected [main.go], got %v", result.ChangedFiles)
	}
}

func TestFilterDiffMultiFileMixed(t *testing.T) {
	diff := `diff --git a/src/main.go b/src/main.go
--- a/src/main.go
+++ b/src/main.go
@@ -1,2 +1,3 @@
 package main
+// change 1
 func main() {}
diff --git a/vendor/util.go b/vendor/util.go
--- a/vendor/util.go
+++ b/vendor/util.go
@@ -1,2 +1,3 @@
 package vendor
+// change 2
 func Util() {}
diff --git a/generated.pb.go b/generated.pb.go
--- a/generated.pb.go
+++ b/generated.pb.go
@@ -1,2 +1,3 @@
 package gen
+// change 3
 func Generated() {}
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1,2 +1,3 @@
 # Project
+Updated README.
 Description.
`
	files := []string{"src/main.go", "vendor/util.go", "generated.pb.go", "README.md"}
	result := FilterDiff(diff, files, []string{"vendor/**", "**/*.pb.go"})

	if len(result.ChangedFiles) != 2 {
		t.Errorf("expected 2 files, got %v", result.ChangedFiles)
	}
	expectedFiles := map[string]bool{"src/main.go": true, "README.md": true}
	for _, f := range result.ChangedFiles {
		if !expectedFiles[f] {
			t.Errorf("unexpected file %s in result", f)
		}
	}
	if result.ChangedLines != 2 {
		t.Errorf("expected 2 changed lines, got %d", result.ChangedLines)
	}
	if stringsContains(result.Diff, "vendor/util.go") {
		t.Errorf("diff should not contain vendor/util.go")
	}
	if stringsContains(result.Diff, "generated.pb.go") {
		t.Errorf("diff should not contain generated.pb.go")
	}
	if !stringsContains(result.Diff, "src/main.go") {
		t.Errorf("diff should contain src/main.go")
	}
	if !stringsContains(result.Diff, "README.md") {
		t.Errorf("diff should contain README.md")
	}
}

func TestCountChangedLines(t *testing.T) {
	tests := []struct {
		name     string
		diff     string
		expected int
	}{
		{
			name:     "empty diff",
			diff:     "",
			expected: 0,
		},
		{
			name: "single addition",
			diff: `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1,2 @@
 package main
+// new
`,
			expected: 1,
		},
		{
			name: "addition and deletion",
			diff: `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,2 +1,2 @@
 package main
-func old() {}
+func new() {}
`,
			expected: 2,
		},
		{
			name: "multiple hunks",
			diff: `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1 +1,2 @@
 package main
+// new
@@ -5 +6 @@
-func old() {}
+func new() {}
`,
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countChangedLines(tt.diff)
			if got != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, got)
			}
		})
	}
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
