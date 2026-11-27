package main

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOverrides(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "overrides.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadOverrides(t *testing.T) {
	if o, err := loadOverrides(filepath.Join(t.TempDir(), "none.json")); err != nil || o.count() != 0 {
		t.Fatalf("a missing file must be empty: %+v %v", o, err)
	}
	if o, err := loadOverrides(writeOverrides(t, " \n")); err != nil || o.count() != 0 {
		t.Fatalf("an empty file must be empty: %+v %v", o, err)
	}

	o, err := loadOverrides(writeOverrides(t, `{
		"A": {"telegram": "#Tag", "pawchive": "https://pawchive.pw/fanbox/user/1"},
		"B": {"telegram": ["x", "#y", ""], "pawchive": ""},
		"C": {"telegram": ""},
		"D": {"telegram": []},
		"E": {"pawchive": "patreon/2"},
		"F": {}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := o.Telegram["A"]; len(got) != 1 || got[0] != "Tag" {
		t.Errorf("A telegram = %v", got)
	}
	if got := o.Telegram["B"]; len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("B telegram = %v", got)
	}
	for _, n := range []string{"C", "D"} { // given and empty: the author gets none
		if got, ok := o.Telegram[n]; !ok || len(got) != 0 {
			t.Errorf("%s telegram = %v %v", n, got, ok)
		}
	}
	for _, n := range []string{"E", "F"} { // not given: left to the automatic matching
		if _, ok := o.Telegram[n]; ok {
			t.Errorf("%s must have no telegram override", n)
		}
	}
	if _, ok := o.Pawchive["F"]; ok {
		t.Error("F must have no pawchive override")
	}
	if p := o.Pawchive["A"]; p.None || p.Service != "fanbox" || p.ID != "1" {
		t.Errorf("A pawchive = %+v", p)
	}
	if !o.Pawchive["B"].None {
		t.Errorf("B pawchive = %+v", o.Pawchive["B"])
	}
	if p := o.Pawchive["E"]; p.Service != "patreon" || p.ID != "2" {
		t.Errorf("E pawchive = %+v", p)
	}
	if _, ok := o.Pawchive["C"]; ok {
		t.Error("C must have no pawchive override")
	}
	if o.count() != 4+3 { // telegram: A B C D, pawchive: A B E
		t.Errorf("count = %d", o.count())
	}
}

func TestLoadOverridesRejectsMistakes(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field":   `{"A": {"telegran": "x"}}`,
		"bad creator":     `{"A": {"pawchive": "nonsense"}}`,
		"old flat format": `{"A": "#tag"}`,
		"not an object":   `[1]`,
		"broken json":     `{"A": `,
	} {
		if _, err := loadOverrides(writeOverrides(t, content)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The overrides that were in telegram_overrides.json were moved to overrides.json.
func TestRealOverridesFileLoads(t *testing.T) {
	o, err := loadOverrides(filepath.Join("config", "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Telegram) == 0 {
		t.Skip("no telegram overrides")
	}
	t.Logf("telegram=%d pawchive=%d", len(o.Telegram), len(o.Pawchive))
}
