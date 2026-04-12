---
allowed-tools: Bash(gh issue view*), Bash(gh issue list*), Bash(gh issue comment*), Bash(gh pr create*), Bash(gh project*), Bash(gh api*), Bash(git *), Bash(go *), Bash(npm *), Bash(pgrep *), Bash(pkill *), Bash(launchctl *), Bash(curl *), Bash(sleep *), Bash(tail *), Agent, Read, Write, Edit, Glob, Grep
description: "Read issue, analyze plan, implement code, create PR linked to the issue"
---

# /implement-issue — Read Issue → Implement → Create PR

You receive a GitHub issue number. You read the issue (which contains an implementation plan from `/create-issue`), implement all steps, and create a PR that closes the issue.

## Usage

```
/implement-issue <issue-number>
```

**Examples**:
- `/implement-issue 27`
- `/implement-issue #27`

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
gh issue view <number> -R TARGET_REPO --json title,body,labels,comments
```

Extract from the issue body:
1. **Goal** — what this issue achieves
2. **Implementation Plan** — the numbered steps with file paths and changes
3. **Acceptance Criteria** — how to verify
4. **Files Affected** — the list of files to create/modify

If the issue has no implementation plan, analyze the issue description and the codebase yourself to create one before proceeding.

## Phase 2: Setup Branch

```bash
git fetch origin
git checkout main && git pull origin main
git checkout -b issue-<number>-<short-slug>
```

Branch naming: `issue-<number>-<slug>` where slug is 2-3 words from the title in kebab-case.

## Phase 3: Implement

For each step in the implementation plan:

1. **Read** the relevant files first — understand existing code before changing
2. **Implement** the change — follow existing patterns, naming conventions, imports
3. **Build** — verify after each step:
   ```bash
   go build ./cmd/bomclaw/
   ```
   If build fails, fix before moving on.
4. **Commit** — one atomic commit per logical step:
   ```bash
   git add <specific-files>
   git commit -m "$(cat <<'EOF'
   <type>: <short description>

   <what changed and why>

   Refs #<number>
   EOF
   )"
   ```

Commit types: `feat`, `fix`, `refactor`, `docs`, `chore`, `perf`, `test`

**Rules during implementation**:
- Follow existing code patterns exactly (naming, error handling, imports)
- Read before write — never edit a file you haven't read
- No TODO comments — implement completely or skip the step
- No placeholder/mock code — everything must work
- If a step is unclear, use Grep/Glob/Agent to explore before implementing

## Phase 4: Run Tests

After all steps are implemented:

```bash
go build ./cmd/bomclaw/
go vet ./...
go test ./internal/... -count=1
```

Fix any failures before proceeding.

## Phase 5: Push & Create PR

```bash
git push -u origin issue-<number>-<short-slug>
```

Create PR linked to the issue:

```bash
gh pr create -R TARGET_REPO \
  --title "<type>: <short description from issue title>" \
  --body "$(cat <<'PR_EOF'
## Summary
<2-3 bullet points: what was implemented>

## Changes
| File | Change |
|------|--------|
| `path/to/file.go` | <what changed> |

## Implementation Steps
- [x] Step 1: ...
- [x] Step 2: ...
- [x] Step N: ...

## Verification
- [ ] `go build ./cmd/bomclaw/` passes
- [ ] `go vet ./...` passes
- [ ] `go test ./internal/...` passes
- [ ] <manual verification from acceptance criteria>

Closes #<number>

🤖 Generated with [Claude Code](https://claude.com/claude-code)
PR_EOF
)"
```

Use `Closes #<number>` so merging the PR auto-closes the issue.

## Phase 6: Report

Show:
- ✅ PR URL
- Branch name
- Commits list (hash + message)
- Implementation steps completed
- Any remaining work or known issues
- Suggest: merge the PR to close issue #<number>

## Rules

- ALWAYS read the issue first — don't re-plan from scratch
- ALWAYS create a feature branch from latest main
- ALWAYS reference the issue in commits (`Refs #<number>`)
- ALWAYS atomic commits — one per logical step
- ALWAYS verify build after each step
- ALWAYS use `Closes #<number>` in the PR body
- Follow existing code patterns (check similar files before writing)
- Do NOT ask for confirmation — read, implement, push, create PR. Report at the end.
- Write PR body in the same language as the issue content
