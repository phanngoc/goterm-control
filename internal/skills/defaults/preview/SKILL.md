---
name: preview
description: 'Make a project runnable from the dashboard''s Preview button (DEV) and publish it to Cloudflare (PROD: bomclaw env). Use when: a project has anything to look at or click, when you create a project, change how it runs, or deploy it. NOT for: a library or a script with nothing to show — say so in bomclaw.json instead of inventing a page.'
---

# preview

The person reading a project should not need a terminal to see it. The project
editor has a **Preview** button: it reads `bomclaw.json` at the project root,
starts what that file says, and shows the running thing in a panel next to the
code — live, clickable, reloading as you change files. Your job is to make that
button work, and keep it working as the project changes.

If there is no `bomclaw.json`, the button says the project declares no preview.
That is the state to never leave a project with something to show in.

## The commands

```
bomclaw preview show  ch_<name>    # what bomclaw.json says, and what the files suggest
bomclaw preview init  ch_<name>    # write it: guessed (Vite, Next, wrangler, index.html) or from flags
bomclaw preview init  ch_<name> --command "…$HOST…$PORT…" --setup "npm install" --cwd web --base keep
bomclaw preview init  ch_<name> --static board
bomclaw preview check ch_<name>    # run it as the button will; PASS or FAIL with the server's output
```

`init` refuses to replace a declared preview without `--force`, and runs
`check` right after writing. A guess is a starting point: if `check` fails,
fix the file (or pass flags) and run `check` again until it says PASS.

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

```
bomclaw preview check ch_<name>
```

It runs `setup` and `command` in a login shell with the same `$PORT`, `$HOST`
and `$BASE_PATH` the dashboard sets, waits for the port, fetches the page the
way the proxy will (under `$BASE_PATH` for `keep`, at `/` for `strip`), and
fails on a 4xx/5xx or on `src`/`href` values that would escape the base
(`/assets/app.js` with `strip`). It stops the server when done. Only say the
preview works after it prints PASS, and paste the PASS line in your reply.

It cannot see what JavaScript fetches at runtime: `fetch('/api/x')` in a
bundle still breaks under `strip`. Keep API calls relative too.

## Keep it true

- Create `bomclaw.json` when the project first has something to look at, not at the end. `bomclaw ch new` reminds you; the panel's "Nhờ agent thiết lập" button sends you here.
- When you change how it runs (new framework, moved folder, new port flag), change `bomclaw.json` in the same piece of work.
- Mention it in the project's `AGENTS.md` ("Preview: `bomclaw.json` runs the Vite app in `web/`") so the next agent does not guess.
- Nothing to show — a CLI, a library, a data pipeline? Then say so and point the preview at the output a person would look at, e.g. `{"preview": {"static": "reports"}}` with an `index.html` listing the latest report. A pipeline that produces a board has a board to preview.

## DEV and PROD — publishing to Cloudflare

Every project room has two environments, the way Lovable has an editor and a
published site:

| | DEV | PROD |
|---|---|---|
| where | this machine | Cloudflare |
| site | the dashboard's **Preview** (behind the login) | a Worker serving the built files: `https://<name>.<account>.workers.dev`, plus a custom domain on a Cloudflare zone (e.g. `phim.bomclaw.org`) |
| data | a local SQLite file | a **D1** database (SQLite, hosted) |
| changes | live as you edit | only when someone **deploys** |

Both are declared in the project's `bomclaw.json` — `preview` for DEV,
`deploy` for PROD. The dashboard shows them side by side (button **DEV · PROD**
in the room's header), and you see the same from the shell.

### Check

```bash
bomclaw env status <channel>          # DEV and PROD side by side
bomclaw env status <channel> --json   # the same, for a script
```

Report what it says, with the URL. "Deployed" without a URL you opened is not
a report.

### Declare PROD (once)

```bash
bomclaw env init <channel> --static dist --build "npm run build" \
  [--domain <sub>.bomclaw.org] [--spa] [--database data/app.db]
```

- `--static` is the BUILT folder (`dist`, `build`, `out`), not the source.
  A plain HTML project is `--static .` or its folder.
- `--spa` for client-side routers (React Router, Vue Router): unknown paths
  serve `index.html`.
- `--domain` must be on a zone in the Cloudflare account. Optional — every
  Worker also answers on workers.dev.
- `--database` is the local SQLite file that becomes the PROD database (D1).
- `--main worker.js` adds a Worker script in front of the files (an API).
  Only a script can read the database (`env.DB`); a files-only site cannot.

It writes `bomclaw.json` → `"deploy": {…}`; edit that file directly to change it.
`"target"` picks the provider (`--provider` on init):

- `cloudflare` (default) — static sites and Worker scripts, D1 for SQLite data.
- `fly` — Fly.io Machines: `--static dist` is served by a generated nginx
  image; `--dockerfile Dockerfile --port 3000` deploys an app that runs a
  server (Node, Python, Go…), which Cloudflare cannot. Answers on
  `<name>.fly.dev`; machines sleep when idle. No database in this provider
  yet (Managed Postgres planned) — `--with-db` is refused.

Check `bomclaw env status` rather than assuming which one a project uses.

### Publish

```bash
bomclaw env deploy <channel> --dry-run     # build + show the wrangler config; sends nothing
bomclaw env deploy <channel>               # build + publish the site
bomclaw env deploy <channel> --with-db     # ALSO replace the PROD database with the local file
```

Rules:

- **Always dry-run first** when the spec is new or changed, and read the config.
- **`--with-db` replaces every table in PROD** with the local copy. Never use it
  on your own initiative once the site has real users or data you did not
  create; ask the owner, in the room, naming what will be overwritten.
  A site redeploy without it never touches the data.
- After a deploy, open the URL (`curl -sI <url>` at least) before saying it is live.
- Deploys are the owner's public face. Do not deploy to a custom domain the
  owner did not ask for.

### When it fails

- `Cloudflare is not connected` / `Fly.io is not connected` — no credentials yet.
  Ask the owner for them, or point them at the dashboard (Admin → Kết nối).

### Credentials handed to you in chat

When the owner pastes a token, store it — never anywhere else:

```bash
bomclaw secrets set cloudflare --stdin <<'END'
CLOUDFLARE_API_TOKEN=<the token>
CLOUDFLARE_ACCOUNT_ID=<the account id>
END
```

It checks the token against the service and prints a mask, never the value.
Without an account id, find it with the token: `GET /client/v4/accounts`.
Fly is `bomclaw secrets set fly --stdin` with `FLY_API_TOKEN=…`; `bomclaw
secrets list` shows what is connected. Never write a token into a project
file, a commit, a room message or your reply — say "stored, check OK" and
what the check found.
- `… is empty — nothing to publish` — the build did not produce the static
  folder, or `static` points at the wrong place.
- A custom domain error — the hostname is not on a zone in this account, or
  already has a DNS record pointing elsewhere.
- A database "Statement too long" — one INSERT is huge; split the data.

### Not yet

Supabase (Postgres) is the planned next database engine
(`"database": {"engine": "supabase"}` is reserved and refused for now). Until
it exists, PROD data is D1.
