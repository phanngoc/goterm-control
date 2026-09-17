package skills

import (
	"fmt"
	"strings"
)

// Who else is here, and what they are actually good at.
//
// Both places that described the other agents said the same empty thing:
// "different backends, so different strengths and different costs", and then
// named no strength. An agent choosing who to hand a piece of work to had
// three names, three backend labels, and nothing to choose on — so it chose by
// guessing, which is what happened: a 3D task went to two peers on no basis
// beyond there being two peers.
//
// A peer's skills answer the question, and they answer it from the same files
// the peer's own prompt is built from. Nothing is published and nothing is
// stored, so nothing can go stale — which is how the old roster failed. It was
// prose in a config file, written once, and by the time a third agent existed
// it was wrong: agent 1 said out loud that it had no idea who bomclaw3 was.

// MaxSkillsPerPeer bounds one peer's line. A roster is for choosing between
// agents, not for reading their manuals — and this is paid on every prompt
// that mentions the peers at all.
const MaxSkillsPerPeer = 6

// MaxPeerSkillRunes is how much of a skill's description survives into a
// roster. The full text is in the peer's own index; here it only has to be
// enough to pick.
const MaxPeerSkillRunes = 90

// Peer is one other agent, as the roster needs it. A local struct rather than
// coord.Agent: this package is a leaf and stays one.
type Peer struct {
	ID        string
	Provider  string
	Model     string
	Workspace string
	Online    bool
}

// Roster describes the other agents to one agent, skills included. Empty when
// there are no peers, so a prompt never carries a heading over nothing.
func Roster(peers []Peer) string {
	var lines []string
	for _, p := range peers {
		state := "online"
		if !p.Online {
			state = "offline right now"
		}
		backend := p.Provider
		if p.Model != "" {
			backend = fmt.Sprintf("%s · %s", p.Provider, p.Model)
		}
		line := fmt.Sprintf("- **%s** — %s (%s)", p.ID, backend, state)
		if known := describe(p.Workspace); known != "" {
			line += "\n" + known
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## The other agents on this machine\n\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

// describe lists what one peer can do, read from the peer's own skills folder.
//
// An unreadable folder yields nothing rather than an error: a peer whose disk
// this agent cannot see is still a peer worth naming, and a roster that failed
// to render because one workspace was missing would take the other peers with
// it.
func describe(workspace string) string {
	list, err := Load(workspace)
	if err != nil || len(list) == 0 {
		return ""
	}
	if len(list) > MaxSkillsPerPeer {
		list = list[:MaxSkillsPerPeer]
	}
	var b strings.Builder
	for _, s := range list {
		fmt.Fprintf(&b, "  - %s: %s\n", s.Name, cut(firstSentence(s.Description), MaxPeerSkillRunes))
	}
	return strings.TrimRight(b.String(), "\n")
}

// firstSentence keeps the "what it is" and drops the "when not to" — the
// caveats matter to the agent holding the skill and not to the one deciding
// whom to ask.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
