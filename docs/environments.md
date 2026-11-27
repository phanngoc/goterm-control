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
| where | this machine | a hosting provider — **Cloudflare**, **Fly.io**, **AWS** or **Vercel** |
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
| `target` | the provider: `cloudflare` (default), `fly`, `aws` or `vercel` |
| `name` | the Worker name, which is also its workers.dev subdomain. Default: the channel id without `ch_`. |
| `build` | the command run in the project before uploading |
| `cwd` | the folder `build` runs in and `static` is relative to |
| `static` | the **built** folder to serve |
| `spa` | serve `index.html` for unknown paths (client-side routers) |
| `main` | an optional Worker script in front of the files, e.g. an API. Only a script can read the database (`env.DB`). |
| `domain` | a custom hostname. On Cloudflare it must be on a zone in the account. On Fly.io a certificate is added (`flyctl certs add`), and the hostname needs a CNAME to `<name>.fly.dev`. Optional. |
| `dockerfile` | Fly.io and AWS (ECS Express on Fargate): the project's own image, for an app that runs a server |
| `port` | Fly.io and AWS: the port that image listens on (default 8080) |
| `region` | Fly.io: where it runs (default `$FLY_REGION`, else `sin`). AWS: the region (default `$AWS_REGION`, else `ap-southeast-1`) |
| `framework` | Vercel only: Vercel builds the source itself (`nextjs`, `vite`, `astro`…) instead of uploading `static` |
| `database` | `engine: "sqlite"` and `file`: the local file that becomes a D1 database (`name` defaults to `<name>-db`, `binding` to `DB`) |

`bomclaw env init <channel> --static dist --build "npm run build" --domain x.bomclaw.org --database data/app.db`
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

## On AWS

`"target": "aws"` has two shapes:

- **`static`** — the built folder is synced to a private S3 bucket
  (`bomclaw-<name>-<account id>`, only changed files are uploaded, deleted
  files removed) behind a CloudFront distribution with Origin Access Control,
  so the bucket is never public. HTML is served `no-cache`, everything else
  for an hour; each publish invalidates `/*`. `spa` maps 403/404 to
  `index.html`. Answers on `https://<id>.cloudfront.net`; the first rollout of
  a new distribution takes a few minutes.
- **`dockerfile`** — the image is built here for this machine's architecture
  (`linux/arm64` on Apple Silicon, `linux/amd64` otherwise), pushed to ECR
  (`bomclaw/<name>`), and run on Fargate by an **ECS Express Mode** service in
  the cluster `bomclaw` (0.25 vCPU / 512 MB, 1–2 tasks scaled on CPU) with the
  connectors' values as its environment. Express Mode puts the service behind
  an Application Load Balancer it manages (shared by up to 25 Express
  services, roughly $16+/month on its own) in the region's default VPC, and
  answers on `https://<id>.ecs.<region>.on.aws`. The health check is `/`,
  which must answer 200. The roles `bomclaw-ecs-task-execution` and
  `bomclaw-ecs-infrastructure` are created the first time. Docker must be
  installed. (App Runner was the first choice; AWS no longer lets new
  accounts create App Runner services.)

With `domain`, a static site gets an ACM certificate in us-east-1 and the
distribution an alias; an app gets a CloudFront distribution in front of its
endpoint (no caching, every method passed through) with the same certificate
and alias. The
records each asks for (certificate validation, the CNAME) are written through
Cloudflare DNS when the domain is on a Cloudflare zone, otherwise the deploy
log lists them. Version: the commit, as bucket tags or the image tag.

Credentials: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` on the
Kết nối page — or none, and the machine's AWS CLI profile (`~/.aws`,
`AWS_PROFILE`) is used. Use an IAM user with S3, CloudFront, ACM, ECR, App
Runner, IAM (role creation), CloudWatch read and Cost Explorer read — not the
root account's keys. Admin → Infra shows requests and 5xx from CloudWatch and
the month's cost by service (Cost Explorer, read at most every six hours: each
call is billed $0.01).

## On Vercel

`"target": "vercel"`, over the REST API (no CLI):

- **`static`** — the folder built here is uploaded as is (an SPA gets a
  generated `vercel.json` rewrite).
- **`framework`** — the source is uploaded and Vercel builds it, which is what
  Next.js server routes need. `node_modules`, `.git`, build output and every
  `.env*` file stay behind.

The connectors' values become the project's environment variables (encrypted,
production and preview) before the build. The deployment is followed until
ready; a failed build ends the publish with the tail of its log. With
`domain`, the domain is added to the project and its CNAME (`cname.vercel-dns.com`,
or `A 76.76.21.21` for an apex) and verification record are written through
Cloudflare DNS when possible. Version: the commit, in the deployment's `meta`.

Credentials: `VERCEL_TOKEN`, and `VERCEL_TEAM_ID` to deploy into a team.
Admin → Infra shows deployments and failed ones per project; Vercel's request
counts are not in its public API.

## Connections: where the keys live

Credentials for the providers are entered once and used by every deploy:

- **Dashboard → Admin → Kết nối.** There is one card per service
  (Cloudflare, Fly.io, AWS, Vercel, and Supabase stored for later). Each card shows its
  fields, whether each value is stored here or comes from `.env`, who set it
  and when, and a **Lưu & kiểm tra** button that asks the service whether the
  key works (e.g. "token active · account … · workers.dev subdomain …").
  Secrets come back only as a mask (`cfat…4b81`). Saving with a field left
  empty keeps the stored value.
- **In chat.** Paste the token to an agent. It runs
  `bomclaw secrets set cloudflare --stdin` with `KEY=VALUE` lines. This is the
  same store, the same check, and the same mask, and it records which agent
  set the value.
- **`.env`** still works as a fallback. A value in the store wins.

The store is `secrets.yaml` beside the coordination database
(`~/.goterm-shared/data/`). Like `tokens.yaml`, it is mode 0600 and written
atomically. It is not the coordination database, which every `bomclaw`
command opens.

```bash
bomclaw secrets list                     # every service, connected or not, masked
bomclaw secrets set fly --stdin          # KEY=VALUE lines on stdin, then a live check
bomclaw secrets verify cloudflare        # check again
bomclaw secrets unset supabase           # remove what is stored (never touches .env)
```

## Connecting Cloudflare

Enter the token and account ID on the Kết nối page, or hand them to an agent.
The `.env` equivalent is `CLOUDFLARE_API_TOKEN=…` and `CLOUDFLARE_ACCOUNT_ID=…`.

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

Built: **Cloudflare** (`cloudflare`), **Fly.io** (`fly`), **AWS** (`aws`) and
**Vercel** (`vercel`). Only Cloudflare hosts a database (D1); the others refuse
`database` with that answer.

Planned:

- **Supabase** as a database engine (`database.engine: "supabase"`, reserved
  and refused today), for projects that outgrow SQLite or need auth and
  storage with it.
- **Fly Managed Postgres** as the database for `fly` apps.
- Further hosts register the same way; one named in `planned` answers
  "planned" instead of "unknown target".

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
