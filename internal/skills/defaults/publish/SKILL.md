---
name: publish
description: 'Publish a project to PROD with MCP tools publish_status / publish_setup / publish. Use when asked to publish, deploy or where it is live. NOT for: running it locally.'
---

# publish

A project room has a **Publish** button, the way Lovable does: one click builds
the project and puts the new version on its public address. You have the same
thing as three tools from the `bomclaw` MCP server. Use the tools — they run the
same code as the button, and a real publish is announced in the project's room
by you, so the team sees it went out.

## The tools

- `publish_status` — where PROD stands: provider, connected or not, account,
  plan, services, live URL, last publish, and whether publishing is set up.
  Changes nothing. Call it first.
- `publish_setup` — declare how the project publishes (writes `"deploy"` into
  its `bomclaw.json`): the built folder (`static`), the `build` command, the
  `domain`. Once per project. If `publish_status` found a site that is already
  live without a declaration, setup adopts it — its Worker name and domain are
  kept.
- `publish` — build and publish. Takes minutes; answers with the live URL and
  the end of the log. `dry_run: true` builds and checks without publishing.

All three take `project` (`ch_<name>`), and default to the project you are
working in.

## Rules

- **Publish when asked.** "Làm xong" is not "đăng lên": a finished change waits
  for the person to say publish, unless the task says to.
- **`with_database` replaces every PROD table** with the local SQLite file. Only
  when the person asked for exactly that, in this conversation. Never to "make
  it work".
- **Not connected** means credentials are missing: say so and point to Admin →
  Kết nối. Do not try to work around it.
- After publishing, open the live URL with `bomclaw browser` and look at it
  before saying it works.
- If the tools are not in your list (another CLI, an old copy of this plugin),
  the same commands exist in the shell: `bomclaw env status|init|deploy <channel>`.
