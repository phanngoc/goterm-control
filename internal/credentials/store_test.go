package credentials

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTokenStoreCRUD(t *testing.T) {
	s := NewTokenStore(filepath.Join(t.TempDir(), TokenFile))
	if err := s.Save(TokenInput{Provider: ProviderClaude, Name: "a", Kind: KindSetupToken, Value: "sk-ant-oat01-aaaaaaaa1111", Rotate: true}); err != nil {
		t.Fatal(err)
	}
	// Edit without a value keeps the secret; rename moves it.
	if err := s.Save(TokenInput{Provider: ProviderClaude, OldName: "a", Name: "a2", Kind: KindSetupToken, Rotate: false, Note: "work"}); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Load()
	tok := all[ProviderClaude].Tokens[0]
	if tok.Name != "a2" || tok.Value != "sk-ant-oat01-aaaaaaaa1111" || tok.Rotate || tok.Note != "work" {
		t.Fatalf("after edit: %+v", tok)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(s.Path()); info.Mode().Perm() != 0o600 {
			t.Fatalf("store mode %v", info.Mode())
		}
	}
	for _, bad := range []TokenInput{
		{Provider: ProviderClaude, Name: "a2", Kind: KindSetupToken, Value: "x"},   // duplicate
		{Provider: ProviderClaude, Name: "b", Kind: KindSetupToken},                // new without value
		{Provider: ProviderClaude, Name: "b", Kind: KindAccessToken, Value: "x"},   // codex kind on claude
		{Provider: ProviderCodex, Name: "b", Kind: KindSetupToken, Value: "x"},     // claude kind on codex
		{Provider: "gemini", Name: "b", Kind: KindAPIKey, Value: "x"},              // unknown provider
		{Provider: ProviderClaude, Name: "Bad Name", Kind: KindAPIKey, Value: "x"}, // name
		{Provider: ProviderClaude, Name: "c", Kind: KindAPIKey, Value: "x\ny"},     // two lines
	} {
		if err := s.Save(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := s.Delete(ProviderClaude, "a2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ProviderClaude, "a2"); err == nil {
		t.Fatal("deleted twice")
	}
}

func TestMaskShowsKindNotSecret(t *testing.T) {
	m := Mask("sk-ant-oat01-SECRETSECRETSECRET-wxyz")
	if !strings.HasPrefix(m, "sk-ant-oat01-") || !strings.HasSuffix(m, "wxyz") || strings.Contains(m, "SECRET") {
		t.Fatalf("mask = %q", m)
	}
	if Mask("short") != "••••" {
		t.Fatal("a short secret was shown")
	}
}

// A pool with the store attached picks up edits on its next turn, and a
// session pinned to the rotation survives the token it started on being
// deleted. The default-store login (the subscription logged in with
// `claude`) rotates with the tokens: they all answer from one session store.
// A login with a store of its own does not.
func TestPoolFollowsTheStore(t *testing.T) {
	s := NewTokenStore(filepath.Join(t.TempDir(), TokenFile))
	p, _ := NewPool(ProviderClaude, []Account{
		{Name: "default", ConfigDir: "~/.claude"},
		{Name: "tam", ConfigDir: "~/.claude-tam", NoRotate: true},
		{Name: "own-store", ConfigDir: "/tmp/elsewhere"},
	}, time.Hour)
	p.AttachStore(s)

	bump := func(n int) {
		later := time.Now().Add(time.Duration(n) * time.Second)
		_ = os.Chtimes(s.Path(), later, later)
	}
	for i, n := range []string{"a", "b"} {
		if err := s.Save(TokenInput{Provider: ProviderClaude, Name: n, Kind: KindSetupToken, Value: "sk-ant-oat01-" + n + "aaa1111", Rotate: true}); err != nil {
			t.Fatal(err)
		}
		bump(i + 1)
	}
	if err := s.SetRotateEvery(ProviderClaude, 2); err != nil {
		t.Fatal(err)
	}
	bump(5)

	var seq []string
	for i := 0; i < 7; i++ {
		a, err := p.Pick(GroupPin)
		if err != nil {
			t.Fatal(err)
		}
		seq = append(seq, a.Name)
	}
	if got := strings.Join(seq, ","); got != "default,default,a,a,b,b,default" {
		t.Fatalf("rotation = %s", got)
	}
	if pin := p.PinFor(Account{Name: "a", OAuthToken: "x"}); pin != GroupPin {
		t.Fatalf("a rotating token pins %q", pin)
	}
	if pin := p.PinFor(Account{Name: "own-store", ConfigDir: "/tmp/elsewhere"}); pin != "own-store" {
		t.Fatalf("a login with its own store pins %q", pin)
	}
	for _, st := range p.Status() {
		if st.Name == "a" && (!st.Managed || st.Kind != "token") {
			t.Fatalf("status a: %+v", st)
		}
	}

	// Delete both tokens: the session carries on with the default login.
	_ = s.Delete(ProviderClaude, "a")
	_ = s.Delete(ProviderClaude, "b")
	bump(9)
	for i := 0; i < 3; i++ {
		if a, err := p.Pick(GroupPin); err != nil || a.Name != "default" {
			t.Fatalf("after delete: %q %v", a.Name, err)
		}
	}
}

func TestCodexCredentialsFromTheEnvironment(t *testing.T) {
	set, unset := Account{Name: "c", Provider: ProviderCodex, OAuthToken: "eyJaccess"}.Env()
	if set["CODEX_ACCESS_TOKEN"] != "eyJaccess" || !contains(unset, "CODEX_API_KEY") || !contains(unset, "OPENAI_API_KEY") {
		t.Fatalf("access token: %v %v", set, unset)
	}
	set, unset = Account{Name: "k", Provider: ProviderCodex, APIKey: "sk-proj-x"}.Env()
	if set["CODEX_API_KEY"] != "sk-proj-x" || !contains(unset, "CODEX_ACCESS_TOKEN") {
		t.Fatalf("api key: %v %v", set, unset)
	}
	if _, has := set["CODEX_HOME"]; has {
		t.Fatal("an env credential moved codex's thread store")
	}
}
