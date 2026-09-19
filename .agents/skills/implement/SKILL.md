---
name: implement
description: 'Build what an issue describes, from branch to deployed and verified. Use when: handed an issue number to implement. NOT for: deciding WHETHER to build it — that is the plan skill — or for a change with no issue behind it.'
---

# implement

The issue says what. This says how to get it onto the running machine without
breaking it.

## The procedure

### 1. Read the issue and the code it names

Both. An issue written before the code was read is wrong somewhere, and the
place it is wrong is usually the seam. If the plan says to hook something, count
the callers before trusting the count in the issue.

If the issue turns out to be wrong, say so in the PR and build the right thing.
Do not build the wrong thing because it was written down.

### 2. Branch

```
git checkout -q main && git pull -q --ff-only
git checkout -b <type>/<short-description>
```

Never commit to main.

### 3. Build it, smallest correct piece first

Match the surrounding code: its comment density, its naming, its idioms. A file
where your function reads differently from the ones above it is a file somebody
will have to reconcile later.

### 4. Test, and read the output

```
go test ./... 2>&1 | grep -E "FAIL|panic"
go vet ./...
```

**Never chain `echo "ok"` after a test command.** It runs whether the suite
passed or failed, and it will report green over red every single time. Read the
real output, or grep it and read the grep.

After any signature change, run the **whole** suite. Callers in other packages —
especially test files — are exactly where a changed signature lands, and
`go build` will not find them.

For a schema change, run the migration against a **copy of the production
database**, not only a fresh one. Fresh and upgraded take different paths:

```
cp ~/.goterm-shared/data/coord.db /tmp/mig.db
./bomclaw <some-command> --db /tmp/mig.db
```

An index on a column added by `ALTER` belongs with the other post-ALTER
statements, never in the table list — on an upgrade it runs before the column
exists and the gateway will not start.

### 5. Dashboard, if you touched it

```
cd dashboard && npx tsc --noEmit && npm run build
```

### 6. Commit, PR, merge

The commit message says **why**, and what would have gone wrong otherwise. The
diff already says what.

### 7. Deploy

Follow `.claude/commands/deploy.md`. The step that is not optional:

**Verify the signature before restarting.** `codesign` can hang on a keychain
dialog; a timeout kills it and leaves the binary with the linker's ad-hoc
signature, and launchd then kills the process at exec with no useful message.

```
codesign -dv ~/.bomclaw/bomclaw 2>&1 | grep Identifier
```

Must print `Identifier=com.bomclaw.gateway`. If it prints anything else, do not
restart.

Restart all three labels — one binary is shared, so a deploy restarts every
agent.

### 8. Verify on the running system

Health on all three ports, and then **the thing you actually built**. A green
health check only says the process started.

Where the change touches live data, check the live data: the column exists, the
row was written, the schedule fired.

## Pitfalls

**A deploy kills whatever is mid-run.** Check for in-flight work before
restarting, and know that the work comes back — if it does not, that is the bug
to fix before deploying again.

**Do not delete to tidy.** Archive, hide, or mark. A row made by a bug still
holds work somebody did.

**Refuse rather than sanitise.** Silently reinterpreting what a caller asked for
produces something they cannot find again. An error they can read is better.

**Write through a temp file and a rename** for anything an agent may be reading
right now. A half-written file of instructions is instructions it will act on.

## When it does not work

Report it with the output. A step skipped is said plainly. Finished and verified
is said plainly too — without hedging.
