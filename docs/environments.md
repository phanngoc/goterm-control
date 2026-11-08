---
title: Environments (DEV / PROD)
layout: default
---

<section class="doc-page">
<div class="doc-content" markdown="1">

# Environments: DEV and PROD

Every project room has two environments:

| | DEV | PROD |
|---|---|---|
| where | this machine | a hosting provider — **Cloudflare** or **Fly.io** |
| site | the dashboard's **Preview** (`/preview/<channel>/`, behind the login) | Cloudflare: a Worker serving the built files on `https://<name>.<account>.workers.dev`. Fly.io: Machines on `https://<name>.fly.dev`, serving the built files through nginx or running the project's own Dockerfile. Both can add a custom domain (`phim.bomclaw.org`). |
| data | a local SQLite file | Cloudflare **D1** (hosted SQLite); Fly Managed Postgres and Supabase planned |
| changes | live as you edit | only when someone **deploys** |

The dashboard shows both in the room's header (**DEV · PROD**). Agents see
the same from their shell with `bomclaw env status <channel>`.

## What we took from Lovable

Lovable separates *building* from *publishing*. The editor shows your work as
you change it. **Publish** deploys a snapshot to `<app>.lovable.app` and is
manual: "later changes are not automatically pushed live", and a dot on the
Publish button means there are unpublished changes. A custom domain is
attached afterwards, and the `lovable.app` address keeps working while it
connects. Lovable's built-in backend and its Supabase integration hold the
data. Their docs do not describe a separate test database, so a schema change
can reach the live app. ([Lovable — Publish](https://docs.lovable.dev/features/deploy))

The same shape here:

- **DEV is the editor.** Preview runs the project's dev server on this
  machine. It changes as agents edit, and only people who can log in see it.
- **PROD is the published snapshot.** It changes only on a deploy, from the
  panel's **Deploy** button or `bomclaw env deploy`.
- **Data is never pushed by accident.** A site deploy leaves the database
  alone. Uploading the local database is a separate, explicit choice
  (`--with-db`, or the checkbox in the panel), because it *replaces* the PROD
  tables. Lovable does not separate these, and it should.

## Declaring PROD

`bomclaw.json` at the project root holds both environments:

```json
{
  "preview": { "command": "npm run dev -- --host $HOST --port $PORT --base $BASE_PATH" },
  "deploy": {
    "target": "cloudflare",
    "build": "npm run build",
    "static": "dist",
    "spa": true,
    "domain": "phim.bomclaw.org",
    "database": { "engine": "sqlite", "file": "data/app.db" }
  }
}
```

| field | meaning |
|---|---|
| `target` | the provider: `cloudflare` (default) or `fly` |
| `name` | the Worker name, which is also its workers.dev subdomain. Default: the channel id without `ch_`. |
| `build` | the command run in the project before uploading |
| `cwd` | the folder `build` runs in and `static` is relative to |
| `static` | the **built** folder to serve |
| `spa` | serve `index.html` for unknown paths (client-side routers) |
| `main` | an optional Worker script in front of the files, e.g. an API. Only a script can read the database (`env.DB`). |
| `domain` | a custom hostname. On Cloudflare it must be on a zone in the account. On Fly.io a certificate is added (`flyctl certs add`), and the hostname needs a CNAME to `<name>.fly.dev`. Optional. |
| `dockerfile` | Fly.io only: the project's own image, for an app that runs a server |
| `port` | Fly.io only: the port that image listens on (default 8080) |
| `region` | Fly.io only: where it runs (default `$FLY_REGION`, else `sin`) |
| `database` | `engine: "sqlite"` and `file`: the local file that becomes a D1 database (`name` defaults to `<name>-db`, `binding` to `DB`) |

`bomclaw env init <channel> --static dist --build "npm run build" --domain x.bomclaw.org --db data/app.db`
writes this section.

## Deploying

```bash
bomclaw env deploy <channel> --dry-run   # build, print the wrangler config, send nothing (no credentials needed)
bomclaw env deploy <channel>             # build and publish the site
bomclaw env deploy <channel> --with-db   # also replace the PROD database with the local file
```

On Cloudflare a deploy:

1. Runs `build`.
2. With a database (`--with-db`, or always when there is a `main` script that
   binds it), creates the D1 database the first time.
3. With `--with-db`, converts the local file with `sqlite3 .dump` into SQL that
   D1 accepts and that **replaces** the tables. D1 imports SQL rather than
   `.sqlite` files and refuses `BEGIN`/`COMMIT`. The dump is prefixed with
   `DROP TABLE IF EXISTS` for each table, so importing twice does not double
   the rows. It then runs `wrangler d1 execute <db> --remote --file`.
4. Generates a `wrangler.json`: the name, a pinned `compatibility_date`, the
   assets directory, the custom domain as a `custom_domain` route, and the D1
   binding when there is a script. It then runs `wrangler deploy` through
   `npx wrangler@4`, so nothing needs installing.

The Cloudflare deploy uses Workers with static assets, not Pages. Cloudflare
now points new static sites there, and it is the same product as Workers, so a
static site can gain an API later without moving.

## On Fly.io

`"target": "fly"` puts PROD on Fly Machines. This is the right choice when the
project runs a server process (Node, Python, Go, a websocket), which a
Cloudflare Worker cannot:

1. Runs `build`.
2. Takes the image: the project's `dockerfile`, or, for `static`, an
   `nginx:1.27-alpine` image generated outside the project that serves the
   built folder on 8080 (`spa` routes unknown paths to `index.html`).
3. Generates a `fly.toml` with one HTTP service, HTTPS forced, and machines
   that stop when idle and start on a request. A project site should not bill
   around the clock while nobody is using it.
4. Creates the app the first time (Machines API, in `$FLY_ORG`), then runs
   `flyctl deploy <project> --config … --dockerfile … --remote-only`, so the
   image is built on Fly's builder and Docker is not needed here.
5. With `domain`, runs `flyctl certs add <domain>`.

Status comes from the Machines API (the app and its machines). The domain's
certificate status comes from Fly's GraphQL API. Fly databases are not part
of this provider yet: `database` and `--with-db` are refused with a message.

Credentials: `FLY_API_TOKEN` (from `fly tokens create org`, or `fly auth
token`), optionally `FLY_ORG` and `FLY_REGION`, and `flyctl` installed
(`brew install flyctl`).

Fly's MCP server (`mcp.fly.io`, or the `fly@sprites` Claude Code plugin) is
the other way an agent drives Fly. It exposes Machines, volumes and Managed
Postgres as tools and suits one-off infrastructure work. This provider is what
the dashboard and `bomclaw env` use: one declared app, deployed the same way
every time.

## Connecting Cloudflare

Add two lines to the dashboard's and the agents' `.env` (`~/.bomclaw/.env`,
`~/.bomclaw2/.env`, …) and restart:

```
CLOUDFLARE_API_TOKEN=…
CLOUDFLARE_ACCOUNT_ID=…
```

Create the token at *My Profile → API Tokens → Create Token → Custom*:

| permission | scope |
|---|---|
| Account · Workers Scripts · Edit | the account |
| Account · D1 · Edit | the account |
| Account · Account Settings · Read | the account (wrangler reads the workers.dev subdomain) |
| Zone · Workers Routes · Edit | the zone(s) you put custom domains on |
| Zone · DNS · Edit | the same zone(s) (a custom domain creates its DNS record) |
| Zone · Zone · Read | the same zone(s) |

These are the same names wrangler reads, so a person running `wrangler` by
hand in a project uses the same token. The cloudflared tunnel (`~/.cloudflared`)
is a separate thing: it publishes *local* services through this machine, and
PROD does not use it.

## Providers

`internal/deploy.Provider` is the seam. Each provider implements `Validate`,
`Connected`, `Status` and `Deploy`, and registers itself under its `target`
name. The dashboard panel, `bomclaw env` and the agents' skill only talk to
the interface.

Built: **Cloudflare** (`cloudflare`) and **Fly.io** (`fly`).

Planned:

- **Supabase** as a database engine (`database.engine: "supabase"`, reserved
  and refused today), for projects that outgrow SQLite or need auth and
  storage with it.
- **Fly Managed Postgres** as the database for `fly` apps.
- Further hosts register the same way. `vercel` is listed as planned, so
  trying it gives that answer.

## Security

- The token can publish under your domains, so it lives in `.env` files
  readable only by you, like the other keys.
- Publishing is never automatic. An agent deploys only when asked. The skill
  tells agents never to use `--with-db` on their own once a site has data they
  did not create.
- Dry runs need no credentials, so an agent can prepare and check a deploy
  without the token.

</div>
</section>
