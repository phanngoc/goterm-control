package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func rpcDeps(t *testing.T) (Deps, *coord.DB) {
	t.Helper()
	db := testCoordDB(t)
	carrier(t, db, "bomclaw")
	return Deps{Coord: db, AgentID: "bomclaw", OwnerChatID: 4242}, db
}

func rpc(t *testing.T, fn func(Deps, json.RawMessage) (json.RawMessage, error), deps Deps, params string) json.RawMessage {
	t.Helper()
	out, err := fn(deps, json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", params, err)
	}
	return out
}

// The whole round trip a screen makes: see what is there, add one, change it,
// take it away.
func TestGatewayRPCRoundTrip(t *testing.T) {
	deps, _ := rpcDeps(t)

	var listed struct {
		Gateways      []coord.ChannelGateway `json:"gateways"`
		DefaultTarget string                 `json:"default_target"`
		DefaultAgent  string                 `json:"default_agent"`
		Kinds         []string               `json:"kinds"`
	}
	if err := json.Unmarshal(rpc(t, handleChannelGateways, deps, `{}`), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Gateways) != 0 {
		t.Fatalf("a fresh install already speaks somewhere: %+v", listed.Gateways)
	}
	// The screen must be able to offer "your own chat" without asking anyone
	// to remember a number.
	if listed.DefaultTarget != "4242" || listed.DefaultAgent != "bomclaw" {
		t.Errorf("no defaults for the add form: %+v", listed)
	}
	if len(listed.Kinds) < 2 {
		t.Errorf("the screen cannot offer transports it is not told about: %v", listed.Kinds)
	}

	// Kind, target and carrier all default: the least a person can type.
	var made coord.ChannelGateway
	if err := json.Unmarshal(
		rpc(t, handleChannelBind, deps, `{"channel_id":"ch_general"}`), &made); err != nil {
		t.Fatal(err)
	}
	if made.Kind != coord.GatewayTelegram || made.Target != "4242" || made.AgentID != "bomclaw" {
		t.Fatalf("the defaults did not fill in: %+v", made)
	}
	if made.Mode != coord.ForwardMentions {
		t.Errorf("telegram should start narrow, got %q", made.Mode)
	}

	// A webhook beside it: the point of the whole change.
	if _, err := handleChannelBind(deps,
		json.RawMessage(`{"channel_id":"ch_general","kind":"webhook","target":"https://a.test/h","secret":"s3cret","label":"Slack"}`)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc(t, handleChannelGateways, deps, `{"channel_id":"ch_general"}`), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Gateways) != 2 {
		t.Fatalf("one room could not hold two destinations: %+v", listed.Gateways)
	}

	// Editing by id must not need the target spelled out again.
	var edited coord.ChannelGateway
	if err := json.Unmarshal(rpc(t, handleChannelBind, deps,
		`{"id":"`+made.ID+`","mode":"off"}`), &edited); err != nil {
		t.Fatal(err)
	}
	if edited.Mode != coord.ForwardOff || edited.Target != "4242" {
		t.Fatalf("editing the mode lost the destination: %+v", edited)
	}

	if _, err := handleChannelUnbind(deps, json.RawMessage(`{"id":"`+made.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rpc(t, handleChannelGateways, deps, `{"channel_id":"ch_general"}`), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Gateways) != 1 || listed.Gateways[0].Kind != coord.GatewayWebhook {
		t.Fatalf("unbind removed the wrong one: %+v", listed.Gateways)
	}
}

// A secret is write-only. It reaches the screen as "there is one" and never as
// the token itself, because a screen that can show it is a screen that leaks it.
func TestAWebhookSecretNeverLeavesTheServer(t *testing.T) {
	deps, _ := rpcDeps(t)
	if _, err := handleChannelBind(deps,
		json.RawMessage(`{"channel_id":"ch_general","kind":"webhook","target":"https://a.test/h","secret":"s3cret"}`)); err != nil {
		t.Fatal(err)
	}
	out := rpc(t, handleChannelGateways, deps, `{"channel_id":"ch_general"}`)
	if strings.Contains(string(out), "s3cret") {
		t.Fatalf("the secret came back over the wire: %s", out)
	}
	if !strings.Contains(string(out), `"has_secret":true`) {
		t.Errorf("the screen cannot tell there is a secret at all: %s", out)
	}
}
