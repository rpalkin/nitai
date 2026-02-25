# Scratch — Raw Notes

## User's scenario from PR perspective
1. Work on some feature in feature branch
2. Create PR
3. Get a brief summary of changes made in a comment
4. Get inline comments to code that need attention
5. New commits after the initial review should trigger review but the system should understand that initial comments are already there and consider this - keep track of previous comments and don't repeat yourself

## User's scenario from admin perspective
1. Register on admin portal and register organization
2. Organization can have any number of connected providers
3. Add a provider providing required data (api url, token, etc...) and receiving webhook urls and tokens
4. Select repos that should be enabled for review
5. Has an ability to enter custom instructions and rules for these instructions to be enabled (filter repos, filetypes, etc...)
6. Has a log of system action regarding to his repositories

## Technical details about the system
- React TypeScript frontend for admin console with Vite for dev
- Golang (connectrpc/connect-go) backend for API and webhooks
- PostgreSQL as main database
- Temporal for workflow orchestration
- Reviewer (probably Pydantic AI) for making a review with LLM using PR diff and other information available about the repo provided by other tools
- Indexer - for indexing repos with embeddings for semantic search
- Qdrant - for storing indexes
- Search-MCP - for search through the Qdrant (used by reviewer)
- Repo-syncer - for keeping local version of repos in sync with remote for indexer
- Diff-fetcher - fetches the PR diff from git provider

## Workflows

### User adds a provider
- Sync the list of available repos and additional metadata to PostgreSQL

### PR created/updated webhook received
- Fetch required PR details from provider's API
- Check from DB if PR has been processed (compare processed diff hash with current state) and add details to PR context
- Sync the current state of target branch with local storage
- Create or update indexes of target branch in Qdrant. Collections in Qdrant are built per repo per target branch (more often just master/main)
- Run PR review with context of previous runs, PR diff, access to search-mcp, access to reading codebase files directly and custom user's instructions to get a comprehensive detailed review
- Process the review:
  - Write inline comments to the lines they belong to
  - Write summary comment (only first pass)
  - Store review details in database
