package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var hashtagRe = regexp.MustCompile(`#([^\s#]+)`)

// TagList is the list of channel hashtags (each with its "#") of one author. In JSON it is
// always an array; a bare string or null is also accepted when reading.
type TagList []string

func (l TagList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(l))
}

func (l *TagList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = nil
		if one != "" {
			*l = TagList{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*l = TagList(many)
	return nil
}

// telegramTags holds the channel's author hashtags and manual overrides.
type telegramTags struct {
	byNorm    map[string]string   // normalized hashtag -> hashtag as written (first one wins)
	order     []string            // normalized hashtags in the order of the tags file
	overrides map[string][]string // exact author name -> extra hashtags (empty = never link this author)
}

func cleanHashtag(s string) string {
	return strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "#")), ".,;:!?)）、，。")
}

// parseHashtags returns every #hashtag in text, without the "#".
func parseHashtags(text string) []string {
	var out []string
	for _, m := range hashtagRe.FindAllStringSubmatch(text, -1) {
		if t := cleanHashtag(m[1]); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// loadTelegramTags reads a text file containing #hashtags (anything else in it is ignored)
// and a JSON object {"author name": "hashtag"} or {"author name": ["hashtag", ...]}.
// A missing file is not an error.
func loadTelegramTags(tagsPath, overridesPath string) (*telegramTags, error) {
	t := &telegramTags{byNorm: map[string]string{}, overrides: map[string][]string{}}
	if b, err := os.ReadFile(tagsPath); err == nil {
		for _, tag := range parseHashtags(string(b)) {
			if k := normName(tag); k != "" {
				if _, ok := t.byNorm[k]; !ok {
					t.byNorm[k] = tag
					t.order = append(t.order, k)
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if b, err := os.ReadFile(overridesPath); err == nil {
		raw := map[string]TagList{}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", overridesPath, err)
		}
		for name, tags := range raw {
			list := []string{}
			for _, tag := range tags {
				if c := cleanHashtag(tag); c != "" {
					list = append(list, c)
				}
			}
			t.overrides[name] = list
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return t, nil
}

// apply sets Telegram (every hashtag of the author, each with its "#") on the rows and
// returns how many rows got at least one. A row gets, in this order: the hashtags of its
// manual override (by exact name; an empty override means the row gets none at all), the
// hashtag equal to its normalized name, and every hashtag equal to an alias that belongs to
// exactly one row (and is not the name hashtag of another row).
func (t *telegramTags) apply(rows []AuthorRow) int {
	if t == nil {
		return 0
	}
	nameTaken := map[string]bool{} // normalized hashtags used by an override or a name match
	suppressed := make([]bool, len(rows))
	got := make([][]string, len(rows)) // normalized hashtags per row, in order
	has := func(i int, k string) bool {
		for _, x := range got[i] {
			if x == k {
				return true
			}
		}
		return false
	}
	for i := range rows {
		rows[i].Telegram = nil
		if tags, ok := t.overrides[rows[i].Name]; ok {
			if len(tags) == 0 {
				suppressed[i] = true
				continue
			}
			for _, tag := range tags {
				k := normName(tag)
				if k == "" || has(i, k) {
					continue
				}
				got[i] = append(got[i], k)
				rows[i].Telegram = append(rows[i].Telegram, "#"+tag)
				nameTaken[k] = true
			}
		}
		if k := normName(rows[i].Name); k != "" {
			if tag, ok := t.byNorm[k]; ok {
				nameTaken[k] = true
				if !has(i, k) {
					got[i] = append(got[i], k)
					rows[i].Telegram = append(rows[i].Telegram, "#"+tag)
				}
			}
		}
	}
	claims := map[string][]int{} // normalized hashtag -> rows matching it by alias
	for i := range rows {
		if suppressed[i] {
			continue
		}
		seen := map[string]bool{}
		for _, al := range rows[i].Alias {
			k := normName(al)
			if k == "" || seen[k] || nameTaken[k] {
				continue
			}
			if _, ok := t.byNorm[k]; ok {
				seen[k] = true
				claims[k] = append(claims[k], i)
			}
		}
	}
	for _, k := range t.order { // file order keeps the result stable
		if idx := claims[k]; len(idx) == 1 && !has(idx[0], k) {
			got[idx[0]] = append(got[idx[0]], k)
			rows[idx[0]].Telegram = append(rows[idx[0]].Telegram, "#"+t.byNorm[k])
		}
	}
	n := 0
	for i := range rows {
		if len(rows[i].Telegram) > 0 {
			n++
		}
	}
	return n
}
