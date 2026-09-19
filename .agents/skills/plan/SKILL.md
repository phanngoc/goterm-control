---
name: plan
description: 'Turn an idea into a GitHub issue somebody else could build from. Use when: asked to plan, design, or scope something before writing code. NOT for: a change small enough to just make, or a bug with an obvious one-line fix.'
---

# plan

An issue nobody can build from is a note. This is how to write one that somebody
— another agent, or you in three weeks — can pick up and finish.

## The procedure

### 1. Read the code before writing a line of the plan

Not after. A plan written from the idea alone is wrong in the places nobody can
check, and it reads exactly as confident as a correct one.

Find the seam the change actually lands on, and count the callers:

```
grep -rn "TheFunction(" internal cmd | grep -v _test
```

A hook placed where only one of seven writers goes through it will forward the
one line you tested and silently miss the six that matter. That is the specific
way a plan looks right and is not.

### 2. Ask only what changes the work

If two readings lead to the same build, pick one and say which. If they lead to
different builds, ask — before writing the plan, not after it is merged.

### 3. Write the issue in five parts

**Bối cảnh** — what exists today and why it is a problem. Name the file and the
line. "Nothing records X" is a claim; `internal/foo/bar.go:88 never calls Y` is
a fact.

**Trước / sau** — a table. Left column today, right column after. If a row reads
the same on both sides, cut it: it is not part of this change.

**Sequence** — a mermaid diagram. Draw the failure branch, not only the happy
path. A sequence with no `alt` is usually a sequence that has not been thought
through.

**Kế hoạch** — files and functions, in the order they must be done. Say what is
deliberately NOT in scope, because that is the half people argue about later.

**Nghiệm thu** — checkboxes that can be verified with one command or one click.
"Works correctly" cannot. "`bomclaw skills list` prints the rules" can.

### 4. Open it

```
gh issue create --title "..." --label enhancement --body-file <path>
```

Write the body to a file first. A long `--body` on the command line breaks on
quoting and fails with an error that does not mention quoting.

The board adds it as **Todo** on its own — do not run `gh project item-edit`.
Verify instead:

```
gh issue view <n> --json projectItems -q '.projectItems[].title'
```

### 5. Stop

Planning is not implementing. Hand back the issue number and let somebody decide
whether to build it.

## Pitfalls

**State the risk, then finish the plan.** A concern that stops the work is a
concern nobody can act on. Write it into the issue and keep going.

**A plan that cannot be wrong is a plan that says nothing.** If every line would
be true of any change, delete them and write the three that are specific to this
one.

**Impact belongs in the issue, not in your reply.** The reply is read once; the
issue is read by whoever builds it.
