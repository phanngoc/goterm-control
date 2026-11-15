package coord

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Which project a conversation turn was about.
//
// A turn answered in a project's room belongs to that project. A turn in the
// owner's Direct room — or on Telegram, which lands there — belongs to the
// room it was asked in, which is nobody's project: work on the shop site asked
// for from the phone was filed under "dm_bomclaw__owner" and never showed up
// on the shop's board.
//
// The turn itself says where it belongs: the folders and repos it worked in
// are the project's (channels.workspace, channel_repos.path), and the request
// usually names it. GuessProject weighs both and answers only when one
// project clearly wins; a turn that touched nothing in particular stays where
// it was asked.

// ProjectHint is one project's places, for matching.
type ProjectHint struct {
	ChannelID string
	Name      string
	Paths     []string // absolute folders: the workspace and every repo checkout
}

// ProjectHints lists every project with the folders it owns.
func (db *DB) ProjectHints() ([]ProjectHint, error) {
	// archived_at is '' (not NULL) on a live room.
	rows, err := db.conn.Query(`SELECT id, name, workspace FROM channels WHERE workspace != '' AND COALESCE(archived_at, '') = ''`)
	if err != nil {
		return nil, err
	}
	byID := map[string]*ProjectHint{}
	var order []string
	for rows.Next() {
		var h ProjectHint
		var ws string
		if err := rows.Scan(&h.ChannelID, &h.Name, &ws); err != nil {
			rows.Close()
			return nil, err
		}
		h.Paths = []string{ws}
		byID[h.ChannelID] = &h
		order = append(order, h.ChannelID)
	}
	rows.Close()
	repos, err := db.conn.Query(`SELECT channel_id, path FROM channel_repos WHERE path != ''`)
	if err == nil {
		for repos.Next() {
			var id, p string
			if repos.Scan(&id, &p) == nil && byID[id] != nil {
				byID[id].Paths = append(byID[id].Paths, p)
			}
		}
		repos.Close()
	}
	out := make([]ProjectHint, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// GuessProject picks the project a turn was about from what it did (tool
// inputs: commands, file paths) and what was asked. "" when no project wins
// clearly.
//
// Scoring: a tool input inside one of a project's folders is strong evidence
// (+3); the request naming the project is weaker (+2) — a person can mention a
// project while asking about something else. A project needs 3 points, and
// must lead the next one by 2, so one stray mention never moves a turn.
//
// A folder that is inside another project's folder — arcade's games/ holds
// several repos — counts for the deepest match only.
func GuessProject(hints []ProjectHint, request string, toolInputs []string) string {
	home, _ := os.UserHomeDir()
	type place struct {
		id, path string
	}
	var places []place
	for _, h := range hints {
		for _, p := range h.Paths {
			p = strings.TrimRight(filepath.Clean(p), "/")
			if p == "" || p == "." || p == "/" || p == home {
				continue
			}
			places = append(places, place{h.ChannelID, p})
			if home != "" && strings.HasPrefix(p, home+"/") {
				places = append(places, place{h.ChannelID, "~" + strings.TrimPrefix(p, home)})
			}
		}
	}
	score := map[string]int{}
	for _, in := range toolInputs {
		best, bestLen := "", 0
		for _, pl := range places {
			if containsPath(in, pl.path) && len(pl.path) > bestLen {
				best, bestLen = pl.id, len(pl.path)
			}
		}
		if best != "" {
			score[best] += 3
		}
	}
	req := fold(request)
	for _, h := range hints {
		if namesProject(req, h.Name) {
			score[h.ChannelID] += 2
		}
	}
	first, second, firstID := 0, 0, ""
	for id, s := range score {
		switch {
		case s > first:
			first, second, firstID = s, first, id
		case s > second:
			second = s
		}
	}
	if first < 3 || first-second < 2 {
		return ""
	}
	return firstID
}

// containsPath: s mentions path as a whole folder, not as a prefix of a
// longer name ("shopee-deals-settings" is not "shopee-deals").
func containsPath(s, path string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], path)
		if j < 0 {
			return false
		}
		end := i + j + len(path)
		if end == len(s) || !isNameRune(rune(s[end])) {
			return true
		}
		i = i + j + 1
	}
}

func isNameRune(r rune) bool {
	return r == '-' || r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// namesProject: the request names the project — "shopee-deals", "shopee deals",
// "shopee deal" all name ch_shopee-deals. Words of the name shorter than 4
// letters are ignored, and every remaining word must appear (a plural "s"
// optional), so "deal" alone never names it.
func namesProject(req, name string) bool {
	words := strings.FieldsFunc(fold(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	n := 0
	for _, w := range words {
		if len([]rune(w)) < 4 {
			continue
		}
		n++
		if !strings.Contains(req, w) && !strings.Contains(req, strings.TrimSuffix(w, "s")+" ") &&
			!strings.HasSuffix(req, strings.TrimSuffix(w, "s")) {
			return false
		}
	}
	return n > 0
}

// fold lowercases and strips Vietnamese diacritics, so "Phim ngắn" matches
// "phim-ngan-25d".
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r == 'đ' {
			b.WriteRune('d')
			continue
		}
		if base, ok := viBase[r]; ok {
			r = base
		}
		b.WriteRune(r)
	}
	return b.String()
}

var viBase = func() map[rune]rune {
	m := map[rune]rune{}
	for base, all := range map[rune]string{
		'a': "àáảãạăằắẳẵặâầấẩẫậ", 'e': "èéẻẽẹêềếểễệ", 'i': "ìíỉĩị",
		'o': "òóỏõọôồốổỗộơờớởỡợ", 'u': "ùúủũụưừứửữự", 'y': "ỳýỷỹỵ",
	} {
		for _, r := range all {
			m[r] = base
		}
	}
	return m
}()
