---
name: preview
description: 'Make a project runnable from the dashboard''s Preview button, like Lovable or a VS Code dev server. Use when: a project has anything to look at or click (a board, a web app, an API with a page, a report site), when you create a project, or when you change how it runs. NOT for: a library or a script with nothing to show — say so in bomclaw.json instead of inventing a page.'
---

# preview

The person reading a project should not need a terminal to see it. The project
editor has a **Preview** button: it reads `bomclaw.json` at the project root,
starts what that file says, and shows the running thing in a panel next to the
code — live, clickable, reloading as you change files. Your job is to make that
button work, and keep it working as the project changes.

If there is no `bomclaw.json`, the button says the project declares no preview.
That is the state to never leave a project with something to show in.

## The file

`bomclaw.json`, at the project root (the folder the room's Files button opens):

```json
{
  "preview": {
    "command": "npm run dev -- --host $HOST --port $PORT --strictPort --base $BASE_PATH",
    "setup": "npm install",
    "cwd": "web",
    "base": "keep"
  }
}
```

| field     | meaning |
|-----------|---------|
| `command` | Starts a server that **listens on `$HOST:$PORT`** and keeps running. Required unless `static`. |
| `setup`   | Runs before `command` on every start. Keep it idempotent and quick when nothing changed (`npm install`, `pip install -r requirements.txt`). |
| `cwd`     | Where both run, relative to the project root. Default: the root. |
| `base`    | `"keep"` (default): requests arrive with the `$BASE_PATH` prefix, for servers told their base. `"strip"`: the prefix is removed first, for servers that only know `/`. |
| `env`     | Extra variables, `{"NAME": "value"}`. Never secrets — this file is read by everyone in the room. |
| `static`  | A folder to serve as-is instead of running anything, e.g. `"board"`. The page must use **relative** links (`data/latest.json`, not `/data/latest.json`). |

The dashboard sets, for `setup` and `command`:

- `PORT` — a free port chosen at each start. **Never hard-code a port.**
- `HOST` — `127.0.0.1`. Bind there, not `0.0.0.0`: the preview is reached through the dashboard's proxy, and nothing else should reach your dev server.
- `BASE_PATH` — `/preview/<channel>/`, the address the app is served under.
- `BOMCLAW_PREVIEW=1`, `BOMCLAW_PROJECT=<channel>`, `BROWSER=none`.

It runs in a login shell in `cwd`, so `node`, `python3`, `uv`… are on PATH as
in your own terminal.

## The one thing that breaks previews: the base path

The app is served at `https://bot.bomclaw.org/preview/<channel>/`, not at `/`.
Anything the page asks for with an absolute path (`/assets/app.js`,
`fetch('/api/data')`, `ws://…/`) goes to the dashboard instead of your app and
404s. Either tell the server its base (`"base": "keep"`), or make every URL
relative and use `"base": "strip"`.

Recipes that work:

| stack | command | base |
|-------|---------|------|
| **Vite** (React/Vue/Svelte) | `npm run dev -- --host $HOST --port $PORT --strictPort --base $BASE_PATH` | keep |
| **Next.js** | `next.config.js`: `basePath: process.env.BASE_PATH?.replace(/\/$/, '') \|\| ''`, then `npx next dev -H $HOST -p $PORT` | keep |
| **Astro** | `astro.config`: `base: process.env.BASE_PATH`, then `npx astro dev --host $HOST --port $PORT` | keep |
| **Static folder** (HTML/JS/JSON) | `"static": "board"` — no command at all | — |
| **Static with live reload** | `npx live-server --host=$HOST --port=$PORT --no-browser` | strip |
| **Python http.server** | `python3 -m http.server $PORT --bind $HOST` | strip |
| **FastAPI / Flask** | `uvicorn app:app --host $HOST --port $PORT --reload --root-path ${BASE_PATH%/}` (FastAPI) · Flask: `flask run -h $HOST -p $PORT` with relative URLs | keep · strip |
| **Streamlit** | `streamlit run app.py --server.address $HOST --server.port $PORT --server.baseUrlPath $BASE_PATH --server.headless true` | keep |

Vite's hot reload works through the proxy with `--base $BASE_PATH`; nothing
else to configure. With `strip`, in-app links must not start with `/`.

## Check it before you say it works

Run exactly what the button will run, with the variables it will set, and
fetch the page through the base path:

```
cd <project>/<cwd>
export PORT=5199 HOST=127.0.0.1 BASE_PATH=/preview/<channel>/
<setup> && <command> &        # the command from bomclaw.json
sleep 5
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:5199/preview/<channel>/   # base: keep
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:5199/                     # base: strip
kill %1
```

200 — and the HTML's `<script src>` / `<link href>` either start with
`$BASE_PATH` or are relative. A page whose assets start with `/assets/` will
load blank in the preview even though the curl above said 200.

## Keep it true

- Create `bomclaw.json` when the project first has something to look at, not at the end.
- When you change how it runs (new framework, moved folder, new port flag), change `bomclaw.json` in the same piece of work.
- Mention it in the project's `AGENTS.md` ("Preview: `bomclaw.json` runs the Vite app in `web/`") so the next agent does not guess.
- Nothing to show — a CLI, a library, a data pipeline? Then say so and point the preview at the output a person would look at, e.g. `{"preview": {"static": "reports"}}` with an `index.html` listing the latest report. A pipeline that produces a board has a board to preview.
