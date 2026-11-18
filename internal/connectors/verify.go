package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Verify asks the service itself whether the stored values work — the
// "Kiểm tra" button and connector_verify. get reads a value (the secrets
// store, then the environment). The answer says what the key reaches (an
// account, a mode, a workspace) so a person can tell it is the right one.
func Verify(ctx context.Context, id string, get func(string) string) (string, error) {
	c, ok := ByID(id)
	if !ok {
		return "", unknown(id)
	}
	for _, r := range c.Requires {
		rc, _ := ByID(r)
		if rc != nil {
			for _, fl := range rc.Fields {
				if fl.Required && get(fl.Env) == "" {
					return "", fmt.Errorf("%s needs %s first (%s)", c.Title, rc.Title, fl.Env)
				}
			}
		}
	}
	for _, fl := range c.Fields {
		if fl.Required && get(fl.Env) == "" {
			if fl.Managed {
				return "", fmt.Errorf("%s is not connected yet — sign in with Google on the Connectors page", c.Title)
			}
			return "", fmt.Errorf("%s is missing %s", c.Title, fl.Env)
		}
	}
	if c.verify == nil {
		return "values stored (this connector has no online check)", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return c.verify(ctx, get)
}

// HTTPClient makes the checks; a var for tests.
var HTTPClient = &http.Client{Timeout: 20 * time.Second}

type rq struct {
	method, url string
	header      map[string]string
	body        string
	user, pass  string
}

// do runs a request and returns the status and up to 1 MB of body.
func do(ctx context.Context, r rq) (int, []byte, error) {
	if r.method == "" {
		r.method = http.MethodGet
	}
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, r.url, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range r.header {
		req.Header.Set(k, v)
	}
	if r.user != "" || r.pass != "" {
		req.SetBasicAuth(r.user, r.pass)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "bomclaw-connectors")
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return 0, nil, redactErr(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// redactErr drops the URL from a transport error: some APIs take the key in
// the URL (Telegram, Gemini, Maps), and an error is shown to people and models.
func redactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %v", ue.Op, ue.Err)
	}
	return err
}

// refused turns a non-2xx answer into an error with the service's own words.
func refused(code int, b []byte) error {
	msg := apiMessage(b)
	if msg == "" {
		msg = http.StatusText(code)
	}
	return fmt.Errorf("refused (%d): %s", code, msg)
}

// apiMessage digs the human message out of the usual error shapes.
func apiMessage(b []byte) string {
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		s := strings.TrimSpace(string(b))
		if len(s) > 200 {
			s = s[:200] + "…"
		}
		return s
	}
	var walk func(any) string
	walk = func(x any) string {
		switch t := x.(type) {
		case string:
			return t
		case map[string]any:
			for _, k := range []string{"message", "error_description", "detail", "description", "error", "errors", "title", "name"} {
				if s := walk(t[k]); s != "" {
					return s
				}
			}
		case []any:
			if len(t) > 0 {
				return walk(t[0])
			}
		}
		return ""
	}
	return walk(v)
}

func getJSON(ctx context.Context, r rq, out any) error {
	code, b, err := do(ctx, r)
	if err != nil {
		return err
	}
	if code < 200 || code > 299 {
		return refused(code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("unexpected answer: %v", err)
		}
	}
	return nil
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

// ── per service ───────────────────────────────────────────────────────────

var gaID = regexp.MustCompile(`^G-[A-Z0-9]{4,}$`)

func verifyGA(_ context.Context, get func(string) string) (string, error) {
	id := get("GA_MEASUREMENT_ID")
	if !gaID.MatchString(id) {
		return "", fmt.Errorf("%q is not a GA4 measurement id (G-XXXXXXXXXX)", id)
	}
	s := "measurement id " + id + " — events show in GA → Reports → Realtime once the page loads gtag"
	if get("GA_API_SECRET") != "" {
		s += " · server events via Measurement Protocol"
	}
	return s, nil
}

func verifyMaps(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Status       string `json:"status"`
		ErrorMessage string `json:"error_message"`
	}
	u := "https://maps.googleapis.com/maps/api/geocode/json?address=Hanoi&key=" + url.QueryEscape(get("GOOGLE_MAPS_API_KEY"))
	if err := getJSON(ctx, rq{url: u}, &v); err != nil {
		return "", err
	}
	switch {
	case v.Status == "OK" || v.Status == "ZERO_RESULTS":
		return "key works (Geocoding API answered)", nil
	case strings.Contains(strings.ToLower(v.ErrorMessage), "referer"):
		return "key exists and is restricted to websites (HTTP referrer) — right for the browser; a server call needs another key", nil
	case strings.Contains(v.ErrorMessage, "not activated") || strings.Contains(v.ErrorMessage, "not authorized"):
		return "key is valid; Geocoding API is not enabled for it (Maps JavaScript API may still be) — " + v.ErrorMessage, nil
	}
	return "", fmt.Errorf("%s: %s", v.Status, v.ErrorMessage)
}

func verifyStripe(ctx context.Context, get func(string) string) (string, error) {
	key := get("STRIPE_SECRET_KEY")
	var v struct {
		Livemode  bool `json:"livemode"`
		Available []struct {
			Currency string `json:"currency"`
		} `json:"available"`
	}
	if err := getJSON(ctx, rq{url: "https://api.stripe.com/v1/balance", header: bearer(key)}, &v); err != nil {
		return "", err
	}
	mode := "TEST mode"
	if v.Livemode {
		mode = "LIVE mode — real money"
	}
	if pk := get("STRIPE_PUBLISHABLE_KEY"); pk != "" && strings.HasPrefix(pk, "pk_live") != v.Livemode {
		return "", fmt.Errorf("the secret key is %s but the publishable key is not the same mode", mode)
	}
	cur := ""
	if len(v.Available) > 0 {
		cur = " · " + strings.ToUpper(v.Available[0].Currency)
	}
	return "key works · " + mode + cur, nil
}

func verifyPayPal(ctx context.Context, get func(string) string) (string, error) {
	base, mode := "https://api-m.sandbox.paypal.com", "sandbox"
	if strings.EqualFold(get("PAYPAL_ENV"), "live") {
		base, mode = "https://api-m.paypal.com", "LIVE"
	}
	var v struct {
		AppID string `json:"app_id"`
	}
	err := getJSON(ctx, rq{method: http.MethodPost, url: base + "/v1/oauth2/token", body: "grant_type=client_credentials",
		user: get("PAYPAL_CLIENT_ID"), pass: get("PAYPAL_CLIENT_SECRET"),
		header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}}, &v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", mode, err)
	}
	return fmt.Sprintf("credentials work · %s · app %s", mode, v.AppID), nil
}

func verifyPaddle(ctx context.Context, get func(string) string) (string, error) {
	key := get("PADDLE_API_KEY")
	base, mode := "https://api.paddle.com", "LIVE"
	if strings.Contains(key, "_sdbx_") {
		base, mode = "https://sandbox-api.paddle.com", "sandbox"
	}
	var v struct {
		Data []any `json:"data"`
	}
	if err := getJSON(ctx, rq{url: base + "/event-types", header: bearer(key)}, &v); err != nil {
		return "", fmt.Errorf("%s: %w", mode, err)
	}
	return "key works · " + mode, nil
}

func verifyMollie(ctx context.Context, get func(string) string) (string, error) {
	key := get("MOLLIE_API_KEY")
	var v struct {
		Count int `json:"count"`
	}
	if err := getJSON(ctx, rq{url: "https://api.mollie.com/v2/methods", header: bearer(key)}, &v); err != nil {
		return "", err
	}
	mode := "LIVE"
	if strings.HasPrefix(key, "test_") {
		mode = "test"
	}
	return fmt.Sprintf("key works · %s · %d payment method(s) active", mode, v.Count), nil
}

func verifyShopify(ctx context.Context, get func(string) string) (string, error) {
	dom := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(get("SHOPIFY_STORE_DOMAIN"), "https://"), "http://"), "/")
	var v struct {
		Shop struct {
			Name string `json:"name"`
			Plan string `json:"plan_display_name"`
		} `json:"shop"`
	}
	if err := getJSON(ctx, rq{url: "https://" + dom + "/admin/api/2025-01/shop.json",
		header: map[string]string{"X-Shopify-Access-Token": get("SHOPIFY_ADMIN_TOKEN")}}, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("store %s · plan %s", v.Shop.Name, v.Shop.Plan), nil
}

func verifyWoo(ctx context.Context, get func(string) string) (string, error) {
	base := strings.TrimSuffix(get("WOOCOMMERCE_URL"), "/")
	if !strings.HasPrefix(base, "https://") {
		return "", fmt.Errorf("WOOCOMMERCE_URL must be https:// (Basic auth over plain HTTP is refused)")
	}
	var v []struct {
		Name string `json:"name"`
	}
	if err := getJSON(ctx, rq{url: base + "/wp-json/wc/v3/products?per_page=1",
		user: get("WOOCOMMERCE_CONSUMER_KEY"), pass: get("WOOCOMMERCE_CONSUMER_SECRET")}, &v); err != nil {
		return "", err
	}
	return "keys work · REST API answered", nil
}

func verifyResend(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Data []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"data"`
	}
	code, b, err := do(ctx, rq{url: "https://api.resend.com/domains", header: bearer(get("RESEND_API_KEY"))})
	if err != nil {
		return "", err
	}
	if code == 401 && strings.Contains(string(b), "restricted_api_key") {
		return "key works (sending access only)", nil
	}
	if code != 200 {
		return "", refused(code, b)
	}
	_ = json.Unmarshal(b, &v)
	var ds []string
	for _, d := range v.Data {
		ds = append(ds, d.Name+" ("+d.Status+")")
	}
	if len(ds) == 0 {
		return "key works · no domain verified yet — mail can only come from onboarding@resend.dev to your own address", nil
	}
	return "key works · domains: " + strings.Join(ds, ", "), nil
}

func verifyTelegram(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
		Description string `json:"description"`
	}
	tok := get("TELEGRAM_BOT_TOKEN")
	code, b, err := do(ctx, rq{url: "https://api.telegram.org/bot" + tok + "/getMe"})
	if err != nil {
		return "", err
	}
	_ = json.Unmarshal(b, &v)
	if code != 200 || !v.OK {
		return "", fmt.Errorf("refused (%d): %s", code, v.Description)
	}
	return "bot @" + v.Result.Username, nil
}

func verifySlack(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
		Team  string `json:"team"`
		User  string `json:"user"`
	}
	if err := getJSON(ctx, rq{method: http.MethodPost, url: "https://slack.com/api/auth.test", header: bearer(get("SLACK_BOT_TOKEN"))}, &v); err != nil {
		return "", err
	}
	if !v.OK {
		return "", fmt.Errorf("refused: %s", v.Error)
	}
	return fmt.Sprintf("bot %s in workspace %s", v.User, v.Team), nil
}

func verifyDiscord(ctx context.Context, get func(string) string) (string, error) {
	u := get("DISCORD_WEBHOOK_URL")
	if !strings.HasPrefix(u, "https://discord.com/api/webhooks/") && !strings.HasPrefix(u, "https://discordapp.com/api/webhooks/") {
		return "", fmt.Errorf("not a Discord webhook URL (https://discord.com/api/webhooks/…)")
	}
	var v struct {
		Name string `json:"name"`
	}
	if err := getJSON(ctx, rq{url: u}, &v); err != nil {
		return "", err
	}
	return "webhook " + v.Name, nil
}

func verifyTwilio(ctx context.Context, get func(string) string) (string, error) {
	sid := get("TWILIO_ACCOUNT_SID")
	var v struct {
		FriendlyName string `json:"friendly_name"`
		Status       string `json:"status"`
		Type         string `json:"type"`
	}
	if err := getJSON(ctx, rq{url: "https://api.twilio.com/2010-04-01/Accounts/" + url.PathEscape(sid) + ".json",
		user: sid, pass: get("TWILIO_AUTH_TOKEN")}, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("account %s · %s · %s", v.FriendlyName, v.Status, v.Type), nil
}

func verifySendGrid(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Scopes []string `json:"scopes"`
	}
	if err := getJSON(ctx, rq{url: "https://api.sendgrid.com/v3/scopes", header: bearer(get("SENDGRID_API_KEY"))}, &v); err != nil {
		return "", err
	}
	send := "cannot send mail (no mail.send scope)"
	for _, s := range v.Scopes {
		if s == "mail.send" {
			send = "can send mail"
		}
	}
	return "key works · " + send, nil
}

func verifyNotion(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	h := bearer(get("NOTION_TOKEN"))
	h["Notion-Version"] = "2022-06-28"
	if err := getJSON(ctx, rq{url: "https://api.notion.com/v1/users/me", header: h}, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("integration %s · workspace %s — share each page with it (••• → Connections)", v.Name, v.Bot.WorkspaceName), nil
}

func verifyAirtable(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		ID     string   `json:"id"`
		Scopes []string `json:"scopes"`
	}
	if err := getJSON(ctx, rq{url: "https://api.airtable.com/v0/meta/whoami", header: bearer(get("AIRTABLE_TOKEN"))}, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("token works · user %s · scopes %s", v.ID, strings.Join(v.Scopes, ", ")), nil
}

func verifyGitHub(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Login string `json:"login"`
	}
	h := bearer(get("GITHUB_TOKEN"))
	h["Accept"] = "application/vnd.github+json"
	if err := getJSON(ctx, rq{url: "https://api.github.com/user", header: h}, &v); err != nil {
		return "", err
	}
	return "user @" + v.Login, nil
}

func verifyLinear(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Data struct {
			Viewer struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := getJSON(ctx, rq{method: http.MethodPost, url: "https://api.linear.app/graphql", body: `{"query":"{ viewer { name email } }"}`,
		header: map[string]string{"Authorization": get("LINEAR_API_KEY"), "Content-Type": "application/json"}}, &v); err != nil {
		return "", err
	}
	if len(v.Errors) > 0 {
		return "", fmt.Errorf("refused: %s", v.Errors[0].Message)
	}
	return fmt.Sprintf("user %s <%s>", v.Data.Viewer.Name, v.Data.Viewer.Email), nil
}

func countModels(ctx context.Context, r rq) (string, error) {
	var v struct {
		Data   []any `json:"data"`
		Models []any `json:"models"`
	}
	if err := getJSON(ctx, r, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("key works · %d model(s) available", len(v.Data)+len(v.Models)), nil
}

func verifyOpenAI(ctx context.Context, get func(string) string) (string, error) {
	return countModels(ctx, rq{url: "https://api.openai.com/v1/models", header: bearer(get("OPENAI_API_KEY"))})
}

func verifyAnthropic(ctx context.Context, get func(string) string) (string, error) {
	return countModels(ctx, rq{url: "https://api.anthropic.com/v1/models?limit=100", header: map[string]string{
		"x-api-key": get("ANTHROPIC_API_KEY"), "anthropic-version": "2023-06-01"}})
}

func verifyGemini(ctx context.Context, get func(string) string) (string, error) {
	return countModels(ctx, rq{url: "https://generativelanguage.googleapis.com/v1beta/models?pageSize=100",
		header: map[string]string{"x-goog-api-key": get("GEMINI_API_KEY")}})
}

func verifyElevenLabs(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Subscription struct {
			Tier           string `json:"tier"`
			CharacterCount int    `json:"character_count"`
			CharacterLimit int    `json:"character_limit"`
		} `json:"subscription"`
	}
	code, b, err := do(ctx, rq{url: "https://api.elevenlabs.io/v1/user", header: map[string]string{"xi-api-key": get("ELEVENLABS_API_KEY")}})
	if err != nil {
		return "", err
	}
	if code == 401 && strings.Contains(string(b), "missing_permissions") {
		return "key works (restricted: cannot read the account)", nil
	}
	if code != 200 {
		return "", refused(code, b)
	}
	_ = json.Unmarshal(b, &v)
	s := v.Subscription
	return fmt.Sprintf("key works · plan %s · %d/%d characters used", s.Tier, s.CharacterCount, s.CharacterLimit), nil
}

func verifyFirecrawl(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Data struct {
			Remaining int `json:"remaining_credits"`
		} `json:"data"`
	}
	if err := getJSON(ctx, rq{url: "https://api.firecrawl.dev/v1/team/credit-usage", header: bearer(get("FIRECRAWL_API_KEY"))}, &v); err != nil {
		return "", err
	}
	return fmt.Sprintf("key works · %d credits left", v.Data.Remaining), nil
}

func verifyReplicate(ctx context.Context, get func(string) string) (string, error) {
	var v struct {
		Username string `json:"username"`
	}
	if err := getJSON(ctx, rq{url: "https://api.replicate.com/v1/account", header: bearer(get("REPLICATE_API_TOKEN"))}, &v); err != nil {
		return "", err
	}
	return "account " + v.Username, nil
}

func verifyGoogleClient(_ context.Context, get func(string) string) (string, error) {
	id := get("GOOGLE_CLIENT_ID")
	if !strings.HasSuffix(id, ".apps.googleusercontent.com") {
		return "", fmt.Errorf("%q is not an OAuth client id (…apps.googleusercontent.com)", id)
	}
	return "client stored — it is checked for real when a Google connector signs in", nil
}

// verifyGoogle trades the refresh token for an access token and asks Google
// who it belongs to and what it may do.
func verifyGoogle(tokenEnv, scope string) func(context.Context, func(string) string) (string, error) {
	return func(ctx context.Context, get func(string) string) (string, error) {
		tok, err := GoogleAccessToken(ctx, get("GOOGLE_CLIENT_ID"), get("GOOGLE_CLIENT_SECRET"), get(tokenEnv))
		if err != nil {
			return "", err
		}
		var info struct {
			Email string `json:"email"`
			Scope string `json:"scope"`
		}
		if err := getJSON(ctx, rq{url: "https://oauth2.googleapis.com/tokeninfo?access_token=" + url.QueryEscape(tok)}, &info); err != nil {
			return "", err
		}
		if !strings.Contains(" "+info.Scope+" ", " "+scope+" ") {
			return "", fmt.Errorf("signed in as %s but without access to %s — connect again", info.Email, scope)
		}
		return "signed in as " + info.Email, nil
	}
}
