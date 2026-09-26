package trace

import "testing"

// runs.tags is queried by the dashboard's search, so a malformed value is not
// cosmetic — it is a run nobody can find.
func TestTagsIsValidJSONOrNothing(t *testing.T) {
	if got := Tags("channel:ch_trading", "message:cm_1"); got != `["channel:ch_trading","message:cm_1"]` {
		t.Errorf("Tags() = %q", got)
	}
	// Nothing tagged stays distinguishable from tagged-with-nothing.
	if got := Tags(); got != "" {
		t.Errorf("no tags gave %q, want the empty string rather than []", got)
	}
	if got := Tags("", "  "); got != "" {
		t.Errorf("blank tags gave %q", got)
	}
	// A value with a quote in it must be escaped, not concatenated raw.
	if got := Tags(`a"b`); got != `["a\"b"]` {
		t.Errorf("Tags() = %q — an unescaped quote makes the whole column unparseable", got)
	}
}
