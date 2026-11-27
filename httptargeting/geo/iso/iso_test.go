package iso

import (
	"context"
	"slices"
	"strings"
	"testing"

	tunables "github.com/nikulex/gotunables"
)

func TestTheListsAreSane(t *testing.T) {
	for name, items := range map[string][]tunables.Item[string]{"countries": countryList(), "languages": languageList()} {
		seen := map[string]bool{}
		for _, it := range items {
			if it.ID == "" || it.Name == "" || it.ID != strings.ToLower(it.ID) || len(it.ID) != 2 || seen[it.ID] {
				t.Errorf("%s: bad %+v", name, it)
			}
			seen[it.ID] = true
		}
		if len(items) < 150 {
			t.Errorf("%s: only %d", name, len(items))
		}
	}
	has := func(list []tunables.Item[string], id string) bool {
		return slices.ContainsFunc(list, func(i tunables.Item[string]) bool { return i.ID == id })
	}
	for _, code := range []string{"rs", "ua", "ru", "us", "xk", "de", "id"} {
		if !has(countryList(), code) {
			t.Errorf("no country %s", code)
		}
	}
	for _, code := range []string{"sr", "uk", "ru", "en", "id", "de"} {
		if !has(languageList(), code) {
			t.Errorf("no language %s", code)
		}
	}
}

func TestTheSourcesFindByNameAndId(t *testing.T) {
	ctx := context.Background()
	found, err := Countries().Search(ctx, "serb", 5, 0)
	if err != nil || len(found) == 0 || found[0].ID != "rs" || found[0].Name != "Serbia" {
		t.Errorf("countries: %v, %v", found, err)
	}
	got, err := Countries().Get(ctx, []string{"xk", "ua"})
	if err != nil || len(got) != 2 {
		t.Errorf("a country the geo databases have: %v, %v", got, err)
	}
	got, err = Languages().Get(ctx, []string{"sr", "uk"})
	if err != nil || len(got) != 2 || got[0].Name != "Serbian" || got[1].Name != "Ukrainian" {
		t.Errorf("languages: %v, %v", got, err)
	}
}

// a program adds a country the list does not have, and the rules take both
func TestTheListCanBeAddedTo(t *testing.T) {
	mine := tunables.StaticSource([]tunables.Item[string]{{ID: "ws", Name: "Wonderland"}})
	both := tunables.CombineSources(Countries(), mine)
	for q, id := range map[string]string{"wonder": "ws", "serb": "rs"} {
		found, err := both.Search(context.Background(), q, 5, 0)
		if err != nil || len(found) == 0 || found[0].ID != id {
			t.Errorf("%s: %v, %v", q, found, err)
		}
	}
}
