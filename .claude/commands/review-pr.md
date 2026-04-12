---
allowed-tools: Bash(gh pr view*), Bash(gh pr diff*), Bash(gh pr checks*), Bash(gh api*), Bash(gh repo view*), Bash(gh pr review*), Bash(git *), Agent, Read, Glob, Grep
description: "Fetch PR, review architecture/best-practices/security, comment on PR"
---

# /review-pr — Fetch PR → Review → Comment

You receive a PR number. You fetch the PR diff, review it for architecture, best practices, and security issues, then post a structured review comment on the PR.

## Usage

```
/review-pr <pr-number>
```

**Examples**:
- `/review-pr 36`
- `/review-pr #36`

## Arguments: $ARGUMENTS

## Phase 1: Fetch PR

Parse `$ARGUMENTS` — extract the PR number (strip `#` if present).

Detect repo:
```bash
gh repo view --json nameWithOwner -q '.nameWithOwner'
```
Store as `TARGET_REPO`.

Fetch PR metadata and diff:
```bash
gh pr view <number> -R TARGET_REPO --json title,body,files,baseRefName,headRefName,additions,deletions,commits
gh pr diff <number> -R TARGET_REPO
```

Also fetch the list of changed files:
```bash
gh pr view <number> -R TARGET_REPO --json files -q '.files[].path'
```

## Phase 2: Understand Context

For each changed file in the PR:
1. **Read the full file** (not just the diff) to understand the surrounding code
2. **Read related files** — imports, callers, tests — to understand the impact
3. If the PR references an issue, fetch it: `gh issue view <issue-number> -R TARGET_REPO --json title,body`

Use Agent(subagent_type="Explore") if you need broader codebase understanding.

## Phase 3: Review

Analyze the diff across three dimensions. For each dimension, assign a rating (pass/warn/fail) and list specific findings with file:line references.

### 3a: Architecture Review
- Does the change fit the existing codebase architecture?
- Are responsibilities correctly separated (single responsibility)?
- Are new abstractions justified or premature?
- Does it introduce coupling between unrelated packages?
- Are interfaces used appropriately?
- Is the change in the right layer (handler vs service vs model)?

### 3b: Best Practices Review
- Error handling: are errors wrapped with context? Silently swallowed?
- Naming: do new names follow existing conventions?
- DRY: is there duplicated logic that should be shared?
- Tests: are new code paths covered? Are edge cases tested?
- Concurrency: are shared resources protected? Race conditions?
- Resource management: are resources (goroutines, files, connections) properly cleaned up?
- API design: are function signatures clear? Do they accept the narrowest interface?

### 3c: Security Review
- Input validation: is user input validated/sanitized?
- Injection: SQL injection, command injection, path traversal?
- Secrets: are credentials, tokens, or keys exposed?
- Auth: are authorization checks in place where needed?
- Data exposure: does logging or error messages leak sensitive data?
- Dependencies: are new dependencies from trusted sources?

## Phase 4: Comment on PR

Post a review comment using `gh pr review`. Choose the review type based on severity:

- **APPROVE** — if no warn/fail findings, or only minor style suggestions
- **COMMENT** — if there are warnings but nothing blocking
- **REQUEST_CHANGES** — if there are fail-level issues that must be fixed

Format the review body in this structure:

```bash
gh pr review <number> -R TARGET_REPO --<type> --body "$(cat <<'REVIEW_EOF'
## Code Review

### Architecture <rating>
<findings with file:line references, or "Looks good" if clean>

### Best Practices <rating>
<findings with file:line references, or "Looks good" if clean>

### Security <rating>
<findings with file:line references, or "No issues found" if clean>

### Summary
<1-3 sentence overall assessment>

<optional: specific suggestions as a numbered list>

---
🤖 Reviewed by [Claude Code](https://claude.com/claude-code)
REVIEW_EOF
)"
```

Rating symbols: `✅` pass, `⚠️` warn, `❌` fail

## Phase 5: Report

Show to the user:
- PR title and number of changed files
- Overall verdict (approve/comment/request changes)
- Key findings summary (if any)
- Confirm the review was posted

## Rules

- ALWAYS read the full changed files, not just the diff — context matters
- ALWAYS reference specific file:line when citing issues
- Be honest — do NOT rubber-stamp. If there are real problems, say so.
- Be constructive — explain WHY something is a problem and suggest a fix
- Prioritize: security > correctness > architecture > style
- Do NOT nitpick formatting or style unless it's inconsistent with the codebase
- Write review comments in the same language the PR is written in (Vietnamese if PR is Vietnamese)
- Do NOT ask for confirmation — fetch, review, comment. Report at the end.
