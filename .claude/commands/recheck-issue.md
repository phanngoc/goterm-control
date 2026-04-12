---
allowed-tools: Bash(gh issue view*), Bash(gh issue list*), Bash(gh issue edit*), Bash(gh issue comment*), Bash(gh api*), Bash(gh repo view*), Bash(git *), Bash(go *), Agent, Read, Glob, Grep, AskUserQuestion
description: "Read issue, re-research codebase, compare plan vs reality, report gaps, update if approved"
---

# /recheck-issue — Re-validate Issue Plan Against Current Codebase

You receive a GitHub issue number. You read the issue's implementation plan, re-research the codebase to check if the plan is still accurate, report any gaps or outdated assumptions, and — if the user approves — update the issue with a corrected plan.

## Usage

```
/recheck-issue <issue-number>
```

**Examples**:
- `/recheck-issue 42`
- `/recheck-issue #42`

## Arguments: $ARGUMENTS

## Phase 1: Read Issue

Parse `$ARGUMENTS` — extract the issue number (strip `#` if present).

Detect repo:
```bash
gh repo view --json nameWithOwner -q '.nameWithOwner'
```
Store as `TARGET_REPO`.

Fetch the issue:
```bash
gh issue view <number> -R TARGET_REPO --json title,body,labels,comments,state
```

Extract from the issue body:
1. **Goal** — what this issue is trying to achieve
2. **Implementation Plan** — the numbered steps with file paths and changes
3. **Files Affected** — the list of files to create/modify
4. **Acceptance Criteria** — how to verify

If the issue has no implementation plan, tell the user and stop — nothing to recheck.

## Phase 2: Re-research Codebase

For EVERY file and function mentioned in the plan, verify against the current codebase:

1. **File existence** — do the referenced files still exist? Have they been renamed/moved?
   - Use Glob to check paths
   - Use Grep to search for moved/renamed files if missing

2. **Function/struct existence** — do the referenced symbols still exist?
   - Use Grep to find function/struct definitions
   - Check signatures haven't changed

3. **Pattern validity** — are the code patterns referenced in the plan still used?
   - Read relevant files to verify current patterns
   - Check imports, naming conventions, error handling style

4. **Architecture changes** — has the project structure shifted since the plan was written?
   - Check for new packages, refactored modules, changed interfaces
   - Look at recent commits for relevant changes:
     ```bash
     git log --oneline -20
     ```

5. **Dependency changes** — have relevant dependencies been added/removed/updated?
   - Check go.mod, package.json, or relevant dependency files

Use Agent(subagent_type="Explore") for broad exploration when needed. Use Grep/Glob/Read for targeted checks.

## Phase 3: Gap Analysis

Compare the plan against your findings. For each step in the plan, classify as:

- **Still valid** — code matches plan's assumptions, step can proceed as-is
- **Needs update** — file/function exists but has changed (different signature, moved location, etc.)
- **Obsolete** — referenced code no longer exists or has been replaced
- **Already done** — the change described has already been implemented
- **New dependency** — step requires additional changes not in the original plan
- **Missing step** — something the plan didn't account for that's now needed

## Phase 4: Report to User

Present a clear gap report in this format:

```
## Recheck Report: Issue #<number> — <title>

### Summary
<1-2 sentences: overall plan health>

### Step-by-step Review

#### Step 1: <title>
**Status**: Still valid | Needs update | Obsolete | Already done
**Details**: <what matched, what didn't, what changed>
**Files checked**: `path/to/file.go`

#### Step 2: <title>
...

### Gaps Found
| # | Type | Description | Impact |
|---|------|-------------|--------|
| 1 | <type> | <what's wrong> | <how it affects implementation> |

### Proposed Changes
<If gaps exist, show the corrected plan diff — what would change>
```

Then ask the user for approval using AskUserQuestion:
- **Option 1**: "Update issue" — apply the corrected plan to the issue
- **Option 2**: "Show full new plan" — display the complete updated plan before deciding
- **Option 3**: "No changes needed" — keep the issue as-is

## Phase 5: Update Issue (if approved)

If the user chose "Update issue" (or approved after seeing the full plan):

1. Build the updated issue body preserving the original structure but with corrected:
   - File paths and function names
   - Implementation steps (reordered, added, removed as needed)
   - Acceptance criteria (updated if scope changed)
   - Files Affected table

2. Update the issue:
   ```bash
   gh issue edit <number> -R TARGET_REPO --body "$(cat <<'BODY_EOF'
   <updated issue body>
   BODY_EOF
   )"
   ```

3. Add a comment documenting what changed:
   ```bash
   gh issue comment <number> -R TARGET_REPO --body "$(cat <<'COMMENT_EOF'
   ## Plan Rechecked

   **Date**: <today>
   **Reason**: Codebase has diverged from original plan

   ### Changes Made
   - <bullet list of what was updated in the plan>

   ---
   _Rechecked by Claude Code via `/recheck-issue`_
   COMMENT_EOF
   )"
   ```

## Phase 6: Report

Show:
- Issue URL
- Number of gaps found
- Steps updated / added / removed
- Whether issue was updated or left as-is

## Rules

- ALWAYS read the issue first — understand the existing plan before researching
- ALWAYS verify EVERY file path and symbol mentioned in the plan
- ALWAYS show the gap report BEFORE making any changes
- ALWAYS ask for user approval — never update the issue without explicit consent
- Check recent git history for context on what changed and why
- Write the report and updated plan in the same language as the issue content
- If no gaps are found, say so clearly — don't invent problems
- Do NOT change the issue's title or labels — only the body (plan content)
