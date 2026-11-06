---
name: projects
description: 'Open rooms and tasks when the person asks, so the work shows up in Messages and on the Tasks board. Use when: the person says "make a room/project/channel", "track this", "open a task/goal", gives a multi-step goal (incl. a `/goal ...` line), or asks to archive/rename/bind a room. NOT for: a quick question answered in this turn.'
---

# projects

The person steers you from Telegram and expects to manage rooms and tasks just
by asking. A chat turn by itself leaves no trace in Messages or on the Tasks
board. Only rooms (`bomclaw ch`) and tasks (`bomclaw task`) show up there. If you
answer a big request only in chat, the work cannot be seen from the dashboard.
That has happened here: a whole Shopee tool was built and shipped in a DM, and
the dashboard showed none of it.

When the person asks for a room, a project, a task or a goal, do it with the
commands below. Do not tell them to use the dashboard instead.

## Rooms

```
bomclaw ch new <name> --purpose "<one line>"    # project room + folder ~/goterm-projects/<name>
bomclaw ch new <name> --no-workspace            # a room with no folder (like #general)
bomclaw ch new <name> --members bomclaw2        # add peers now (you and the owner are always in)
bomclaw ch list [--all]                         # existing rooms: check before creating a duplicate
bomclaw ch post <channel> "<text>"              # say something in the room
bomclaw ch bind <channel> --mode all|mentions   # make the room also speak to Telegram
bomclaw ch archive <channel>                    # hide it; nothing in it is lost
```

`ch new` prints the channel id (`ch_<name>`), the folder, and the brief file
(`AGENTS.md`). The brief is what every later run in that project reads first.
Write the goal, the decisions made so far, and where the existing files are.

If the project will have anything a person can look at (a site, a board, an
app, a report page), give it a Preview as part of setting it up: follow the
`preview` skill, `bomclaw preview init ch_<name>`, and stop at PASS from
`bomclaw preview check ch_<name>`. An empty folder has nothing to detect yet;
then put "preview check passes" in the first task's acceptance instead.
When the code lives somewhere else, the Preview runs from a copy in the project
folder (a clone, or the built files), never from a production checkout.

A project's code lives in GitHub repos, and the room links them: one
**primary** ("the repo" when nobody says which) and any number of others (a
repo per game, a shared library, a production checkout).

```
bomclaw ch repo list    ch_<name>
bomclaw ch repo add     ch_<name> <owner/name|url> [--primary] [--clone] [--path web] [--label "…"]
bomclaw ch repo create  ch_<name> <name> [--public]     # new repo, cloned into the project folder
bomclaw ch repo primary ch_<name> <owner/name>
bomclaw ch repo remove  ch_<name> <owner/name>          # unlink only
```

Every agent in the room is told the list, so link a repo the moment the
project has one — when you create it, clone it, or find the project's code in
one — rather than leaving it in a line of AGENTS.md. The first link is the
primary. A checkout that must not be edited (production) is linked with
`--path` to it and a `--label` saying so.

Name rooms in short lowercase kebab-case (`shopee-deals`). Run `ch list --all`
first; reuse a room that already covers the work.

## Tasks and goals

```
bomclaw task new --title "<short>" --body "<self-contained>" \
    --channel ch_<name> --acceptance "<what makes this done>" --to <you>
bomclaw task list [--state S] [--mine]
bomclaw task show --id <id>      # runs, checkpoint, history
bomclaw task tree --id <id>      # a goal: what is left, what it cost
bomclaw task cancel --id <id>    # only when the person says stop
bomclaw task close --id <id> --reason "…"   # a task that should not run: duplicate, already done, plan changed
```

- `--channel` puts the task on that project's board. Without it, the task
  belongs to no project.
- `--acceptance` turns a task into a goal: the bar it is judged against. When
  the person gives one ("xong khi…", "done when…"), copy it. When they don't,
  write a concrete, checkable one and tell them what you wrote.
- `--to <you>` (your own id, `$BOMCLAW_AGENT_ID`) keeps the work with the
  agent that has the context. Leave `--to` out only when the person wants any
  agent to take it, and use `--to <peer>` when they name a peer.
- The body must stand alone. The run that picks it up does not see this chat,
  so put the context in the body and in the room's brief.

## A task runs in the background, not in this chat

Once created, a task is claimed by a task runner. The runner works in the
project folder and reports on the board. So after you open a task, stop doing
that work in this chat, or it gets done twice. Reply with:

- the room id and the task id,
- the acceptance you set,
- where to watch it: Admin → Messages → the room, and Admin → Tasks.

When the person only wants it recorded ("track this", "để lên board") and you
are already partway through in chat, open the room, write the brief with what
is already done and where it lives, and open the task for what remains. Do not
move files that a running site or job depends on without asking.

## A service that keeps running goes in pm2

A project's long-running process (a site, an API, a bot, a studio) runs under
pm2, never as `nohup …&` or a one-off terminal: a hand-started server dies with
the next reboot or crash and nobody restarts it, and nobody can see it either.

- One ecosystem file per project: `~/.goterm-shared/pm2/<project>.config.cjs`,
  every app with `namespace: '<project>'`, `cwd` = the project folder and its
  own `out_file`/`error_file`. Run the program itself: `script` = absolute
  node/python3, `args` = its arguments, `interpreter: 'none'`. pm2's wrapper
  require()s the script otherwise, and an ESM/.ts server then never listens
  while pm2 reports it `online`. Copy a working file there as the template.
- `pm2 start ~/.goterm-shared/pm2/<project>.config.cjs && pm2 save`; after a
  code change, `pm2 restart <app>`.
- `bomclaw infra ls` shows every pm2 app by project, every LaunchAgent and every
  listening port, and flags servers nothing manages. Check it before starting a
  port; if a stray server of your project holds it, move it into pm2 rather
  than starting a second copy.
- The room's Preview button (`bomclaw preview`) is separate: the gateway starts
  and stops that server itself. Do not also put it in pm2.

## Mapping requests to commands

| The person says | You do |
|---|---|
| "tạo phòng/dự án X", "make a project for X" | `ch new x --purpose …` → write the brief → link/create its repo → preview (if it has something to show) → reply with the id |
| "link repo …", "kết nối GitHub", "tạo repo cho dự án" | `ch repo add ch_x owner/name [--primary]` or `ch repo create ch_x <name>` |
| "làm preview", "chạy thử dự án trên dashboard" | `preview` skill: `preview init` → `preview check` until PASS |
| "/goal …", "mục tiêu là …", a multi-step build | room (reuse or new) + `task new --channel … --acceptance …` |
| "track this", "đưa việc này lên board" | room + brief of what is done + task for what is left |
| "giao cho agent 2" | `task new … --to bomclaw2` |
| "báo vào Telegram" for a room | `ch bind <channel> --mode all` |
| "dừng/huỷ task …" | `task cancel --id …` |
| you find a duplicate / already-done / obsolete task | `task close --id … --reason "duplicate of t_…"`, then say so in the thread |
| "đóng phòng X" | `ch archive ch_x` |

Confirm before archiving a room or cancelling a task the person did not name
exactly. Closing is different: a task that duplicates one already done, or that
a change of plan made pointless, you close yourself with the reason — do not
ask the person to run a command for it. You cannot close a task another agent
is running; message that agent instead. Everything else on this page is cheap to undo, so just do it.
