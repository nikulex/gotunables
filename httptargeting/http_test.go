package httptargeting

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	tunables "github.com/nikulex/gotunables"
)

func TestParserReadsARequest(t *testing.T) {
	geo := GeoFunc(func(ip netip.Addr) Geo {
		if ip.String() == "93.86.1.1" {
			return Geo{Country: "RS", Region: "Vojvodina", City: "Novi Sad"}
		}
		return Geo{}
	})
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	req := func(remote string, h map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "http://Example.com:8080/x", nil)
		r.RemoteAddr = remote
		r.Header.Set("User-Agent", chrome)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}

	p := NewParser(WithGeo(geo))
	info := p.Parse(req("93.86.1.1:5555", map[string]string{"Accept-Language": "sr-RS,sr;q=0.9,en-US;q=0.8,en;q=0.7"}))
	if info.IP != "93.86.1.1" || info.Country != "rs" || info.Region != "vojvodina" || info.City != "novi sad" {
		t.Errorf("place: %+v", info)
	}
	if info.Host != "example.com" || info.Language != "sr" || !slices.Equal(info.Languages, []string{"sr", "en"}) {
		t.Errorf("host, languages: %+v", info)
	}
	if info.Browser != "chrome" || info.OS != "windows" || info.Device != "desktop" {
		t.Errorf("agent: %+v", info)
	}

	// the connection's address, unless a header of a proxy is trusted: and then only that one
	spoof := map[string]string{"X-Real-IP": "93.86.1.1"}
	if got := p.Parse(req("10.0.0.1:1", spoof)); got.Country != "" || got.IP != "10.0.0.1" {
		t.Errorf("a header nobody was asked to trust is nothing: %+v", got)
	}
	trusting := NewParser(WithGeo(geo), WithClientIPHeader("X-Real-IP"))
	if got := trusting.Parse(req("10.0.0.1:1", spoof)); got.Country != "rs" || got.IP != "93.86.1.1" {
		t.Errorf("the trusted header: %+v", got)
	}
	if got := trusting.Parse(req("10.0.0.1:1", map[string]string{"X-Real-IP": "garbage"})); got.IP != "10.0.0.1" {
		t.Errorf("a header that is not an address: %+v", got)
	}
	if got := p.Parse(req("[::ffff:93.86.1.1]:80", nil)); got.IP != "93.86.1.1" || got.Country != "rs" {
		t.Errorf("an IPv4 in IPv6 clothes: %+v", got)
	}
	// no geo, no languages, nothing: empty, not a failure
	empty := Parse(req("", nil))
	if empty.IP != "" || empty.Country != "" || empty.Language != "" || len(empty.Languages) != 0 || empty.Browser != "chrome" {
		t.Errorf("empty: %+v", empty)
	}
}

func TestTheProvidersAreReplaceable(t *testing.T) {
	var asked []string
	p := NewParser(
		WithGeo(GeoFunc(func(ip netip.Addr) Geo {
			asked = append(asked, ip.String())
			return Geo{Country: "ID", City: "Denpasar"}
		})),
		WithUserAgent(UserAgentFunc(func(ua string) UserAgent { return UserAgent{Browser: "My Browser", OS: "MyOS", Device: "Watch"} })),
	)
	r := httptest.NewRequest("GET", "http://x/", nil)
	r.RemoteAddr = "1.2.3.4:5"
	info := p.Parse(r)
	if info.Country != "id" || info.City != "denpasar" || info.Browser != "my browser" || info.OS != "myos" || info.Device != "watch" || !slices.Equal(asked, []string{"1.2.3.4"}) {
		t.Errorf("%+v %v", info, asked)
	}
	// a provider that is nil is the default one
	if got := NewParser(WithUserAgent(nil)).Parse(r); got.Browser != "" && got.Browser == "my browser" {
		t.Errorf("nil: %+v", got)
	}
}

func TestTheDefaultUserAgentParser(t *testing.T) {
	for _, c := range []struct{ ua, browser, os, device string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "edge", "windows", "desktop"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15", "safari", "macos", "desktop"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0", "firefox", "linux", "desktop"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1", "safari", "ios", "mobile"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0.6723.37 Mobile/15E148 Safari/604.1", "chrome", "ios", "mobile"},
		{"Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36", "samsung browser", "android", "mobile"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "chrome", "chromeos", "desktop"},
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", "googlebot", "", "bot"},
		{"", "", "", ""},
	} {
		got := DefaultUserAgentParser.Parse(c.ua)
		if got.Browser != c.browser || got.OS != c.os || got.Device != c.device {
			t.Errorf("%q\n got %+v\nwant %s %s %s", c.ua, got, c.browser, c.os, c.device)
		}
	}
}

func TestAcceptedLanguages(t *testing.T) {
	for in, want := range map[string][]string{
		"":                             nil,
		"en":                           {"en"},
		"en-US,en;q=0.9,ru;q=0.8":      {"en", "ru"},
		"ru;q=0.5, en;q=0.9, de;q=0.7": {"en", "de", "ru"},
		"*":                            nil,
		"fr;q=0, en":                   {"en"},
		"EN-gb , uk;q=0.8, en;q=0.2":   {"en", "uk"},
		"en;q=abc, de":                 {"en", "de"},
		"de;q=0.5, fr;q=0.5, en;q=0.5": {"de", "fr", "en"}, // equal weights: as they come
		";q=0.9, , en":                 {"en"},
	} {
		if got := acceptedLanguages(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

// ---- the Rules, in a feature, in the settings ----

type features struct {
	Features struct {
		Checkout tunables.Feature[Targeting] `json:"checkout" tune:"name:New checkout"`
	} `json:"features"`
}

// one is the targeting of the only group of a feature (made when there is none yet)
func one[T any](f *tunables.Feature[T]) *T {
	if len(f.Groups) == 0 {
		f.Groups = append(f.Groups, tunables.Group[T]{ID: "g"})
	}
	return &f.Groups[0].Targeting
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == "PUT" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestTheInfoAndTheTargetingGoTogether(t *testing.T) {
	if err := tunables.CheckInfo[Targeting, Info](); err != nil {
		t.Errorf("both ways: %v", err)
	}
	m, err := tunables.NewMatcher[Targeting, Info]()
	if err != nil {
		t.Fatal(err)
	}
	f := tunables.Feature[Targeting]{Enabled: true}
	one(&f).Country.Include = tunables.NewSet("rs")
	one(&f).Device.Exclude = tunables.NewSet("bot")
	one(&f).Languages.Include = tunables.NewSet("en")
	in := Info{Country: "rs", Device: "desktop", Languages: []string{"sr", "en"}, IP: "1.2.3.4", Language: "sr"}
	if !m.IsEnabled(&f, &in) || !f.IsEnabled(in) {
		t.Error("in")
	}
	for name, mutate := range map[string]func(i *Info){
		"another country": func(i *Info) { i.Country = "us" },
		"a bot":           func(i *Info) { i.Device = "bot" },
		"no english":      func(i *Info) { i.Languages = []string{"sr"} },
		"nothing known":   func(i *Info) { *i = Info{} },
	} {
		i := in
		mutate(&i)
		if m.IsEnabled(&f, &i) || f.IsEnabled(i) {
			t.Errorf("%s: must be off", name)
		}
	}
}

func TestExtendedByEmbedding(t *testing.T) {
	type myTargeting struct {
		Targeting
		Provider tunables.Rule[string] `json:"provider" tune:"enum:google,telegram,email"`
		UserID   tunables.Rule[uint]   `json:"users"`
	}
	type myInfo struct {
		Info
		Provider string
		UserID   uint
	}
	m, err := tunables.NewMatcher[myTargeting, myInfo]()
	if err != nil {
		t.Fatal(err)
	}
	f := tunables.Feature[myTargeting]{Enabled: true}
	one(&f).Country.Include = tunables.NewSet("rs")
	one(&f).Provider.Include = tunables.NewSet("telegram")
	one(&f).UserID.Exclude = tunables.NewSet[uint](13)
	ok := myInfo{Info: Info{Country: "rs"}, Provider: "telegram", UserID: 7}
	if !m.IsEnabled(&f, &ok) {
		t.Error("the base rules and the new ones together")
	}
	for _, bad := range []myInfo{{Info: Info{Country: "us"}, Provider: "telegram", UserID: 7}, {Info: Info{Country: "rs"}, Provider: "email", UserID: 7}, {Info: Info{Country: "rs"}, Provider: "telegram", UserID: 13}} {
		bad := bad
		if m.IsEnabled(&f, &bad) {
			t.Errorf("%+v must be off", bad)
		}
	}
	// the info of the base only does not have what the new Rules are for: the program is told when it starts
	if _, err := tunables.NewMatcher[myTargeting, Info](); err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Errorf("a mismatch: %v", err)
	}
	// the settings page has all of them in one feature
	tn, err := tunables.New(struct {
		F tunables.Feature[myTargeting] `json:"f"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Fields    []struct{ Key string }
		GroupSets []struct {
			Fields []struct{ Key string }
		}
	}
	if err := json.Unmarshal(do(tn.Handler(), "GET", "/schema", "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range out.Fields {
		keys = append(keys, f.Key)
	}
	for _, f := range out.GroupSets[0].Fields { // the rules are in the groups: one place for all of them
		keys = append(keys, "f."+f.Key)
	}
	for _, want := range []string{"f.enabled", "f.country.include", "f.device.exclude", "f.provider.include", "f.users.exclude"} {
		if !slices.Contains(keys, want) {
			t.Errorf("%s is not in %v", want, keys)
		}
	}
}

func TestTheListsTheRulesPickFrom(t *testing.T) {
	tn, err := tunables.New(features{})
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	var out struct {
		GroupSets []struct {
			Fields []struct {
				Key, Source, UI, Item string
				Custom                bool
			}
		}
	}
	if err := json.Unmarshal(do(h, "GET", "/schema", "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]struct {
		Key, Source, UI, Item string
		Custom                bool
	}{}
	for _, f := range out.GroupSets[0].Fields { // the rules of a group, by their keys in it
		by[f.Key] = f
	}
	for rule, want := range map[string]struct {
		source string
		custom bool
	}{
		"country": {"countries", true}, "region": {"regions", true}, "city": {"cities", true}, "languages": {"languages", true}, "browser": {"browsers", true}, "os": {"systems", true}, "device": {"devices", false},
	} {
		for _, side := range []string{"include", "exclude"} {
			f := by[rule+"."+side]
			if f.Source != want.source || f.Custom != want.custom || f.UI != "picker" || f.Item != "string" {
				t.Errorf("%s.%s: %+v", rule, side, f)
			}
		}
	}
	if f := by["host.include"]; f.Source != "" || f.UI != "chips" { // typed
		t.Errorf("host: %+v", f)
	}

	get := func(path string) string {
		w := do(h, "GET", path, "")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	if body := get("/sources/browsers/search?q=chrome"); !strings.Contains(body, `"id":"chrome"`) {
		t.Errorf("browsers: %s", body)
	}
	if body := get("/sources/systems/items?id=ios&id=macos"); !strings.Contains(body, `"id":"ios"`) || !strings.Contains(body, `"id":"macos"`) {
		t.Errorf("systems: %s", body)
	}
	if body := get("/sources/devices/search"); strings.Count(body, `"id"`) != 4 {
		t.Errorf("devices: %s", body)
	}

	// a code the list does not have: a country may be added by hand (custom), a device may not
	if w := do(h, "PUT", "/values/features.checkout.groups", `{"value":[{"id":"a","targeting":{"country":{"include":["rs","zz"]}}}]}`); w.Code != 200 {
		t.Errorf("custom: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/features.checkout.groups", `{"value":[{"id":"a","targeting":{"device":{"include":["watch"]}}}]}`); w.Code != 400 {
		t.Errorf("a device that is not in the list: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/features.checkout.groups", `{"value":[{"id":"a","targeting":{"device":{"include":["tablet"]}}}]}`); w.Code != 200 {
		t.Errorf("a device of the list: %d %s", w.Code, w.Body)
	}

	// a source of the program's own under the same name replaces the list
	own, err := tunables.New(features{}, tunables.WithSource("devices", tunables.StaticSource([]tunables.Item[string]{{ID: "watch", Name: "Watch"}})))
	if err != nil {
		t.Fatal(err)
	}
	if w := do(own.Handler(), "PUT", "/values/features.checkout.groups", `{"value":[{"id":"a","targeting":{"device":{"include":["watch"]}}}]}`); w.Code != 200 {
		t.Errorf("its own list: %d %s", w.Code, w.Body)
	}
}

func TestTheListsAreSane(t *testing.T) {
	has := func(list []tunables.Item[string], id string) bool {
		return slices.ContainsFunc(list, func(i tunables.Item[string]) bool { return i.ID == id })
	}
	// what the parser says of ordinary browsers is in the lists of the browsers and the systems
	browsers := namedList("Chrome", "Firefox", "Safari", "Edge", "Opera")
	for _, ua := range []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0",
	} {
		if got := DefaultUserAgentParser.Parse(ua); !has(browsers, got.Browser) {
			t.Errorf("%q: %+v is not in the list", ua, got)
		}
	}
}

func TestTheStandardListsCanBeAddedTo(t *testing.T) {
	// a program that has a browser that the list does not: the list and its own, together, under the same name
	tn, err := tunables.New(features{},
		tunables.WithSource("browsers", tunables.CombineSources(Browsers(), tunables.StaticSource([]tunables.Item[string]{{ID: "yandex browser", Name: "Yandex Browser"}}))))
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	w := do(h, "GET", "/sources/browsers/items?id=yandex+browser&id=chrome", "")
	if body := w.Body.String(); w.Code != 200 || !strings.Contains(body, "Yandex Browser") || !strings.Contains(body, `"id":"chrome"`) {
		t.Errorf("browsers: %d %s", w.Code, body)
	}
	// both can be chosen
	if w := do(h, "PUT", "/values/features.checkout.groups", `{"value":[{"id":"a","targeting":{"browser":{"include":["yandex browser","chrome"]}}}]}`); w.Code != 200 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
