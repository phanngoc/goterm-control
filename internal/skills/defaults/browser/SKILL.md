---
name: browser
description: 'Drive the user''s OWN logged-in browser. Use when: a page needs a login, a site blocks scripted fetches, you must click/fill/submit, or you need to see what the user sees. NOT for: fetching a public page you can just curl.'
---

# browser

`bomclaw browser` drives the browser the user is already sitting in front of,
through the Browser Bridge extension. Their tabs, their cookies, their logins.

## Why this and not a headless browser

A fetched page and a headless browser are both logged OUT. On any site that
needs an account they return a login wall, and the honest-looking conclusion —
"the site is inaccessible" — is wrong. This has happened here.

If a page needs a session, this is the only tool that has one.

## The loop

Every element you act on is addressed by a `ref` from a snapshot, so a snapshot
comes first and again after anything that changes the page.

```
bomclaw browser status                 # exit code 3 = no browser connected
bomclaw browser navigate <url>
bomclaw browser snapshot               # the page as a tree: n1, n2, n3 …
bomclaw browser click n7
bomclaw browser fill n3 "text"         # replaces the value
bomclaw browser type n3 "text"         # appends to it
bomclaw browser text --ref n5          # read one element
bomclaw browser screenshot --out p.png
```

`wait`, `scroll`, `back`, `select`, `tabs` and `eval` are there too —
`bomclaw browser` with no arguments lists everything.

## Rules

Snapshot before you click. A `ref` from an older snapshot may now point at a
different element, and clicking the wrong thing on someone's logged-in account
is not recoverable by trying again.

Exit code 3 means nobody is connected. Say so — do not fall back to fetching
the page and report what a logged-out visitor sees.

This is the user's real session. Do not log out, do not change settings, and do
not submit anything that spends money or sends a message unless you were asked
to.
