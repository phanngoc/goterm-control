---
title: Home
layout: default
---

<section class="hero">
  <div class="container">
    <div class="hero-badge">Open Source &middot; MIT License</div>
    <h1>Your Computer,<br>Claude's Brain</h1>
    <p class="hero-sub">
      BomClaw is a lean, self-hosted AI agent that gives Claude full control over your computer.
      One binary, one process, no microservices. Command it from Telegram, the web, or your terminal.
    </p>
    <div class="hero-actions">
      <a href="{{ '/getting-started' | relative_url }}" class="btn btn-primary">Get Started</a>
      <a href="https://github.com/phanngoc/goterm-control" class="btn btn-outline" target="_blank">View on GitHub</a>
    </div>
    <div style="margin-top: 2rem; display: flex; gap: 1rem; justify-content: center; flex-wrap: wrap;">
      <div style="text-align: center;">
        <img src="{{ '/assets/telegram-demo.png' | relative_url }}" width="320" alt="Telegram bot demo — tool calls and structured output" style="border-radius: 12px; box-shadow: 0 4px 24px rgba(0,0,0,0.18);" />
      </div>
      <div style="text-align: center;">
        <img src="{{ '/assets/telegram-chat.png' | relative_url }}" width="320" alt="Telegram bot chat — interactive conversation" style="border-radius: 12px; box-shadow: 0 4px 24px rgba(0,0,0,0.18);" />
      </div>
    </div>
    <p style="color: #888; font-size: 0.9rem; margin-top: 0.5rem; text-align: center;"><em>Left: tool calls, web crawling &amp; structured output &nbsp;|&nbsp; Right: interactive chat with book recommendations</em></p>
    <div class="hero-code">
      <div class="hero-code-header">
        <span class="hero-code-dot"></span>
        <span class="hero-code-dot"></span>
        <span class="hero-code-dot"></span>
      </div>
<pre><span class="comment"># Build</span>
<span class="cmd">go build</span> <span class="flag">-o bomclaw</span> ./cmd/bomclaw/

<span class="comment"># Start chatting</span>
<span class="cmd">./bomclaw</span> chat

<span class="comment"># Or launch the full gateway</span>
<span class="cmd">./bomclaw</span> gateway <span class="flag">--port 18789</span></pre>
    </div>
  </div>
</section>

<section class="section" style="padding-bottom: 1rem;">
  <div class="container">
    <div class="section-header">
      <h2>Philosophy</h2>
      <p>What makes BomClaw different from every other AI agent framework.</p>
    </div>
    <div class="features-grid" style="grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 1.2rem;">
      <div class="feature-card" style="text-align: center;">
        <div class="feature-icon purple" style="font-size: 2rem;">&#x1F5A5;&#xFE0F;</div>
        <h3>Full Computer Control</h3>
        <p>Shell, files, processes, clipboard, browser, screenshots &mdash; the agent can do anything you can do on your machine.</p>
      </div>
      <div class="feature-card" style="text-align: center;">
        <div class="feature-icon green" style="font-size: 2rem;">&#x1F30D;</div>
        <h3>Cross-Platform</h3>
        <p>Runs wherever Go compiles &mdash; Linux, macOS, Windows via WSL. One codebase, every platform.</p>
      </div>
      <div class="feature-card" style="text-align: center;">
        <div class="feature-icon yellow" style="font-size: 2rem;">&#x1F9E9;</div>
        <h3>Small Enough to Understand</h3>
        <p>~65 source files, 18 packages. Read the entire codebase in an afternoon. No magic, no abstractions you can't trace.</p>
      </div>
      <div class="feature-card" style="text-align: center;">
        <div class="feature-icon pink" style="font-size: 2rem;">&#x1F464;</div>
        <h3>Built for One User</h3>
        <p>Bespoke, not a framework. No multi-tenant auth, no plugin system, no enterprise features you'll never use.</p>
      </div>
      <div class="feature-card" style="text-align: center;">
        <div class="feature-icon blue" style="font-size: 2rem;">&#x2702;&#xFE0F;</div>
        <h3>Customization = Code</h3>
        <p>No config sprawl. Want to change behavior? Change the code. It's small enough that you can.</p>
      </div>
    </div>
  </div>
</section>

<section class="section">
  <div class="container">
    <div class="stats">
      <div class="stat">
        <div class="stat-value">~65</div>
        <div class="stat-label">Source Files</div>
      </div>
      <div class="stat">
        <div class="stat-value">25</div>
        <div class="stat-label">Built-in Tools</div>
      </div>
      <div class="stat">
        <div class="stat-value">18</div>
        <div class="stat-label">Packages</div>
      </div>
      <div class="stat">
        <div class="stat-value">13MB</div>
        <div class="stat-label">Binary Size</div>
      </div>
    </div>
  </div>
</section>

<section class="section">
  <div class="container">
    <div class="section-header">
      <h2>Why BomClaw?</h2>
      <p>Everything you need to turn your machine into an AI-powered workstation.</p>
    </div>
    <div class="features-grid">
      <div class="feature-card">
        <div class="feature-icon purple">&#x1F916;</div>
        <h3>Agentic Loop</h3>
        <p>The model calls tools, sees results, and keeps working &mdash; up to 50 iterations per request. It doesn't just answer; it <em>does</em>.</p>
      </div>
      <div class="feature-card">
        <div class="feature-icon green">&#x1F310;</div>
        <h3>Multi-Channel Access</h3>
        <p>Talk to your agent from a Telegram bot, a React web dashboard, or an interactive CLI. All channels share the same gateway.</p>
      </div>
      <div class="feature-card">
        <div class="feature-icon pink">&#x1F5A5;&#xFE0F;</div>
        <h3>Full Computer Control</h3>
        <p>Shell commands, file I/O, screenshots, clipboard, processes, and native Chrome DevTools Protocol for browser automation.</p>
      </div>
      <div class="feature-card">
        <div class="feature-icon yellow">&#x1F9E0;</div>
        <h3>Session Continuity</h3>
        <p>Claude CLI session resumption and conversation history provide seamless context across interactions.</p>
      </div>
      <div class="feature-card">
        <div class="feature-icon blue">&#x26A1;</div>
        <h3>Single Binary</h3>
        <p>One Go binary (~13MB), one process, zero dependencies. No Docker, no Node.js, no microservices. Just build and run.</p>
      </div>
      <div class="feature-card">
        <div class="feature-icon red">&#x1F512;</div>
        <h3>Your Machine, Your Data</h3>
        <p>Self-hosted and single-user. Data stays in JSONL files on your disk. Gateway binds to localhost by default.</p>
      </div>
    </div>
  </div>
</section>

<section class="section">
  <div class="container">
    <div class="section-header">
      <h2>Architecture</h2>
      <p>Clean separation of channels, gateway, agent loop, and tools.</p>
    </div>
    <div class="arch-diagram">
<pre>
┌───────────┐  ┌──────────────┐  ┌───────────────┐
│  Telegram  │  │ Web Dashboard│  │   CLI Chat    │
│  Bot       │  │  (React SPA) │  │               │
└─────┬──────┘  └──────┬───────┘  └───────┬───────┘
      │           WebSocket          direct call
      │               │                  │
      ▼               ▼                  ▼
┌─────────────────────────────────────────────────┐
│              Gateway (JSON-RPC)                  │
│         session mgmt · model resolver            │
└──────────────────────┬──────────────────────────┘
                       │
              ┌─────────────────┐
              │   Agent Loop    │  ← up to 50 iterations
              │  stream + tool  │
              │   call cycle    │
              └────────┬────────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
   ┌────────────┐ ┌──────────┐ ┌──────────┐
   │ Claude CLI │ │  Direct  │ │   Tool   │
   │  (OAuth2)  │ │   API    │ │ Executor │
   └────────────┘ └──────────┘ └──────────┘
                                    │
                       ┌────────────┼────────────┐
                       ▼            ▼            ▼
                  ┌─────────┐ ┌─────────┐ ┌──────────┐
                  │ Context │ │Messages │ │Transcript│
                  └─────────┘ └─────────┘ └──────────┘
</pre>
    </div>
  </div>
</section>

<section class="section">
  <div class="container">
    <div class="section-header">
      <h2>Flexible Authentication</h2>
      <p>Use your Claude Pro/Max subscription or a direct API key.</p>
    </div>
    <div class="features-grid" style="grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));">
      <div class="feature-card">
        <h3>Claude CLI (Recommended)</h3>
        <p>Uses your existing Claude Pro/Max subscription via OAuth2. No per-token billing. Just run <code>claude login</code> and go.</p>
        <p style="margin-top:12px;"><code>Token: sk-ant-oat...</code></p>
      </div>
      <div class="feature-card">
        <h3>Direct Anthropic API</h3>
        <p>Pay-per-use with your Anthropic API key. Set <code>ANTHROPIC_API_KEY</code> in your <code>.env</code> file.</p>
        <p style="margin-top:12px;"><code>Token: sk-ant-api03...</code></p>
      </div>
    </div>
  </div>
</section>

<section class="section">
  <div class="container">
    <div class="section-header">
      <h2>Three Built-in Models</h2>
      <p>Switch models per-session with simple aliases.</p>
    </div>
    <div class="doc-content" style="margin:0 auto;">
      <table>
        <thead>
          <tr><th>Model</th><th>Aliases</th><th>Context</th><th>Input / Output (per 1M)</th></tr>
        </thead>
        <tbody>
          <tr><td>Claude Opus 4.6</td><td><code>opus</code>, <code>o4</code></td><td>200k</td><td>$15 / $75</td></tr>
          <tr><td>Claude Sonnet 4.6</td><td><code>sonnet</code>, <code>s4</code></td><td>200k</td><td>$3 / $15</td></tr>
          <tr><td>Claude Haiku 4.5</td><td><code>haiku</code>, <code>h4</code></td><td>200k</td><td>$0.80 / $4</td></tr>
        </tbody>
      </table>
    </div>
  </div>
</section>

<section class="cta">
  <div class="container">
    <h2>Ready to get started?</h2>
    <p>Clone, build, and start chatting in under a minute.</p>
    <div class="hero-actions">
      <a href="{{ '/getting-started' | relative_url }}" class="btn btn-primary">Installation Guide</a>
      <a href="{{ '/features' | relative_url }}" class="btn btn-outline">Explore Features</a>
    </div>
  </div>
</section>
