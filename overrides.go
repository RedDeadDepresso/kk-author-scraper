package main

// config/overrides.json holds the manual corrections of the automatic matching. It is a JSON object
// keyed by the exact name of a row of authors.json; every value is an object with the fields
// to correct. A field that is missing is left to the automatic matching, a field that is empty
// removes the automatic result:
//
//	{
//	  "Some Author":  { "telegram": ["Tag1", "Tag2"], "pawchive": "https://pawchive.pw/fanbox/user/123" },
//	  "Wrong Match":  { "pawchive": "" },
//	  "No Channel":   { "telegram": [] }
//	}
//
//   - "telegram": a hashtag, or a list of hashtags (with or without the "#"), replacing the
//     automatic ones; "" or [] gives the author none (telegram.go).
//   - "pawchive": a pawchive creator, as a URL or "<service>/<id>", replacing the automatic
//     match; "" removes it (pawchive_overrides.go).
//
// A missing file is not an error. An unknown field or an invalid value is, so that a typo does
// not silently do nothing.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// authorOverride is the value of one author in overrides.json. A pointer tells "not given"
// from "given and empty".
type authorOverride struct {
	Telegram *TagList `json:"telegram,omitempty"`
	Pawchive *string  `json:"pawchive,omitempty"`
}

// overrides are the manual corrections, by exact authors.json row name.
type overrides struct {
	Telegram map[string][]string // hashtags without "#"; an empty list means none
	Pawchive pawchiveOverrides
}

func loadOverrides(path string) (*overrides, error) {
	out := &overrides{Telegram: map[string][]string{}, Pawchive: pawchiveOverrides{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return out, nil
	}
	raw := map[string]authorOverride{}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for name, ov := range raw {
		if ov.Telegram != nil {
			list := []string{}
			for _, tag := range *ov.Telegram {
				if c := cleanHashtag(tag); c != "" {
					list = append(list, c)
				}
			}
			out.Telegram[name] = list
		}
		if ov.Pawchive != nil {
			p, err := parsePawchiveOverride(*ov.Pawchive)
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %w", path, name, err)
			}
			out.Pawchive[name] = p
		}
	}
	return out, nil
}

// count returns how many corrections there are.
func (o *overrides) count() int {
	return len(o.Telegram) + len(o.Pawchive)
}

