package tunables

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestRuleMatches(t *testing.T) {
	var none Rule[string]
	if !none.Match("x") || !none.Match("") || !none.MatchAny() || !none.MatchAny("a", "b") {
		t.Error("a rule with nothing in it matches everything")
	}
	only := Rule[string]{Include: NewSet("rs", "ua")}
	if !only.Match("rs") || only.Match("us") || only.Match("") {
		t.Error("Include: only these")
	}
	except := Rule[string]{Exclude: NewSet("ru")}
	if !except.Match("us") || except.Match("ru") {
		t.Error("Exclude: all but these")
	}
	both := Rule[string]{Include: NewSet("rs", "ua"), Exclude: NewSet("ua")}
	if !both.Match("rs") || both.Match("ua") || both.Match("us") {
		t.Error("Exclude wins over Include")
	}
	langs := Rule[string]{Include: NewSet("en"), Exclude: NewSet("ru")}
	for in, want := range map[string]bool{"en": true, "en,de": true, "de": false, "en,ru": false, "ru": false, "": false} {
		var vs []string
		if in != "" {
			vs = strings.Split(in, ",")
		}
		if got := langs.MatchAny(vs...); got != want {
			t.Errorf("MatchAny(%q) = %v, want %v", in, got, want)
		}
	}
}

// a targeting and the info it is checked against: declared by the program, no more than a struct of Rules and a struct with a
// field of the same name for each
type siteInfo struct {
	Country   string
	Languages []string
	Provider  string
	UserID    uint
	Groups    []string
}

type siteTargeting struct {
	Country   Rule[string] `json:"country" tune:"name:Country; desc:Where from"`
	Languages Rule[string] `json:"languages"`
	Provider  Rule[string] `json:"provider" tune:"name:Account provider; enum:google,telegram,email"`
	UserID    Rule[uint]   `json:"users" tune:"name:People; source:users"`
	Groups    Rule[string] `json:"groups"`
}

type moreInfo struct {
	siteInfoBase // an embedded struct: its fields are the info's own
	Provider     string
}

type siteInfoBase struct {
	Country string
}

type featureConfig struct {
	Features struct {
		Checkout Feature[siteTargeting] `json:"checkout" tune:"name:New checkout"`
		Beta     Feature[struct {
			Country Rule[string] `json:"country"`
		}] `json:"beta"`
	} `json:"features"`
}

func TestFeatureIsOnForWhoIsInTheTargeting(t *testing.T) {
	var f Feature[siteTargeting]
	info := siteInfo{Country: "rs", Languages: []string{"sr", "en"}, Provider: "google", UserID: 7}

	if f.IsEnabled(info) {
		t.Error("a feature that is not enabled is off for everybody")
	}
	f.Enabled = true
	if !f.IsEnabled(info) || !f.IsEnabled(siteInfo{}) || !f.IsEnabled(nil) {
		t.Error("enabled with no targeting: on for everybody, whatever is known of them")
	}
	f.Targeting.Country.Include = NewSet("rs", "ua")
	if !f.IsEnabled(info) || f.IsEnabled(siteInfo{Country: "us"}) {
		t.Error("only those countries")
	}
	if f.IsEnabled(siteInfo{}) || f.IsEnabled(nil) {
		t.Error("a country that is not known is not in a list of countries")
	}
	f.Targeting.Provider.Exclude = NewSet("email")
	if !f.IsEnabled(info) || f.IsEnabled(siteInfo{Country: "rs", Provider: "email"}) {
		t.Error("all providers but one")
	}
	if !f.IsEnabled(siteInfo{Country: "rs"}) {
		t.Error("a provider that is not known is not one that is excluded")
	}
	f.Targeting.Languages.Include = NewSet("en")
	if !f.IsEnabled(info) || f.IsEnabled(siteInfo{Country: "rs", Languages: []string{"sr"}}) {
		t.Error("one of the languages has to be in")
	}
	f.Targeting.UserID.Exclude = NewSet[uint](13)
	if !f.IsEnabled(info) || f.IsEnabled(siteInfo{Country: "rs", Languages: []string{"en"}, UserID: 13}) {
		t.Error("a person that is kept out")
	}
	f.Targeting.Groups.Include = NewSet("beta")
	if f.IsEnabled(info) || !f.IsEnabled(siteInfo{Country: "rs", Languages: []string{"en"}, Groups: []string{"staff", "beta"}}) {
		t.Error("one of the groups has to be in")
	}
	if !f.Targets(siteInfo{Country: "rs", Languages: []string{"en"}, Groups: []string{"beta"}}) {
		t.Error("Targets says the same without the switch")
	}
	f.Enabled = false
	if f.IsEnabled(info) || !f.Targets(siteInfo{Country: "rs", Languages: []string{"en"}, Groups: []string{"beta"}}) {
		t.Error("the switch is only IsEnabled's")
	}
	// the info may be a pointer
	f.Enabled = true
	f.Targeting.Groups = Rule[string]{}
	if !f.IsEnabled(&info) || f.IsEnabled((*siteInfo)(nil)) {
		t.Error("a pointer to the info works, a nil one knows nothing")
	}
}

func TestFeatureInfoThatIsExtendedByEmbedding(t *testing.T) {
	var f Feature[struct {
		Country  Rule[string]
		Provider Rule[string]
	}]
	f.Enabled = true
	f.Targeting.Country.Include = NewSet("rs")
	f.Targeting.Provider.Include = NewSet("google")
	if !f.IsEnabled(moreInfo{siteInfoBase{Country: "rs"}, "google"}) || f.IsEnabled(moreInfo{siteInfoBase{Country: "us"}, "google"}) {
		t.Error("the fields of an embedded struct are the info's")
	}
	// the info that cannot answer a Rule of Include does not meet it, but meets one that only keeps somebody out
	if f.IsEnabled(siteInfoBase{Country: "rs"}) {
		t.Error("a Rule of Include that the info cannot answer is not met")
	}
	var g Feature[siteTargeting]
	g.Enabled = true
	g.Targeting.UserID.Exclude = NewSet[uint](13)
	if !g.IsEnabled(siteInfoBase{}) {
		t.Error("an Exclude that the info cannot answer lets everybody in")
	}
}

func TestCheckInfoFindsWhatTheInfoLacks(t *testing.T) {
	if err := CheckInfo[siteTargeting, siteInfo](); err != nil {
		t.Errorf("they go together: %v", err)
	}
	if err := CheckInfo[siteTargeting, *siteInfo](); err != nil {
		t.Errorf("a pointer: %v", err)
	}
	err := CheckInfo[siteTargeting, siteInfoBase]()
	if err == nil || !strings.Contains(err.Error(), "Provider") || !strings.Contains(err.Error(), "UserID") || !strings.Contains(err.Error(), "Groups") {
		t.Errorf("what is missing is named: %v", err)
	}
	type wrong struct {
		Country   string
		Languages []string
		Provider  int
		UserID    string
		Groups    []string
	}
	if err := CheckInfo[siteTargeting, wrong](); err == nil || !strings.Contains(err.Error(), "Provider is int") || !strings.Contains(err.Error(), "UserID is string") {
		t.Errorf("a field of another type: %v", err)
	}
	if err := CheckInfo[siteTargeting, int](); err == nil {
		t.Error("the info is a struct")
	}
}

func TestFeatureInTheSettings(t *testing.T) {
	tn, err := New(featureConfig{}, WithName("t"), WithSource("users", DataSource[uint]{
		Search: func(_ context.Context, q string, limit, offset int) ([]Item[uint], error) { return nil, nil },
		Get: func(_ context.Context, ids []uint) ([]Item[uint], error) {
			var out []Item[uint]
			for _, id := range ids {
				if id == 5 {
					out = append(out, Item[uint]{ID: 5, Name: "five"})
				}
			}
			return out, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	w := do(h, "GET", "/schema", "", nil)
	var out struct {
		Groups []groupDTO
		Fields []fieldDTO
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]fieldDTO{}
	var keys []string
	for _, f := range out.Fields {
		by[f.Key] = f
		keys = append(keys, f.Key)
	}
	// the switch first, then the rules, right in the feature (no "targeting" level)
	if keys[0] != "features.checkout.enabled" || keys[1] != "features.checkout.country.include" || keys[2] != "features.checkout.country.exclude" {
		t.Errorf("keys %v", keys[:4])
	}
	for _, k := range keys {
		if strings.Contains(k, ".targeting.") {
			t.Errorf("%s: the targeting is not a level of its own", k)
		}
	}
	if f := by["features.checkout.enabled"]; f.Type != "bool" || f.Value != false || f.Name != "Enabled" {
		t.Errorf("enabled %+v", f)
	}
	if f := by["features.checkout.country.include"]; f.Type != "set" || f.Item != "string" || f.Name != "Only" {
		t.Errorf("include %+v", f)
	}
	if f := by["features.checkout.country.exclude"]; f.Name != "Except" {
		t.Errorf("exclude %+v", f)
	}
	// the options of a rule's tag are of both its sets
	for _, k := range []string{"features.checkout.provider.include", "features.checkout.provider.exclude"} {
		if f := by[k]; !slices.Equal(f.Enum, []string{"google", "telegram", "email"}) || f.Item != "enum" {
			t.Errorf("%s %+v", k, f)
		}
	}
	for _, k := range []string{"features.checkout.users.include", "features.checkout.users.exclude"} {
		if f := by[k]; f.Source != "users" || f.UI != "picker" || f.Item != "int" {
			t.Errorf("%s %+v", k, f)
		}
	}
	// the groups: the feature, then its rules
	names := map[string]groupDTO{}
	for _, g := range out.Groups {
		names[g.Key] = g
	}
	for key, want := range map[string]struct {
		name          string
		rule, feature bool
	}{
		"features":                   {"Features", false, false},
		"features.checkout":          {"New checkout", false, true},
		"features.checkout.country":  {"Country", true, false},
		"features.checkout.provider": {"Account provider", true, false},
		"features.beta":              {"Beta", false, true},
		"features.beta.country":      {"Country", true, false},
	} {
		if g := names[key]; g.Name != want.name || g.Rule != want.rule || g.Feature != want.feature {
			t.Errorf("group %s: %+v", key, g)
		}
	}
	if _, ok := names["features.checkout.targeting"]; ok {
		t.Error("no group of the targeting")
	}

	// changed through the API, and read as plain Go
	for key, value := range map[string]string{
		"features.checkout.enabled":          `true`,
		"features.checkout.country.include":  `["rs","ua"]`,
		"features.checkout.provider.exclude": `["email"]`,
		"features.checkout.users.exclude":    `[5]`,
	} {
		if w := do(h, "PUT", "/values/"+key, `{"value":`+value+`}`, jsonCT); w.Code != 200 {
			t.Fatalf("%s: %d %s", key, w.Code, w.Body)
		}
	}
	if w := do(h, "PUT", "/values/features.checkout.provider.exclude", `{"value":["netscape"]}`, jsonCT); w.Code != 400 {
		t.Errorf("an enum of the rule is kept: %d", w.Code)
	}
	if w := do(h, "PUT", "/values/features.checkout.users.exclude", `{"value":[99]}`, jsonCT); w.Code != 400 {
		t.Errorf("the source of the rule is asked: %d %s", w.Code, w.Body)
	}
	c := tn.Get().Features.Checkout
	if !c.IsEnabled(siteInfo{Country: "rs", Provider: "google"}) || c.IsEnabled(siteInfo{Country: "rs", Provider: "email"}) || c.IsEnabled(siteInfo{Country: "us"}) || c.IsEnabled(siteInfo{Country: "rs", UserID: 5}) {
		t.Errorf("the feature as it was set: %+v", c)
	}
	if tn.Get().Features.Beta.Enabled {
		t.Error("the other feature is as it was")
	}
	// and back
	if err := tn.Reset("features.checkout.country.include"); err != nil {
		t.Fatal(err)
	}
	if !tn.Get().Features.Checkout.IsEnabled(siteInfo{Country: "us", Provider: "google"}) {
		t.Error("reset: for everybody again")
	}
}

func TestFeaturesFitTheDepthOfTheSettings(t *testing.T) {
	type targeting struct {
		A Rule[string] `json:"a"`
	}
	type top struct {
		F Feature[targeting] `json:"f"`
	}
	type two struct {
		G struct {
			F Feature[targeting] `json:"f"`
		} `json:"g"`
	}
	if _, err := New(top{}); err != nil {
		t.Errorf("on the top: %v", err)
	}
	if _, err := New(two{}); err != nil {
		t.Errorf("in a group: %v", err)
	}
}

func TestFlattenedStruct(t *testing.T) {
	type inner struct {
		B int `json:"b"`
	}
	tn, err := New(struct {
		A     int   `json:"a"`
		Inner inner `json:"inner" tune:"flatten:true"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Fields []fieldDTO }
	if err := json.Unmarshal(do(tn.Handler(), "GET", "/schema", "", nil).Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Fields) != 2 || out.Fields[1].Key != "b" {
		t.Errorf("the fields of the struct are the ones around it: %+v", out.Fields)
	}
	if _, err := New(struct {
		A int `json:"a" tune:"flatten:true"`
	}{}); err == nil || !strings.Contains(err.Error(), "flatten is only for a struct") {
		t.Errorf("flatten on a field: %v", err)
	}
	if _, err := New(struct {
		G inner `json:"g" tune:"flatten:maybe"`
	}{}); err == nil || !strings.Contains(err.Error(), "flatten must be true or false") {
		t.Errorf("flatten with a bad value: %v", err)
	}
}

func TestCollapsedGroups(t *testing.T) {
	type cfg struct {
		Open struct {
			A int `json:"a"`
		} `json:"open" tune:"name:Open"`
		Shut struct {
			B int `json:"b"`
		} `json:"shut" tune:"collapsed:true"`
		Off struct {
			C int `json:"c"`
		} `json:"off" tune:"collapsed:false"`
	}
	tn, err := New(cfg{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Groups []groupDTO }
	if err := json.Unmarshal(do(tn.Handler(), "GET", "/schema", "", nil).Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, g := range out.Groups {
		got[g.Key] = g.Collapsed
	}
	if got["open"] || !got["shut"] || got["off"] {
		t.Errorf("collapsed %v", got)
	}

	if _, err := New(struct {
		A int `json:"a" tune:"collapsed:true"`
	}{}); err == nil || !strings.Contains(err.Error(), "only for a group") {
		t.Errorf("a field is not folded: %v", err)
	}
	if _, err := New(struct {
		G struct{ A int } `json:"g" tune:"collapsed:maybe"`
	}{}); err == nil || !strings.Contains(err.Error(), "collapsed must be true or false") {
		t.Errorf("a bad value: %v", err)
	}
}

var testCountries = []Item[string]{
	{ID: "br", Name: "Brazil"}, {ID: "pe", Name: "Peru"}, {ID: "rs", Name: "Serbia"}, {ID: "se", Name: "Sweden"},
	{ID: "sn", Name: "Senegal"}, {ID: "sc", Name: "Seychelles"}, {ID: "sg", Name: "Singapore"}, {ID: "ru", Name: "Russia"},
}

func TestStaticSourceSearchesRanksPagesAndFinds(t *testing.T) {
	ds := StaticSource(testCountries)
	ctx := context.Background()
	ids := func(items []Item[string]) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}
	// the id typed first, then the names that start with it (in the order of the list), then those that have it inside
	got, _ := ds.Search(ctx, "se", 50, 0)
	if !slices.Equal(ids(got), []string{"se", "rs", "sn", "sc"}) {
		t.Errorf("ranking: %v", ids(got))
	}
	if got, _ = ds.Search(ctx, "r", 50, 0); !slices.Equal(ids(got), []string{"rs", "ru", "br", "pe", "sg"}) { // codes and names that start with it, then those that have it inside
		t.Errorf("starts, then has inside: %v", ids(got))
	}
	if got, _ = ds.Search(ctx, "SERB", 10, 0); len(got) != 1 || got[0].ID != "rs" {
		t.Errorf("by the name, whatever the case: %v", ids(got))
	}
	// all of it, page by page, without repeats
	var all []Item[string]
	for offset := 0; ; offset += 3 {
		page, _ := ds.Search(ctx, "", 3, offset)
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
	}
	if !slices.Equal(ids(all), ids(testCountries)) {
		t.Errorf("paged: %v", ids(all))
	}
	if past, _ := ds.Search(ctx, "", 10, 100); len(past) != 0 {
		t.Errorf("past the end: %v", ids(past))
	}
	found, _ := ds.Get(ctx, []string{"rs", "nope", "sg"})
	if len(found) != 2 || found[0].Name != "Serbia" {
		t.Errorf("get: %v", ids(found))
	}
}

func TestASourceThatAPackageRegisters(t *testing.T) {
	RegisterSource("test-planets", StaticSource([]Item[string]{{ID: "earth", Name: "Earth"}, {ID: "mars", Name: "Mars"}}))
	t.Cleanup(func() {
		registeredMu.Lock()
		delete(registeredSources, "test-planets")
		registeredMu.Unlock()
	})
	type cfg struct {
		Home   Set[string] `json:"home" tune:"source:test-planets"`
		Anyone string      `json:"anyone" tune:"source:test-planets; custom:true"`
	}
	tn, err := New(cfg{})
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	if w := do(h, "GET", "/sources/test-planets/search?q=mar", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Mars"`) {
		t.Errorf("search: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/home", `{"value":["earth"]}`, jsonCT); w.Code != 200 {
		t.Errorf("a planet of the list: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/home", `{"value":["pluto"]}`, jsonCT); w.Code != 400 {
		t.Errorf("a planet that is not in the list: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/anyone", `{"value":"pluto"}`, jsonCT); w.Code != 200 {
		t.Errorf("custom: %d %s", w.Code, w.Body)
	}
	// only what is referred to is there
	plain, err := New(struct{ A bool }{})
	if err != nil {
		t.Fatal(err)
	}
	if w := do(plain.Handler(), "GET", "/sources/test-planets/search", "", nil); w.Code != 404 {
		t.Errorf("not referred to: %d", w.Code)
	}
	// and one of the program's own under the same name wins
	own, err := New(cfg{}, WithSource("test-planets", DataSource[string]{
		Search: func(context.Context, string, int, int) ([]Item[string], error) {
			return []Item[string]{{ID: "zeta", Name: "Zeta"}}, nil
		},
		Get: func(context.Context, []string) ([]Item[string], error) { return nil, nil },
	}))
	if err != nil {
		t.Fatal(err)
	}
	if w := do(own.Handler(), "GET", "/sources/test-planets/search", "", nil); !strings.Contains(w.Body.String(), "Zeta") || strings.Contains(w.Body.String(), "Mars") {
		t.Errorf("its own: %s", w.Body)
	}
	// a name that cannot be a path is a mistake of the package that registers it
	func() {
		defer func() {
			if recover() == nil {
				t.Error("a bad name must not be registered")
			}
		}()
		RegisterSource("a/b", StaticSource(testCountries))
	}()
}

// the info that has a field for everything of siteTargeting and something that is not for targeting
type fullInfo struct {
	Country   string
	Languages []string
	Provider  string
	UserID    uint
	Groups    []string
	Session   string `target:"-"`
}

func TestCheckInfoBothWays(t *testing.T) {
	if err := CheckInfo[siteTargeting, fullInfo](); err != nil {
		t.Errorf("a field that is not for targeting says so: %v", err)
	}
	type extra struct {
		Country   string
		Languages []string
		Provider  string
		UserID    uint
		Groups    []string
		Session   string // no Rule, not marked
		Ip        string
	}
	err := CheckInfo[siteTargeting, extra]()
	if err == nil || !strings.Contains(err.Error(), "extra.Session has no Rule") || !strings.Contains(err.Error(), "extra.Ip has no Rule") || !strings.Contains(err.Error(), `target:"-"`) {
		t.Errorf("fields without a Rule are named: %v", err)
	}
	type hidden struct {
		Country   string `target:"-"`
		Languages []string
		Provider  string
		UserID    uint
		Groups    []string
	}
	if err := CheckInfo[siteTargeting, hidden](); err == nil || !strings.Contains(err.Error(), "tagged target") {
		t.Errorf("a field that is marked not for targeting has a Rule: %v", err)
	}
	// the embedded ones count as the info's own, whatever their type is called
	type both struct {
		siteInfoBase
		Languages []string
		Provider  string
		UserID    uint
		Groups    []string
	}
	if err := CheckInfo[siteTargeting, both](); err != nil {
		t.Errorf("an embedded struct: %v", err)
	}
	// the unexported fields are nobody's
	type quiet struct {
		fullInfo
		note string
	}
	if err := CheckInfo[siteTargeting, quiet](); err != nil {
		t.Errorf("an unexported field: %v", err)
	}
}

func TestMatcherSaysWhatOnSays(t *testing.T) {
	m, err := NewMatcher[siteTargeting, fullInfo]()
	if err != nil {
		t.Fatal(err)
	}
	infos := []fullInfo{
		{},
		{Country: "rs"},
		{Country: "us", Languages: []string{"en"}},
		{Country: "rs", Languages: []string{"sr", "en"}, Provider: "google", UserID: 7},
		{Country: "ua", Languages: []string{"uk"}, Provider: "email", UserID: 13},
		{Country: "rs", Languages: []string{"ru"}, Provider: "telegram", UserID: 5, Groups: []string{"beta"}},
		{Languages: []string{"", "en"}, Groups: []string{"staff"}},
	}
	rules := []siteTargeting{
		{},
		{Country: Rule[string]{Include: NewSet("rs", "ua")}},
		{Country: Rule[string]{Exclude: NewSet("us")}, Provider: Rule[string]{Exclude: NewSet("email")}},
		{Languages: Rule[string]{Include: NewSet("en"), Exclude: NewSet("ru")}},
		{UserID: Rule[uint]{Include: NewSet[uint](7, 5), Exclude: NewSet[uint](5)}},
		{Groups: Rule[string]{Include: NewSet("beta")}, Country: Rule[string]{Include: NewSet("rs")}},
		{Provider: Rule[string]{Include: NewSet("google", "telegram")}},
	}
	for i, tg := range rules {
		f := Feature[siteTargeting]{Enabled: true, Targeting: tg}
		for j, info := range infos {
			info := info
			want := targets(reflect.ValueOf(tg), info) // the reflective one is the reference
			if got := m.Targets(&tg, &info); got != want {
				t.Errorf("rules %d, info %d: matcher %v, reflection %v", i, j, got, want)
			}
			if got := f.IsEnabled(info); got != want {
				t.Errorf("rules %d, info %d: IsEnabled %v, reflection %v", i, j, got, want)
			}
			if got := f.IsEnabled(&info); got != want {
				t.Errorf("rules %d, info %d: IsEnabled(pointer) %v, reflection %v", i, j, got, want)
			}
			if got := m.IsEnabled(&f, &info); got != want {
				t.Errorf("rules %d, info %d: Matcher.IsEnabled %v, reflection %v", i, j, got, want)
			}
		}
		off := f
		off.Enabled = false
		if m.IsEnabled(&off, &infos[1]) || off.IsEnabled(infos[1]) {
			t.Error("a feature that is off is off")
		}
	}
}

func TestMatcherCostsNothingToUse(t *testing.T) {
	m, err := NewMatcher[siteTargeting, fullInfo]()
	if err != nil {
		t.Fatal(err)
	}
	f := Feature[siteTargeting]{Enabled: true}
	f.Targeting.Country.Include = NewSet("rs", "ua")
	f.Targeting.Provider.Exclude = NewSet("email")
	f.Targeting.Languages.Include = NewSet("en")
	f.Targeting.UserID.Exclude = NewSet[uint](13)
	info := fullInfo{Country: "rs", Languages: []string{"sr", "en"}, Provider: "google", UserID: 7}
	if !m.IsEnabled(&f, &info) {
		t.Fatal("it is in")
	}
	if n := testing.AllocsPerRun(1000, func() { m.IsEnabled(&f, &info) }); n != 0 {
		t.Errorf("a check of a Matcher allocates %v times", n)
	}
	// the one that finds its plan by itself: a lookup and, for a value, a copy of the info; the reflection is not done again
	if n := testing.AllocsPerRun(1000, func() { f.IsEnabled(&info) }); n > 1 {
		t.Errorf("a check through a pointer allocates %v times", n)
	}
}

func TestMatcherRefusesWhatItCannotFollow(t *testing.T) {
	type base struct{ Country string }
	type viaPointer struct {
		*base
		Languages []string
		Provider  string
		UserID    uint
		Groups    []string
	}
	if err := CheckInfo[siteTargeting, viaPointer](); err != nil {
		t.Fatalf("the shape is right: %v", err)
	}
	if _, err := NewMatcher[siteTargeting, viaPointer](); err == nil || !strings.Contains(err.Error(), "embedded pointer") {
		t.Errorf("an embedded pointer has no offset: %v", err)
	}
	// ...but Feature.IsEnabled still reads it (by reflection)
	f := Feature[siteTargeting]{Enabled: true}
	f.Targeting.Country.Include = NewSet("rs")
	if !f.IsEnabled(viaPointer{base: &base{Country: "rs"}}) || f.IsEnabled(viaPointer{base: &base{Country: "us"}}) || f.IsEnabled(viaPointer{}) {
		t.Error("an embedded pointer is read")
	}
	if _, err := NewMatcher[siteTargeting, *fullInfo](); err == nil {
		t.Error("the info of a Matcher is the struct, not a pointer to it")
	}
	if _, err := NewMatcher[siteTargeting, siteInfoBase](); err == nil {
		t.Error("an info that lacks what the targeting asks for")
	}
}

func BenchmarkFeatureChecks(b *testing.B) {
	f := Feature[siteTargeting]{Enabled: true}
	f.Targeting.Country.Include = NewSet("rs", "ua")
	f.Targeting.Provider.Exclude = NewSet("email")
	f.Targeting.Languages.Include = NewSet("en")
	f.Targeting.UserID.Exclude = NewSet[uint](13)
	info := fullInfo{Country: "rs", Languages: []string{"sr", "en"}, Provider: "google", UserID: 7}
	m, err := NewMatcher[siteTargeting, fullInfo]()
	if err != nil {
		b.Fatal(err)
	}
	b.Run("matcher", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.IsEnabled(&f, &info)
		}
	})
	b.Run("on-pointer", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.IsEnabled(&info)
		}
	})
	b.Run("on-value", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			f.IsEnabled(info)
		}
	})
	b.Run("reflection-every-time", func(b *testing.B) {
		b.ReportAllocs()
		tv := reflect.ValueOf(f.Targeting)
		for i := 0; i < b.N; i++ {
			targets(tv, info)
		}
	})
}

func TestCombinedSources(t *testing.T) {
	ctx := context.Background()
	extra := StaticSource([]Item[string]{{ID: "xx", Name: "Xanadu"}, {ID: "rs", Name: "Serbia again"}, {ID: "zz", Name: "Zeta"}})
	ds := CombineSources(StaticSource(testCountries), extra)
	ids := func(items []Item[string]) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}
	// the first source's entries first, then the other's, the same id once and from the first
	var all []Item[string]
	for offset := 0; ; offset += 4 {
		page, err := ds.Search(ctx, "", 4, offset)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
	}
	if want := append(ids(testCountries), "xx", "zz"); !slices.Equal(ids(all), want) {
		t.Errorf("paged: %v, want %v", ids(all), want)
	}
	for _, it := range all {
		if it.ID == "rs" && it.Name != "Serbia" {
			t.Errorf("the first source's entry wins: %+v", it)
		}
	}
	if got, _ := ds.Search(ctx, "zeta", 10, 0); !slices.Equal(ids(got), []string{"zz"}) {
		t.Errorf("an entry of the second: %v", ids(got))
	}
	found, _ := ds.Get(ctx, []string{"rs", "xx", "nope"})
	if !slices.Equal(ids(found), []string{"rs", "xx"}) {
		t.Errorf("get: %v", ids(found))
	}
	// a source that fails fails the search
	bad := DataSource[string]{
		Search: func(context.Context, string, int, int) ([]Item[string], error) { return nil, errors.New("down") },
		Get:    func(context.Context, []string) ([]Item[string], error) { return nil, errors.New("down") },
	}
	if _, err := CombineSources(StaticSource(testCountries), bad).Search(ctx, "zzz", 5, 0); err == nil {
		t.Error("a list that is not whole is not shown")
	}
	// it works as a source of a setting: the entries of both can be chosen
	tn, err := New(struct {
		Where Set[string] `json:"where" tune:"source:both"`
	}{}, WithSource("both", ds))
	if err != nil {
		t.Fatal(err)
	}
	if w := do(tn.Handler(), "PUT", "/values/where", `{"value":["rs","xx"]}`, jsonCT); w.Code != 200 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if w := do(tn.Handler(), "PUT", "/values/where", `{"value":["nope"]}`, jsonCT); w.Code != 400 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
