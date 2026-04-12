---
allowed-tools: Bash(gh issue create*), Bash(gh issue list*), Bash(gh issue view*), Bash(gh label*), Bash(gh project*), Bash(gh api*), Bash(gh repo view*), Bash(cd *), Bash(git *), Agent, Read, Glob, Grep
description: "Analyze codebase, create implementation plan, create issue, move to In Progress"
---

# /create-issue — Analyze → Plan → Create Issue → In Progress

You receive a short feature/task description. You analyze the codebase to understand how it should be implemented, write a detailed implementation plan, create a GitHub issue with that plan, and move it to "In Progress" on the project board.

## Usage

```
/create-issue <description>
```

**Examples**:
- `/create-issue "implement memory sqlite"`
- `/create-issue "add rate limiting to gateway"`
- `/create-issue "refactor streamer to support multiple output formats"`

## Arguments: $ARGUMENTS

## Phase 1: Detect Repo

```bash
gh repo view --json nameWithOwner -q '.nameWithOwner'
```
Store as `TARGET_REPO`. Default project board: `https://github.com/users/phanngoc/projects/2` (project number `2`).

## Phase 2: Analyze Codebase

Based on `$ARGUMENTS`, explore the codebase to understand:

1. **What exists** — find related files, functions, patterns using Glob/Grep/Read
2. **What's missing** — identify gaps the feature would fill
3. **How it fits** — understand the architecture and where changes belong
4. **Dependencies** — what packages/modules are affected
5. **Risks** — what could break, edge cases

Use Agent(subagent_type="Explore") for broad exploration. Use Grep/Glob for targeted searches.

Gather enough context to write a concrete, file-level implementation plan — not vague recommendations.

## Phase 3: Write Implementation Plan

From the analysis, create a step-by-step plan with:

- **Specific files** to create or modify (with paths)
- **Specific functions/structs** to add or change
- **Code patterns** to follow (reference existing code)
- **Migration/data** changes if applicable
- **Test strategy**

The plan should be detailed enough that a developer (or `/implement-issue`) can execute it without further research.

## Phase 4: Create Issue

Create the issue directly (no duplicate check needed — the user knows what they want):
```bash
gh issue create -R TARGET_REPO \
  --title "<imperative title>" \
  --body "$(cat <<'ISSUE_EOF'
## Goal
<1-2 sentences: what this achieves>

## Analysis

### Current State
<what exists in the codebase relevant to this feature>

### Architecture Fit
<where this belongs in the codebase, which packages are affected>

## Implementation Plan

### Step 1: <title>
**Files**: `path/to/file.go`
**Changes**: <what to add/modify>
<code snippets if helpful>

### Step 2: <title>
**Files**: `path/to/file.go`
**Changes**: <what to add/modify>

...

### Step N: Verification
**Tests**: <what to test>
**Build**: `go build ./cmd/bomclaw/`

## Acceptance Criteria
- [ ] Criterion 1
- [ ] Criterion 2
- [ ] `go build` passes
- [ ] Manual verification: <how to test>

## Files Affected
| File | Change |
|------|--------|
| `path/to/file.go` | Add/Modify/Create |

## References
- Related code: `path/to/relevant.go`
ISSUE_EOF
)" \
  --label "<labels>"
```

Labels: pick from `enhancement`, `bug`, `refactor`, `architecture`, `performance`, `documentation`.
Create missing labels with `gh label create -R TARGET_REPO` if needed.

Store the created issue number as `ISSUE_NUMBER`.

## Phase 5: Move to In Progress on Project Board

Move the issue to "In Progress" on project board #2:

```bash
# 1. Get project ID
PROJECT_ID=$(gh project list --owner phanngoc --format json | jq -r '.projects[] | select(.number == 2) | .id')

# 2. Add issue to project (if not already there)
ITEM_ID=$(gh project item-add $PROJECT_ID --owner phanngoc --url "https://github.com/TARGET_REPO/issues/ISSUE_NUMBER" --format json | jq -r '.id')

# 3. Get Status field ID and "In Progress" option ID
STATUS_FIELD=$(gh project field-list $PROJECT_ID --owner phanngoc --format json | jq -r '.fields[] | select(.name == "Status")')
FIELD_ID=$(echo $STATUS_FIELD | jq -r '.id')
IN_PROGRESS_ID=$(echo $STATUS_FIELD | jq -r '.options[] | select(.name == "In Progress") | .id')

# 4. Set status to In Progress
gh project item-edit --project-id $PROJECT_ID --id $ITEM_ID --field-id $FIELD_ID --single-select-option-id $IN_PROGRESS_ID
```

If the `gh` token lacks project scopes, show:
> Run: `! gh auth refresh -h github.com -s read:project,project`
Then skip this phase and continue.

## Phase 6: Report

Show:
- Issue URL
- Issue title
- Number of implementation steps
- Project board status
- Suggest: `Run /implement-issue #<number> to start coding`

## Rules

- ALWAYS analyze the codebase BEFORE writing the plan — don't guess
- ALWAYS reference specific file paths and function names in the plan
- ALWAYS use `gh issue create -R <TARGET_REPO>`
- Write in the same language as the user (Vietnamese if user writes Vietnamese)
- The plan must be concrete enough for `/implement-issue` to execute
- Do NOT ask for confirmation — analyze, plan, create, move. Report at the end.
