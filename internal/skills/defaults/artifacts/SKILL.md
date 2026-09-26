---
name: artifacts
description: 'File what a task produced so it can be found later. Use when: a run makes a report, a patch, a page, a file, or a running URL. NOT for: a one-line answer that is already the task result.'
---

# artifacts

`bomclaw artifact` attaches the output of a task to that task.

## Why a path in prose is not enough

A path written into a sentence is findable for about a day. The next run has a
different working directory, the person reading is looking at a board and not a
terminal, and nothing in a sentence survives the file being moved.

This has happened here: a whole 3D viewer was handed over as a localhost URL in
a paragraph, and the task's own output panel was empty.

An artifact is found by id, appears beside the task on the board, and can be
handed to a child task as an input.

## Commands

```
bomclaw artifact put --task <id> --title "<what it is>" --file <path>
bomclaw artifact put --task <id> --title "<what it is>" --kind link --url <url>
bomclaw artifact list --task <id>
bomclaw artifact get <artifact-id>
```

`--kind link` is for output that is a URL rather than a file — a running page,
a dashboard, a PR. That is exactly the case that gets written into prose
instead, so reach for it deliberately.

## What to file, and what not to

File the thing someone would open: the report, the patch, the page, the data.

Do not file your own progress notes — `bomclaw task progress` is for those —
and do not file a summary that is already the task's result. An index full of
restated results is an index nobody reads.

Title it as what it IS, not where it lives. "Đà Nẵng parcels, 412 rows" tells a
reader something; "output.json" does not.
