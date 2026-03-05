# E2E Test Cases

All planned end-to-end test cases for ai-reviewer. Cases 1–13 were designed by the planner based on codebase analysis. Cases A–O were identified by a product review of user-facing scenarios.

Tests marked `[SKIP]` cover features not yet implemented and are included as executable specs — the test code exists with `t.Skip(...)` and should pass once the feature is built.

Tests marked `[DONE]` have test code written in `e2e/e2e_test.go` and all pass (or are intentionally skipped).

---

## Cases 1–13 (Planner)

### Test 1 — Full pipeline via TriggerReview ✅ green [DONE]
**Purpose:** Verify the complete happy path triggered via the admin API.

**Pre-conditions:**
- Full stack running (postgres, restate, api-server, worker, reviewer)
- Mock GitLab server running, configured to return:
  - `GET /api/v4/projects` → project list with one repo
  - `GET /api/v4/projects/:id/merge_requests/:iid` → MR details (not draft, not merged)
  - `GET /api/v4/projects/:id/merge_requests/:iid/changes` → small diff
  - `GET /api/v4/projects/:id/merge_requests/:iid/versions` → version list
  - `POST /api/v4/projects/:id/merge_requests/:iid/notes` → 200 OK (records call)
  - `POST /api/v4/projects/:id/merge_requests/:iid/discussions` → 200 OK (records call)
- Mock LLM server running, returning a valid `ReviewResponse` via tool calling format

**Steps:**
1. `ProviderService/CreateProvider` — register mock GitLab URL + token
2. `RepoService/ListRepos` — list repos for the provider
3. `RepoService/EnableReview` — enable review on the repo
4. `ReviewService/TriggerReview` — trigger review for MR IID=1
5. Poll `ReviewService/GetReviewRun` every 2s (timeout: 60s mock / 300s live) until status ≠ pending/running

**Assertions:**
- `GetReviewRun` returns status = COMPLETED
- `review_comments` table has ≥ 1 row with the expected `body` content
- Mock GitLab received exactly 1 POST to `/notes` (summary)
- Mock GitLab received ≥ 1 POST to `/discussions` (inline comments)
- Inline comment position: `new_path` matches expected file, `new_line` matches expected line

**Not asserted in live mode:** exact comment body (LLM output is non-deterministic)

**Flakiness mitigations:** Restate debounce not an issue for TriggerReview (force=true). Polling timeout is generous.

---

### Test 2 — Full pipeline via Webhook ✅ green [DONE]
**Purpose:** Verify the complete happy path triggered by a simulated GitLab webhook.

**Pre-conditions:** Same as Test 1 (provider registered, repo review enabled, mocks configured).

**Webhook payload:**
```json
{
  "object_kind": "merge_request",
  "project": { "id": 1 },
  "object_attributes": {
    "iid": 1,
    "action": "open",
    "draft": false,
    "work_in_progress": false,
    "state": "opened"
  }
}
```

**Steps:**
1. Register provider + enable review (same as Test 1 steps 1–3)
2. `POST /webhooks/{provider_id}` with `X-Gitlab-Token` header and payload above
3. Query DB directly to find the created `review_runs` row (webhook doesn't return run ID)
4. Poll `GetReviewRun` until terminal status

**Assertions:** Same as Test 1 plus:
- Webhook returns HTTP 200
- `review_runs` row created in DB with `mr_iid=1`, `repo_id` matching the enabled repo

**Not asserted:** order of comment posting

---

### Test 3 — Invalid webhook secret → 401 🔴 red [DONE]
**Purpose:** Verify webhook authentication rejects bad secrets.

**Steps:**
1. Register provider + enable review
2. `POST /webhooks/{provider_id}` with wrong/missing `X-Gitlab-Token`

**Assertions:**
- HTTP response is 401
- No `review_runs` row created in DB
- Mock LLM received 0 calls

---

### Test 4 — Unknown repo in webhook → no run created 🔴 red [DONE]
**Purpose:** Verify webhook for an unregistered repo is accepted but produces no review.

**Steps:**
1. Register provider (no repos enabled)
2. `POST /webhooks/{provider_id}` with valid token but `project.id` not in DB

**Assertions:**
- HTTP 200 (webhook accepted)
- No `review_runs` row created
- Mock LLM received 0 calls

---

### Test 5 — Draft MR → run created with draft status, no LLM call 🔴 red [DONE]
**Purpose:** Verify draft MRs are acknowledged but not reviewed.

**Webhook payload:** `"draft": true, "work_in_progress": true`

**Steps:**
1. Register provider + enable review
2. Send webhook with draft MR

**Assertions:**
- HTTP 200
- `review_runs` row created with `status = 'draft'` (checked via direct DB query)
- Mock LLM received 0 calls
- Mock GitLab received 0 POSTs to `/notes` or `/discussions`

---

### Test 6 — Draft → Ready transition ✅ green [DONE]
**Purpose:** Verify that unmarking a draft MR triggers a full review.

**Steps:**
1. Register provider + enable review
2. Send webhook with `draft: true` → run created, no LLM call (as Test 5)
3. Send second webhook with `changes.draft.previous: true, changes.draft.current: false`
4. Poll `GetReviewRun` for the second run until COMPLETED

**Assertions:**
- Two `review_runs` rows: first with status=draft, second with status=completed
- Comments posted on second run
- Mock LLM called exactly once (for the second run only)

---

### Test 7 — Review disabled repo → no run created 🔴 red [DONE]
**Purpose:** Verify webhooks for repos with review disabled produce no run.

**Steps:**
1. Register provider + enable review + disable review on the repo
2. Send valid non-draft webhook

**Assertions:**
- HTTP 200
- No new `review_runs` row created
- Mock LLM received 0 calls

---

### Test 8 — Large diff short-circuit ✅ green [DONE]
**Purpose:** Verify diffs >5000 lines skip LLM and post a canned message.

**Pre-conditions:** Mock GitLab returns a dynamically generated 5001-line diff (built in test setup via `strings.Builder`).

**Steps:**
1. Register provider + enable review
2. `TriggerReview` for the large-diff MR
3. Poll until COMPLETED

**Assertions:**
- Status = COMPLETED
- Mock LLM received 0 calls
- Mock GitLab received 1 POST to `/notes` with body containing "too large" or equivalent canned message

---

### Test 9 — Duplicate diff dedup 🔴 red [DONE]
**Purpose:** Verify the same diff hash sent twice produces only one review.

**Note:** This test is slow — Restate debounce timer (3 min) may apply on second invocation.

**Steps:**
1. Register provider + enable review
2. Send webhook → first review runs to COMPLETED
3. Send identical webhook (same MR, same diff hash) immediately
4. Wait for debounce + any second invocation to settle

**Assertions:**
- Only 1 `review_runs` row with status=completed for this MR
- Mock LLM called exactly once total

---

### Test 10 — Provider deletion cascade ✅ green [DONE]
**Purpose:** Verify deleting a provider soft-deletes repos and disables future reviews.

**Steps:**
1. Register provider + enable review
2. `ProviderService/DeleteProvider`
3. Send webhook for a repo under the deleted provider

**Assertions:**
- Provider row marked as deleted in DB
- Associated repo rows marked as deleted/disabled
- Webhook returns 200 but no new `review_runs` row created

---

### Test 11 — LLM terminal error → review FAILED 🔴 red [DONE]
**Purpose:** Verify LLM HTTP 400 errors result in a failed review run, not a hang.

**Pre-conditions:** Mock LLM configured to return HTTP 400 for any request matching this test's diff content.

**Steps:**
1. Register provider + enable review
2. `TriggerReview`
3. Poll until terminal status

**Assertions:**
- `review_runs` status = FAILED
- Error message recorded in DB
- Mock GitLab received 0 POSTs to `/notes` or `/discussions`

---

### Test 12 — GitLab 404 for MR → review FAILED 🔴 red [DONE]
**Purpose:** Verify GitLab returning 404 on MR fetch results in a failed review.

**Pre-conditions:** Mock GitLab configured to return 404 for `GET /merge_requests/:iid`.

**Steps:**
1. Register provider + enable review
2. `TriggerReview`
3. Poll until terminal status

**Assertions:**
- `review_runs` status = FAILED
- Error recorded in DB
- Mock LLM received 0 calls

---

### Test 13 — Semantic search: wrong function call detected [SKIP] ⚠️ spec [DONE]
**Purpose:** Verify the reviewer finds a function definition via semantic search when it's not in the diff, and posts a comment about the argument mismatch.

**Status:** `t.Skip("semantic search not yet integrated into reviewer pipeline")` — this test is the executable spec for the feature.

**Pre-conditions:**
- Test repo at `e2e/testdata/semantic-repo/` indexed into Qdrant before the test runs
- `semantic-repo/pkg/mathutil/mathutil.go` contains:
  ```go
  func Foo(x int, y int) string { return fmt.Sprintf("%d+%d", x, y) }
  ```
- Diff (`e2e/testdata/semantic_diff.patch`) modifies `cmd/main.go` to call `mathutil.Foo(x)` — only 1 arg, missing second
- Diff does NOT include `mathutil.go`
- Mock LLM configured to return an inline comment on `cmd/main.go:10` with body: `"mathutil.Foo requires 2 arguments (x int, y int) but is called with only 1"`

**Key assertion (unique to this test):** The request sent to the LLM must contain the `Foo` definition in its context — proving the reviewer fetched it from Qdrant, not from the diff.

**Steps:**
1. Index semantic-repo into Qdrant
2. Register provider + enable review
3. `TriggerReview` with the semantic diff MR
4. Poll until COMPLETED

**Assertions:**
- Status = COMPLETED
- Inline comment on `cmd/main.go:10` with body matching "mathutil.Foo requires 2 arguments"
- LLM request payload contains the `Foo` function definition (from Qdrant context)

---

## Cases A–O (Product Review Gaps)

### Test A — Disable review → subsequent webhook ignored ✅/🔴 lifecycle
**Purpose:** Tests the review toggle lifecycle end-to-end (not just initial state).

**Steps:**
1. Register provider + enable review
2. Send webhook → review completes (green)
3. `RepoService/DisableReview`
4. Send second webhook for a new MR push

**Assertions:**
- First review: status = COMPLETED, comments posted
- After disable: second webhook returns 200 but no new `review_runs` row created
- Mock LLM called exactly once total

---

### Test B — Re-review on new push (same MR, different diff) ✅ green
**Purpose:** Verify a new push to an existing MR triggers a new review. The primary developer workflow.

**Steps:**
1. Register provider + enable review
2. Send webhook for MR IID=1 with diff v1 → first review COMPLETED
3. Send webhook for MR IID=1 with diff v2 (different diff hash)
4. Poll for second review run

**Assertions:**
- Two `review_runs` rows for MR IID=1 with different diff hashes
- Both have status = COMPLETED
- Second run has its own comments reflecting diff v2
- Mock LLM called exactly twice

---

### Test C — Cancel-on-new-push: in-flight review superseded ✅ green
**Purpose:** Verify that a new push while a review is in-flight cancels the first and runs only the second.

**Steps:**
1. Register provider + enable review
2. Send webhook for push v1 — review starts (in debounce or running)
3. Immediately send webhook for push v2 (different diff hash)
4. Wait for all invocations to settle

**Assertions:**
- Only one `review_runs` row reaches status = COMPLETED (for push v2)
- The push v1 run is either CANCELLED or absent
- Mock LLM called exactly once (for push v2 only)
- No duplicate comments on the MR

---

### Test D — Concurrent reviews on different MRs (same repo) ✅ green
**Purpose:** Verify two MRs on the same repo are reviewed independently without interference.

**Steps:**
1. Register provider + enable review
2. Send webhooks for MR IID=1 and MR IID=2 near-simultaneously
3. Poll both runs until COMPLETED

**Assertions:**
- Two separate `review_runs` rows, both status = COMPLETED
- Comments for MR 1 are not posted to MR 2 and vice versa
- Mock GitLab received discussions POSTed to correct MR endpoints
- Mock LLM called twice

---

### Test E — Multiple providers / multiple repos: no cross-contamination ✅ green
**Purpose:** Verify reviews from different providers stay isolated.

**Steps:**
1. Register Provider A and Provider B (both pointing to mock GitLab, different tokens)
2. Enable review on Repo A (under Provider A) and Repo B (under Provider B)
3. Send webhook to Provider A's webhook URL for MR on Repo A
4. Send webhook to Provider B's webhook URL for MR on Repo B
5. Poll both runs

**Assertions:**
- Two `review_runs` rows, each linked to the correct provider and repo
- Comments for Repo A posted to mock GitLab using Provider A's token
- Comments for Repo B posted using Provider B's token
- No cross-posting

---

### Test F — Review produces zero inline comments (clean diff) ✅ green
**Purpose:** Verify a clean diff (no issues found) completes successfully without hanging.

**Pre-conditions:** Mock LLM returns `ReviewResponse` with non-empty summary but empty `comments: []`.

**Steps:**
1. Register provider + enable review
2. `TriggerReview`
3. Poll until terminal status

**Assertions:**
- Status = COMPLETED
- Mock GitLab received exactly 1 POST to `/notes` (summary posted)
- Mock GitLab received 0 POSTs to `/discussions`
- `review_comments` table has 0 rows for this run

---

### Test G — Invalid/expired token at review time 🔴 red
**Purpose:** Verify graceful failure when provider token is invalid at the time of review.

**Pre-conditions:** Mock GitLab configured to return HTTP 401 for authenticated API calls.

**Steps:**
1. Register provider (token accepted during registration)
2. Enable review
3. Reconfigure mock GitLab to return 401 for DiffFetcher calls
4. `TriggerReview`
5. Poll until terminal status

**Assertions:**
- Status = FAILED
- Error message in DB references authentication failure (not an opaque 500)
- Mock LLM received 0 calls

---

### Test H — Webhook for closed/merged MR is ignored 🔴 red
**Purpose:** Verify `close` and `merge` webhook actions produce no review run.

**Webhook payloads:**
```json
{ "object_attributes": { "action": "close", ... } }
{ "object_attributes": { "action": "merge", ... } }
```

**Assertions for each:**
- HTTP 200
- No `review_runs` row created
- Mock LLM received 0 calls

---

### Test I — Debounce collapses rapid pushes (timing test) ✅ green
**Purpose:** Verify two rapid webhooks for the same MR (different diff hashes) produce only one review.

**Note:** Distinct from Test 9 (same diff hash). This tests the debounce timer collapsing rapid sequential pushes, not hash-based dedup.

**Steps:**
1. Register provider + enable review
2. Send webhook for push v1
3. Within 500ms, send webhook for push v2 (different diff hash)
4. Wait for debounce window to expire + review to complete

**Assertions:**
- Exactly one `review_runs` row reaches status = COMPLETED
- Mock LLM called exactly once
- The completed review reflects push v2's diff (the later one)

---

### Test J — RepoSyncer clone failure (unreachable repo) 🔴 red
**Purpose:** Verify graceful FAILED status when the repo can't be cloned.

**Pre-conditions:** Mock GitLab configured to return errors for repo clone/fetch operations (or repo URL points to a non-existent host).

**Steps:**
1. Register provider + enable review (repo marked as requiring sync)
2. `TriggerReview`
3. Poll until terminal status

**Assertions:**
- Status = FAILED
- Error recorded in DB (not a silent hang)

---

### Test K — GetReviewRun API returns complete data ✅ green
**Purpose:** Validate the `GetReviewRun` API contract returns all fields needed for display.

**Steps:**
1. Run Test 1 (full happy path)
2. Call `ReviewService/GetReviewRun` with the completed run ID

**Assertions:**
- Response includes `status = COMPLETED`
- `summary` field is non-empty
- `comments` array is non-empty, each item has: `file_path`, `line_start`, `line_end`, `body`
- `created_at`, `updated_at` timestamps present

---

### Test L — Idempotent comment posting (retry safety) ✅ green
**Purpose:** Verify re-running PostReview does not post duplicate comments.

**Steps:**
1. Complete a full review (Test 1)
2. Note the `provider_comment_id` values in `review_comments` DB rows
3. Simulate a PostReview retry by re-triggering the same run (or directly invoking PostReview via Restate)
4. Check mock GitLab

**Assertions:**
- Mock GitLab received the same number of POSTs as the first run (no new POSTs)
- `review_comments` table has no new rows

---

### Test M — Malformed JSON webhook body → no 500 🔴 red
**Purpose:** Verify garbage webhook body is handled gracefully.

**Steps:**
1. Register provider
2. `POST /webhooks/{provider_id}` with valid `X-Gitlab-Token` but body = `"not valid json{{"`

**Assertions:**
- Response is 400 or 200 (not 500)
- No `review_runs` row created

---

### Test N — Large number of inline comments ✅ green
**Purpose:** Verify 50+ inline comments are all posted without rate-limit or timeout failures.

**Pre-conditions:** Mock LLM returns `ReviewResponse` with 50 inline comments across different files and lines.

**Steps:**
1. Register provider + enable review
2. `TriggerReview`
3. Poll until terminal status

**Assertions:**
- Status = COMPLETED
- Mock GitLab received exactly 50 POSTs to `/discussions`
- All 50 comments recorded in `review_comments` table

---

### Test O — Provider creation populates repo list ✅ green
**Purpose:** Verify that after creating a provider the repo list is fetched from GitLab and stored.

**Pre-conditions:** Mock GitLab `GET /api/v4/projects` returns 3 repos.

**Steps:**
1. `ProviderService/CreateProvider`
2. `RepoService/ListRepos` for the new provider

**Assertions:**
- `ListRepos` returns exactly 3 repos
- Repos match the mock GitLab project list (name, remote ID)
- Each repo has `review_enabled = false` initially

---

## Summary

| Range | Count | Type |
|---|---|---|
| 1–13 | 13 | Original planner cases |
| A–O | 15 | Product review gaps |
| **Total** | **28** | |

| Status | Count |
|---|---|
| ✅ Green (happy path) | 16 |
| 🔴 Red (error/negative) | 11 |
| ⚠️ Spec / skip-marked | 1 (Test 13) |

| Implementation | Count |
|---|---|
| [DONE] Implemented (passing) | 12 (Tests 1–12) |
| [DONE] Implemented (t.Skip) | 1 (Test 13) |
| Not yet implemented | 15 (Tests A–O) |
