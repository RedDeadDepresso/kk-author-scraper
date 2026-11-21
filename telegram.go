package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var hashtagRe = regexp.MustCompile(`#([^\s#]+)`)

// telegramTags holds the channel's author hashtags and manual overrides.
type telegramTags struct {
	byNorm    map[string]string // normalized hashtag -> hashtag as written (first one wins)
	overrides map[string]string // exact author name -> hashtag ("" = never link this author)
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
// and a JSON object {"author name": "hashtag"}. A missing file is not an error.
func loadTelegramTags(tagsPath, overridesPath string) (*telegramTags, error) {
	t := &telegramTags{byNorm: map[string]string{}, overrides: map[string]string{}}
	if b, err := os.ReadFile(tagsPath); err == nil {
		for _, tag := range parseHashtags(string(b)) {
			if k := normName(tag); k != "" {
				if _, ok := t.byNorm[k]; !ok {
					t.byNorm[k] = tag
				}
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if b, err := os.ReadFile(overridesPath); err == nil {
		raw := map[string]string{}
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("%s: %w", overridesPath, err)
		}
		for name, tag := range raw {
			t.overrides[name] = cleanHashtag(tag)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return t, nil
}

// apply sets Telegram (the hashtag, with its "#") on the rows and returns how many got one.
// Order: manual override by exact name, then a hashtag equal to the normalized name,
// then (only for rows still without one) a hashtag equal to exactly one row's alias.
func (t *telegramTags) apply(rows []AuthorRow) int {
	if t == nil {
		return 0
	}
	nameTaken := map[string]bool{} // normalized hashtags used by a name match or override
	done := make([]bool, len(rows))
	for i := range rows {
		rows[i].Telegram = ""
		if tag, ok := t.overrides[rows[i].Name]; ok {
			done[i] = true
			if tag != "" {
				rows[i].Telegram = "#" + tag
				nameTaken[normName(tag)] = true
			}
			continue
		}
		if k := normName(rows[i].Name); k != "" {
			if tag, ok := t.byNorm[k]; ok {
				rows[i].Telegram = "#" + tag
				nameTaken[k] = true
				done[i] = true
			}
		}
	}
	claims := map[string][]int{} // normalized hashtag -> rows matching it by alias
	for i := range rows {
		if done[i] {
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
	for k, idx := range claims {
		if len(idx) != 1 {
			continue // ambiguous: several authors share this alias
		}
		if rows[idx[0]].Telegram == "" {
			rows[idx[0]].Telegram = "#" + t.byNorm[k]
		}
	}
	n := 0
	for i := range rows {
		if rows[i].Telegram != "" {
			n++
		}
	}
	return n
}
