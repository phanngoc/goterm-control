---
name: review
description: 'Judge a design or a change on the dimensions that actually break things here — seams, failure, restart, concurrency, reversibility, cost. Use when: reviewing a PR, a plan, or an architecture decision. NOT for: checking whether code compiles or style nits, which tooling already does.'
---

# review

Reading a diff tells you what it does. A review has to answer what it does
**when things go wrong**, which is where everything on this machine has actually
broken.

Work the dimensions below in order. Each one is a question this repo has
answered the hard way at least once.

## 1. Is the seam in the right place?

Count who goes through it. A hook on one caller of seven forwards what you
tested and misses the rest.

Ask the layer question too: the storage layer runs in processes that have no
bot, no gateway, no network. A dependency pointed the wrong way does not fail at
compile time — it fails in the one process nobody ran it in.

## 2. What happens when it dies half-way?

Every long operation has a middle. Name what is on disk, in the database, and in
the process at that moment.

- a row saying `running` with no process behind it
- a lease held by something that no longer exists
- a file written to the point where it is valid but wrong

Startup is the one moment cleaning up is unambiguously safe: the process has
just begun, so anything still open under its own name is from an instance that
no longer exists.

## 3. Does a restart cost anything it should not?

Deploys happen during work. A restart that consumes a retry, an attempt, or a
budget turns routine maintenance into damage that accumulates — three deploys
and the work is dead having failed at nothing.

## 4. What if two of them run at once?

Three gateways share one database. Ask whether two can pick up the same row, and
whether the loser's side effects have already left.

A compare-and-set that settles the *write* does not help when the *send* went
out first. The fix is usually to scope the work so the race cannot form, not to
guard it after it can.

## 5. Can it be undone?

Archive over delete, by default. A row made by a bug still holds real work
somebody did — hiding it costs nothing and keeps it.

Where undo is impossible, say so in the confirmation the person reads, not in a
comment.

## 6. Does the guard match the surface?

A rule that demands behaviour X while the tool surface forbids the means of
doing X does not make the system safer. It makes it stop working, quietly, and
the logs fill with refusals nobody reads.

Check both directions: what the rule requires, and what the caller can actually
reach.

## 7. What does the silent default do?

`else` branches that end in a specific backend, a specific agent, a specific
account. When the condition drifts, the fallback keeps working and reports the
wrong thing — an agent moved to one backend quietly spending another's quota.

Prefer a loud failure to a plausible default.

## 8. What does it cost per turn?

Anything added to a system prompt is paid on every turn, forever, by every
agent. Anything read per turn is paid per turn.

Ask what it costs when there are fifty of them, not three.

## 9. Does it work on all three backends?

claude, codex and opencode. A mechanism built on one CLI's flag is a mechanism
two thirds of the agents do not have. Say which of the three you checked.

## 10. Will the migration apply to the database that already exists?

A fresh database and an upgraded one take different paths. An index on a column
that an `ALTER` adds cannot be created in the same statement list as the table —
on an upgrade it runs before the column exists and the gateway will not start.

## 11. Is the test real?

Run it and read the output. A command that prints success unconditionally —
`go test ./... ; echo ok`, or a grep whose exit code is discarded — reports a
green suite over a red one, and it will do it every time.

After any signature change, run the whole suite, not the package you edited.
Callers in other packages, especially tests, are where a changed signature lands.

## How to report it

Lead with what is wrong and what it costs. Order by consequence, not by file
position.

Say which dimensions you checked and which you could not — a review that lists
four findings and does not say it never looked at concurrency reads as if
concurrency were fine.

Separate "this is broken" from "I would have done it differently". The second is
worth saying once and not worth blocking on.
