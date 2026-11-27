package connectors

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// Rules is what every use of a connector in code has to respect. It leads
// connector_guide's answer.
const Rules = `How a project gets its connectors:
- The values are environment variables, by the names below. Never write a key into a file of the project, a commit, or a chat answer.
- Preview: the dev server is started with them (restart Preview after a connector changes). Public values also as VITE_<NAME> and NEXT_PUBLIC_<NAME> for the frontend.
- Publish: Cloudflare — secrets of the Worker (env.NAME in the "main" script); public values also at build time. Fly.io — app secrets (process.env.NAME). AWS App Runner — the service's environment (process.env.NAME); an AWS static site gets public values at build time only. Vercel — project environment variables (process.env.NAME), at build and at runtime.
- A static site has no server: only PUBLIC values can be used there. Anything secret needs a server route — add a Worker script (deploy "main", e.g. worker.js answering /api/*) or a Node server — and the browser calls that route.
- To run code locally with the values (a test, a script): bomclaw connectors exec <project> -- <command>. It never prints them.`

// Guide is how to use c in a project's code, for the agent.
func Guide(c *Connector) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%s)\n%s\n", c.Title, c.ID, c.Purpose)
	if c.Docs != "" {
		fmt.Fprintf(&b, "Credentials: %s\n", c.Docs)
	}
	b.WriteString("\nVariables:\n")
	for _, f := range c.Envs() {
		kind := "SECRET — server only"
		if f.Public {
			kind = "public — browser OK"
		}
		req := ""
		if f.Required {
			req = ", required"
		}
		fmt.Fprintf(&b, "- %s (%s%s): %s\n", f.Env, kind, req, f.Label)
	}
	if c.Guide != "" {
		b.WriteString("\nUsage:\n")
		b.WriteString(c.Guide)
		b.WriteString("\n")
	}
	return b.String()
}
