package tunables

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type groupsConfig struct {
	Checkout Feature[siteTargeting] `json:"checkout" tune:"name:New checkout"`
}

func TestAFeatureIsForWhoIsInAnyGroup(t *testing.T) {
	f := Feature[siteTargeting]{Enabled: true}
	if !f.IsEnabled(siteInfo{Country: "us"}) || !f.Targets(nil) {
		t.Error("no groups: for everybody")
	}
	f.Groups = []Group[siteTargeting]{
		{ID: "serbia", Name: "Serbia", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}},
		{ID: "beta", Name: "Beta testers", Targeting: siteTargeting{Groups: Rule[string]{Include: NewSet("beta")}, Country: Rule[string]{Exclude: NewSet("ru")}}},
	}
	for name, tc := range map[string]struct {
		info siteInfo
		want bool
	}{
		"in the first":          {siteInfo{Country: "rs"}, true},
		"in the second":         {siteInfo{Country: "us", Groups: []string{"beta"}}, true},
		"in both":               {siteInfo{Country: "rs", Groups: []string{"beta"}}, true},
		"in none":               {siteInfo{Country: "us"}, false},
		"out of the second":     {siteInfo{Country: "ru", Groups: []string{"beta"}}, false},
		"every rule of a group": {siteInfo{Country: "ru", Groups: []string{"staff"}}, false},
	} {
		if got := f.IsEnabled(tc.info); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
	// a group with nothing in it is for everybody, so the feature is
	f.Groups = append(f.Groups, Group[siteTargeting]{ID: "all"})
	if !f.IsEnabled(siteInfo{Country: "us"}) {
		t.Error("an empty group lets everybody in")
	}
	// off is off, whatever the groups
	f.Enabled = false
	if f.IsEnabled(siteInfo{Country: "rs"}) || !f.Targets(siteInfo{Country: "rs"}) {
		t.Error("switched off: IsEnabled is false, Targets says who is in the groups")
	}
}

func TestTheGroupsAreCheckedAndGetTheirIds(t *testing.T) {
	tn, err := New(groupsConfig{}, WithName("t"), usersFiveSource())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("features.x", nil); err == nil {
		t.Fatal("unknown")
	}
	err = tn.Set("checkout.groups", []Group[siteTargeting]{
		{Name: "No id", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}},
		{ID: "mine", Name: "With one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gs := tn.Get().Checkout.Groups
	if len(gs) != 2 || !groupIDPattern.MatchString(gs[0].ID) || gs[0].ID == gs[1].ID || gs[1].ID != "mine" {
		t.Errorf("a group that has no id is given one: %+v", gs)
	}
	// 51 groups are too many; a name that is too long
	many := make([]Group[siteTargeting], maxGroups+1)
	if err := tn.Set("checkout.groups", many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Name: strings.Repeat("я", maxGroupName+1)}}); err == nil {
		t.Error("a name that is too long")
	}
	if len(tn.Get().Checkout.Groups) != 2 {
		t.Error("what was refused changed nothing")
	}
}

// a snapshot that was taken is not changed by a change that comes after it
func TestTheGroupsOfASnapshotAreNotChanged(t *testing.T) {
	tn, err := New(groupsConfig{}, usersFiveSource(), WithName("t"))
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}}}); err != nil {
		t.Fatal(err)
	}
	before := tn.Get()
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("ua")}}}, {ID: "b"}}); err != nil {
		t.Fatal(err)
	}
	if len(before.Checkout.Groups) != 1 || !before.Checkout.Groups[0].Targeting.Country.Include.Has("rs") || before.Checkout.Groups[0].Targeting.Country.Include.Has("ua") {
		t.Errorf("the snapshot was changed: %+v", before.Checkout.Groups)
	}
}

func TestTheGroupsAreKeptAndLoaded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	given := groupsConfig{}
	given.Checkout.Groups = []Group[siteTargeting]{{Name: "From the program", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}}}
	tn, err := New(given, usersFiveSource(), WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	defaultID := tn.Get().Checkout.Groups[0].ID
	if defaultID != "default-1" {
		t.Fatalf("the group of the program has an id that is the same every time: %q", defaultID)
	}
	// nothing saved: the groups of the program
	if gs := tn.Get().Checkout.Groups; len(gs) != 1 || gs[0].Name != "From the program" {
		t.Errorf("defaults: %+v", gs)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Name: "A"}, {ID: "b", Name: "B", Targeting: siteTargeting{Country: Rule[string]{Exclude: NewSet("ru")}}}}); err != nil {
		t.Fatal(err)
	}
	// a new Tunables on the same file reads them back, in order, with their Rules
	again, err := New(given, usersFiveSource(), WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	gs := again.Get().Checkout.Groups
	if len(gs) != 2 || gs[0].ID != "a" || gs[1].Name != "B" || !gs[1].Targeting.Country.Exclude.Has("ru") {
		t.Errorf("loaded: %+v", gs)
	}
	// no groups, on purpose, is not "nothing saved": it does not bring the groups of the program back
	if err := again.Set("checkout.groups", []Group[siteTargeting]{}); err != nil {
		t.Fatal(err)
	}
	third, err := New(given, usersFiveSource(), WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(third.Get().Checkout.Groups); n != 0 {
		t.Errorf("emptied on purpose: %d groups", n)
	}
	// and Reset is the program's again
	if err := third.Reset("checkout.groups"); err != nil {
		t.Fatal(err)
	}
	if gs := third.Get().Checkout.Groups; len(gs) != 1 || gs[0].ID != defaultID {
		t.Errorf("reset: %+v", gs)
	}
}

func TestAGroupThatWasSavedWrongIsMadeRight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	saved := `{"checkout":{"enabled":true,"groups":[
		{"id":"a","name":"Good","targeting":{"country":{"include":["rs"]}}},
		{"id":"a","name":"Same id","targeting":{"provider":{"exclude":["netscape"]}}},
		{"id":"b c","name":"Bad id"},
		{"name":"No id"}]}}`
	if err := os.WriteFile(path, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	tn, err := New(groupsConfig{}, usersFiveSource(), WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	gs := tn.Get().Checkout.Groups
	if len(gs) != 4 {
		t.Fatalf("%d groups", len(gs))
	}
	seen := map[string]bool{}
	for _, g := range gs {
		if !groupIDPattern.MatchString(g.ID) || seen[g.ID] {
			t.Errorf("an id that is not right: %+v", g)
		}
		seen[g.ID] = true
	}
	if gs[0].ID != "a" || !gs[0].Targeting.Country.Include.Has("rs") {
		t.Errorf("the good one: %+v", gs[0])
	}
	if gs[1].Targeting.Provider.Exclude.Len() != 0 {
		t.Error("a value that is not valid is emptied, the group stays")
	}
}

func TestAnObserverIsToldOfTheChecks(t *testing.T) {
	var mu sync.Mutex
	var got []Check
	tn, err := New(groupsConfig{}, usersFiveSource(), WithName("t"), WithObserver(func(c Check) {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	last := func() Check {
		mu.Lock()
		defer mu.Unlock()
		return got[len(got)-1]
	}
	feature := func() *Feature[siteTargeting] { return &tn.Get().Checkout }
	feature().IsEnabled(siteInfo{})
	if c := last(); c != (Check{Feature: "checkout", Enabled: false}) {
		t.Errorf("switched off: %+v", c)
	}
	if err := tn.Set("checkout.enabled", true); err != nil {
		t.Fatal(err)
	}
	feature().IsEnabled(siteInfo{})
	if c := last(); c != (Check{Feature: "checkout", Enabled: true}) {
		t.Errorf("no groups: %+v", c)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{
		{ID: "serbia", Name: "Serbia", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}},
		{ID: "beta", Targeting: siteTargeting{Groups: Rule[string]{Include: NewSet("beta")}}},
	}); err != nil {
		t.Fatal(err)
	}
	feature().IsEnabled(siteInfo{Country: "rs"})
	if c := last(); c != (Check{Feature: "checkout", Group: "Serbia", Enabled: true}) {
		t.Errorf("a group that let it in: %+v", c)
	}
	feature().IsEnabled(siteInfo{Groups: []string{"beta"}})
	if c := last(); c != (Check{Feature: "checkout", Group: "Group B", Enabled: true}) { // no name: "Group", and the letter of its place
		t.Errorf("a group without a name: %+v", c)
	}
	feature().IsEnabled(siteInfo{Country: "us"})
	if c := last(); c != (Check{Feature: "checkout", Enabled: false}) {
		t.Errorf("in none: %+v", c)
	}
	// a Matcher reports as well, and Targets is a question, not a check
	m, err := NewMatcher[siteTargeting, fullInfo]()
	if err != nil {
		t.Fatal(err)
	}
	n := len(got)
	m.IsEnabled(feature(), &fullInfo{Country: "rs"})
	if len(got) != n+1 || last().Group != "Serbia" {
		t.Errorf("the matcher: %+v", last())
	}
	feature().Targets(siteInfo{Country: "rs"})
	if len(got) != n+1 {
		t.Error("Targets is not reported")
	}
	// a snapshot of the features before the observer had nothing to say is not made
	var plain groupsConfig
	plain.Checkout.IsEnabled(siteInfo{}) // no meta, no observer: nothing to report, nothing to break
	// and a copy of a feature that is kept keeps reporting
	kept := *feature()
	n = len(got)
	kept.IsEnabled(siteInfo{Country: "rs"})
	if len(got) != n+1 {
		t.Error("a copy of a feature reports as the feature does")
	}
}

func TestTheGroupsOfAFeatureInTheJSONOfTheStore(t *testing.T) {
	// what is kept is the plain JSON of the settings: readable, and the same as the API takes
	path := filepath.Join(t.TempDir(), "t.json")
	tn, err := New(groupsConfig{}, usersFiveSource(), WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a", Name: "A", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("ua", "rs")}}}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Checkout struct {
			Groups []struct {
				ID, Name  string
				Targeting struct{ Country struct{ Include []string } }
			}
		}
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if g := back.Checkout.Groups; len(g) != 1 || g[0].Name != "A" || strings.Join(g[0].Targeting.Country.Include, ",") != "rs,ua" {
		t.Errorf("kept: %s", b)
	}
}

func TestAGroupWithNoNameGetsAStandardOne(t *testing.T) {
	tn, err := New(groupsConfig{}, WithName("t"), usersFiveSource())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "a"}, {ID: "b", Name: "Group B"}, {ID: "c", Name: "  "}, {ID: "d", Name: "  Mine  "}, {ID: "e"}}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, g := range tn.Get().Checkout.Groups {
		names = append(names, g.Name)
	}
	// the number of the place, unless a group has that name already; a name is trimmed
	if want := []string{"Group A", "Group B", "Group C", "Mine", "Group E"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names %q, want %q", names, want)
	}
}

func TestTheLettersOfTheGroups(t *testing.T) {
	for n, want := range map[int]string{1: "Group A", 2: "Group B", 26: "Group Z", 27: "Group AA", 28: "Group AB", 52: "Group AZ", 53: "Group BA"} {
		if got := groupLetters(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}
