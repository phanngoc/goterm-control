package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/ngocp/goterm-control/internal/coord"
)

// RPC for the shared channels and for artifacts.
//
// The dashboard is the human's seat in the room, so unless a call says
// otherwise it acts as the owner — not as this gateway's agent. The old
// Messages tab could only speak as an agent, which is why the human's own
// words never appeared in the agents' history at all.

// --- channels --------------------------------------------------------------

type channelsListParams struct {
	// As selects whose view this is: "user" (the dashboard's own seat, the
	// default), "agent" for this gateway's agent, or "all" for every channel.
	As string `json:"as,omitempty"`
}

func handleChannelsList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p channelsListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	kind, id := viewer(deps, p.As)
	channels, err := deps.Coord.ListChannels(kind, id)
	if err != nil {
		return nil, err
	}
	return json.Marshal(channels)
}

type channelMessagesParams struct {
	ChannelID  string `json:"channel_id"`
	ThreadRoot string `json:"thread_root,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

func handleChannelMessages(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p channelMessagesParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ThreadRoot != "" {
		msgs, err := deps.Coord.ThreadMessages(p.ThreadRoot)
		if err != nil {
			return nil, err
		}
		return json.Marshal(msgs)
	}
	if p.ChannelID == "" {
		return nil, fmt.Errorf("channel_id is required")
	}
	msgs, err := deps.Coord.ChannelMessages(p.ChannelID, p.Limit)
	if err != nil {
		return nil, err
	}
	return json.Marshal(msgs)
}

type channelPostParams struct {
	ChannelID  string `json:"channel_id"`
	ThreadRoot string `json:"thread_root,omitempty"`
	Body       string `json:"body"`
	TaskID     string `json:"task_id,omitempty"`
	// As is who the message is from: "user" (default) or an agent id.
	As string `json:"as,omitempty"`
}

func handleChannelPost(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p channelPostParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	kind, id := author(deps, p.As)
	msg, wake, err := deps.Coord.PostMessage(coord.NewChannelMessage{
		ChannelID: p.ChannelID, ThreadRoot: p.ThreadRoot,
		AuthorKind: kind, AuthorID: id, Body: p.Body, TaskID: p.TaskID,
	})
	if err != nil {
		return nil, err
	}
	// Being named is the only thing that wakes an agent; see coord/channels.go.
	for _, who := range wake {
		NotifyAgents(deps.Coord, who, "", "about a mention in "+p.ChannelID)
	}
	return json.Marshal(msg)
}

type channelCreateParams struct {
	Name    string   `json:"name"`
	Purpose string   `json:"purpose,omitempty"`
	Members []string `json:"members,omitempty"` // agent ids; the owner always joins
}

func handleChannelCreate(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p channelCreateParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	members := []coord.Member{{Kind: coord.MemberUser, ID: coord.OwnerUserID}}
	for _, id := range p.Members {
		members = append(members, coord.Member{Kind: coord.MemberAgent, ID: id})
	}
	c, err := deps.Coord.CreateChannel("", p.Name, coord.ChannelPublic, p.Purpose, coord.OwnerUserID, members)
	if err != nil {
		return nil, err
	}
	return json.Marshal(c)
}

type channelReadParams struct {
	ChannelID string `json:"channel_id"`
	As        string `json:"as,omitempty"`
	// MessageIDs, when given, clears those mentions too — the dashboard does
	// this when the human reads a line that named an agent it speaks for.
	MessageIDs []string `json:"message_ids,omitempty"`
}

func handleChannelRead(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p channelReadParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	kind, id := viewer(deps, p.As)
	if id == "" {
		return nil, fmt.Errorf("channels.read needs a member to mark read for")
	}
	if err := deps.Coord.MarkChannelRead(p.ChannelID, kind, id); err != nil {
		return nil, err
	}
	var cleared int64
	if len(p.MessageIDs) > 0 {
		var err error
		if cleared, err = deps.Coord.MarkMentionsRead(kind, id, p.MessageIDs); err != nil {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"ok": true, "mentions_cleared": cleared})
}

// --- artifacts -------------------------------------------------------------

type artifactsListParams struct {
	TaskID    string `json:"task_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
	Tree      bool   `json:"tree,omitempty"` // with task_id: the whole tree
}

func handleArtifactsList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p artifactsListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	switch {
	case p.ContextID != "":
		arts, err := deps.Coord.ContextArtifacts(p.ContextID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(arts)
	case p.TaskID != "" && p.Tree:
		t, err := deps.Coord.GetTask(p.TaskID)
		if err != nil {
			return nil, err
		}
		arts, err := deps.Coord.ContextArtifacts(t.ContextID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(arts)
	case p.TaskID != "":
		arts, err := deps.Coord.TaskArtifacts(p.TaskID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(arts)
	}
	return nil, fmt.Errorf("artifacts.list needs task_id or context_id")
}

// MaxArtifactInline caps what artifacts.get returns over the socket. The
// dashboard is a viewer, not a delivery channel: anything larger is read from
// disk by whoever needs the whole thing.
const MaxArtifactInline = 256 * 1024

type artifactGetParams struct {
	ID string `json:"id"`
}

func handleArtifactGet(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	if deps.Coord == nil {
		return nil, errNoCoord()
	}
	var p artifactGetParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	a, err := deps.Coord.GetArtifact(p.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"artifact": a}
	if a.Kind != coord.ArtifactLink {
		content, err := deps.Coord.ReadArtifact(a.ID)
		switch {
		case err != nil:
			out["error"] = err.Error()
		case len(content) > MaxArtifactInline:
			out["content"] = string(content[:MaxArtifactInline])
			out["truncated"] = true
		default:
			out["content"] = string(content)
		}
	}
	return json.Marshal(out)
}

// --- helpers ---------------------------------------------------------------

// viewer resolves whose unread counts and read cursor a call is about.
func viewer(deps Deps, as string) (kind, id string) {
	switch as {
	case "all":
		return "", ""
	case "agent":
		return coord.MemberAgent, deps.AgentID
	case "", "user", coord.OwnerUserID:
		return coord.MemberUser, coord.OwnerUserID
	default:
		return coord.MemberAgent, as
	}
}

// author resolves who a posted message is from. Unlike viewer, "all" is not a
// speaker, so it falls through to the human.
func author(deps Deps, as string) (kind, id string) {
	if as == "" || as == "user" || as == coord.OwnerUserID || as == "all" {
		return coord.MemberUser, coord.OwnerUserID
	}
	return coord.MemberAgent, as
}

func decodeParams(params json.RawMessage, into any) error {
	if len(params) == 0 {
		return nil
	}
	if err := json.Unmarshal(params, into); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}
