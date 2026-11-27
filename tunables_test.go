package tunables

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sharedPart struct {
	Shared int `json:"shared" tune:"default:7"`
}

type testConfig struct {
	sharedPart
	Plain   bool   `json:"plain"`
	NoTag   string // key "notag"
	Skipped string `json:"-"`
	Hidden  int    `json:"hidden" tune:"-"`
	Yamled  int    `yaml:"yamled"`
	Both    int    `tune:"key:win" json:"lose"`
	Group   struct {
		Color string        `json:"color" tune:"name:Button color; default:blue; enum:blue,green"`
		Max   int           `json:"max" tune:"default:10; min:1; max:100; desc:Cart limit"`
		Wait  time.Duration `json:"wait" tune:"default:500ms; min:10ms; max:1m"`
		Ratio float64       `json:"ratio" tune:"min:0; max:1"`
		Small int8          `json:"small"`
		U     uint          `json:"u"`
	} `json:"group" tune:"name:The group; desc:About the group"`
}

func newTest(t *testing.T, opts ...Option) *Tunables[testConfig] {
	t.Helper()
	tn, err := New(testConfig{Plain: true}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func TestSchemaKeysTagsAndDefaults(t *testing.T) {
	tn := newTest(t)
	got := map[string]*field{}
	for _, f := range tn.c.fields {
		got[f.key] = f
	}
	for _, k := range []string{"shared", "plain", "notag", "yamled", "win", "group.color", "group.max", "group.wait", "group.ratio", "group.small", "group.u"} {
		if got[k] == nil {
			t.Errorf("missing key %q", k)
		}
	}
	for _, k := range []string{"skipped", "hidden", "lose", "Skipped"} {
		if got[k] != nil {
			t.Errorf("key %q must not exist", k)
		}
	}
	if f := got["group.color"]; f.name != "Button color" || f.kind != kindEnum {
		t.Errorf("color: %+v", f)
	}
	if got["group.max"].name != "Max" || got["notag"].name != "Notag" || got["yamled"].name != "Yamled" {
		t.Errorf("a name that is not set is the key segment with a capital: %q %q %q", got["group.max"].name, got["notag"].name, got["yamled"].name)
	}
	if f := got["group.max"]; f.desc != "Cart limit" || *f.min != 1 || *f.max != 100 {
		t.Errorf("max: %+v", f)
	}
	if len(tn.c.groups) != 1 || tn.c.groups[0].key != "group" || tn.c.groups[0].name != "The group" || tn.c.groups[0].desc != "About the group" {
		t.Errorf("groups: %+v", tn.c.groups)
	}
	c := tn.Get()
	if !c.Plain {
		t.Error("initial value must be the default")
	}
	if c.Shared != 7 || c.Group.Color != "blue" || c.Group.Max != 10 || c.Group.Wait != 500*time.Millisecond {
		t.Errorf("tune defaults not applied: %+v", c)
	}
}

func TestHumanize(t *testing.T) {
	for in, want := range map[string]string{
		"max_items": "Max items", "backoff": "Backoff", "api-url": "Api url", "already Big": "Already Big",
		"ёлка": "Ёлка", "": "", "_x": "X",
	} {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnnamedGroupGetsADefaultName(t *testing.T) {
	type cfg struct {
		Cart struct {
			Max_items int `json:"max_items"`
		} `json:"shopping_cart"`
	}
	tn, err := New(cfg{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tn.c.groups) != 1 || tn.c.groups[0].name != "Shopping cart" || tn.c.fields[0].name != "Max items" {
		t.Errorf("groups %+v, field %q", tn.c.groups, tn.c.fields[0].name)
	}
}

func TestNewRejectsBadSchemas(t *testing.T) {
	type slice struct{ X []struct{ A int } }
	type badEnum struct {
		X int `tune:"enum:a,b"`
	}
	type badDefault struct {
		X int `tune:"default:abc"`
	}
	type outOfRange struct {
		X int `tune:"default:500; max:100"`
	}
	type dup struct {
		A int `json:"x"`
		B int `tune:"key:x"`
	}
	type dot struct {
		A int `json:"a.b"`
	}
	type unknownOpt struct {
		A int `tune:"nme:Typo"`
	}
	type noColon struct {
		A int `tune:"name"`
	}
	type minOnString struct {
		A string `tune:"min:1"`
	}
	for name, v := range map[string]any{
		"slice":       slice{},
		"enum on int": badEnum{},
		"bad default": badDefault{},
		"range":       outOfRange{},
		"duplicate":   dup{},
		"dot":         dot{},
		"unknown opt": unknownOpt{},
		"no colon":    noColon{},
		"min string":  minOnString{},
		"non-struct":  5,
	} {
		var err error
		switch x := v.(type) {
		case slice:
			_, err = New(x)
		case badEnum:
			_, err = New(x)
		case badDefault:
			_, err = New(x)
		case outOfRange:
			_, err = New(x)
		case dup:
			_, err = New(x)
		case dot:
			_, err = New(x)
		case unknownOpt:
			_, err = New(x)
		case noColon:
			_, err = New(x)
		case minOnString:
			_, err = New(x)
		case int:
			_, err = New(x)
		}
		if err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func TestSetValidatesAndSwapsSnapshot(t *testing.T) {
	tn := newTest(t)
	before := tn.Get()

	if err := tn.Set("group.max", 50); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("group.wait", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("group.color", "green"); err != nil {
		t.Fatal(err)
	}
	after := tn.Get()
	if after.Group.Max != 50 || after.Group.Wait != 2*time.Second || after.Group.Color != "green" {
		t.Errorf("not applied: %+v", after.Group)
	}
	if before.Group.Max != 10 {
		t.Error("an old snapshot must not change")
	}

	bad := []struct {
		key string
		val any
	}{
		{"group.color", "red"},
		{"group.max", 0},
		{"group.max", 101},
		{"group.max", 1.5},
		{"group.max", "ten"},
		{"group.max", nil},
		{"group.wait", "soon"},
		{"group.wait", 5 * time.Hour},
		{"group.ratio", 2.0},
		{"group.small", 300},
		{"group.u", -1},
		{"plain", "yes"},
	}
	for _, b := range bad {
		if err := tn.Set(b.key, b.val); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("Set(%q, %v) = %v, want ErrInvalidValue", b.key, b.val, err)
		}
	}
	if err := tn.Set("nope", 1); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key: %v", err)
	}
	if tn.Get().Group.Max != 50 {
		t.Error("a rejected value must not change anything")
	}

	if err := tn.Reset("group.max"); err != nil {
		t.Fatal(err)
	}
	if tn.Get().Group.Max != 10 {
		t.Error("reset must restore the default")
	}
}

func TestFileStoreSavesTheStruct(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	// Only some fields are saved; an invalid one (color) must fall back, the missing ones keep their defaults.
	if err := os.WriteFile(path, []byte(`{"group": {"max": 33, "color": "purple"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tn := newTest(t, WithFile(path))
	c := tn.Get()
	if c.Group.Max != 33 {
		t.Errorf("saved value not loaded: %d", c.Group.Max)
	}
	if c.Group.Color != "blue" {
		t.Errorf("an invalid saved value must fall back to the default, got %q", c.Group.Color)
	}
	if c.Shared != 7 || c.Group.Wait != 500*time.Millisecond || !c.Plain {
		t.Errorf("fields missing from the file must keep their defaults: %+v", c)
	}

	if err := tn.Set("group.wait", "2s"); err != nil {
		t.Fatal(err)
	}
	again := newTest(t, WithFile(path))
	if again.Get().Group.Wait != 2*time.Second || again.Get().Group.Max != 33 {
		t.Errorf("not persisted: %+v", again.Get().Group)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved testConfig
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatalf("the file must be the struct itself: %v\n%s", err, b)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(testConfig{}, WithFile(path)); err == nil {
		t.Error("a corrupt file must fail New, not silently reset the settings")
	}
}

type failingStore struct{}

func (failingStore) Load(any) error { return nil }
func (failingStore) Save(any) error { return errors.New("disk full") }

func TestFailedSaveChangesNothing(t *testing.T) {
	tn := newTest(t, WithStore(failingStore{}))
	if err := tn.Set("group.max", 50); err == nil {
		t.Fatal("want a save error")
	}
	if tn.Get().Group.Max != 10 {
		t.Error("a failed save must not change the live value")
	}
}

// A custom Store sees plain *T values, whatever format it uses.
type memStore struct{ saved *testConfig }

func (m *memStore) Load(dst any) error {
	if m.saved != nil {
		*(dst.(*testConfig)) = *m.saved
	}
	return nil
}
func (m *memStore) Save(src any) error {
	c := *(src.(*testConfig))
	m.saved = &c
	return nil
}

func TestCustomStoreGetsTheStruct(t *testing.T) {
	m := &memStore{}
	tn := newTest(t, WithStore(m))
	if err := tn.Set("group.max", 42); err != nil {
		t.Fatal(err)
	}
	if m.saved == nil || m.saved.Group.Max != 42 {
		t.Fatalf("Save must receive the struct: %+v", m.saved)
	}
	if again := newTest(t, WithStore(m)); again.Get().Group.Max != 42 {
		t.Error("Load must get the saved struct back")
	}
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var jsonCT = map[string]string{"Content-Type": "application/json"}

func TestHandlerAPI(t *testing.T) {
	tn := newTest(t, WithName("shop"), WithDescription("d"))
	h := tn.Handler()

	if w := do(h, "GET", "/", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>shop · tunables</title>") {
		t.Errorf("ui: %d", w.Code)
	}

	w := do(h, "GET", "/meta", "", nil)
	var meta map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["name"] != "shop" || meta["description"] != "d" || meta["protocol"] != float64(1) {
		t.Errorf("meta: %v", meta)
	}

	w = do(h, "GET", "/schema", "", nil)
	var schema struct {
		Groups []groupDTO
		Fields []fieldDTO
	}
	if err := json.Unmarshal(w.Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Groups) != 1 || schema.Groups[0].Name != "The group" {
		t.Errorf("groups: %+v", schema.Groups)
	}
	byKey := map[string]fieldDTO{}
	for _, f := range schema.Fields {
		byKey[f.Key] = f
	}
	wait := byKey["group.wait"]
	if wait.Type != "duration" || wait.Value != "500ms" || wait.Min != "10ms" || wait.Max != "1m0s" {
		t.Errorf("duration field: %+v", wait)
	}
	if c := byKey["group.color"]; c.Type != "enum" || len(c.Enum) != 2 || c.Name != "Button color" {
		t.Errorf("enum field: %+v", c)
	}
	if u := byKey["group.u"]; u.Type != "int" {
		t.Errorf("uint must be reported as int: %+v", u)
	}

	w = do(h, "PUT", "/values/group.max", `{"value": 42}`, jsonCT)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	var got fieldDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Value != float64(42) || !got.Modified || tn.Get().Group.Max != 42 {
		t.Errorf("put result: %+v", got)
	}

	if w := do(h, "PUT", "/values/group.max", `{"value": 1000}`, jsonCT); w.Code != 400 {
		t.Errorf("out of range: %d", w.Code)
	}
	if w := do(h, "PUT", "/values/nope", `{"value": 1}`, jsonCT); w.Code != 404 {
		t.Errorf("unknown key: %d", w.Code)
	}
	if w := do(h, "PUT", "/values/group.max", `{}`, jsonCT); w.Code != 400 {
		t.Errorf("missing value: %d", w.Code)
	}
	if w := do(h, "PUT", "/values/group.max", `{"value": 5}`, nil); w.Code != 415 {
		t.Errorf("content type: %d", w.Code)
	}

	w = do(h, "DELETE", "/values/group.max", "", nil)
	if w.Code != 200 || tn.Get().Group.Max != 10 {
		t.Errorf("delete: %d, max=%d", w.Code, tn.Get().Group.Max)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Modified {
		t.Errorf("a value equal to its default is not modified: %+v (%v)", got, err)
	}
}

func TestHandlerOriginAndToken(t *testing.T) {
	tn := newTest(t, WithToken("s3cret"))
	h := tn.Handler()
	auth := map[string]string{"Authorization": "Bearer s3cret", "Content-Type": "application/json"}

	if w := do(h, "GET", "/schema", "", nil); w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	if w := do(h, "GET", "/schema", "", map[string]string{"Authorization": "Bearer nope"}); w.Code != 401 {
		t.Errorf("wrong token: %d", w.Code)
	}
	if w := do(h, "GET", "/", "", nil); w.Code != 200 {
		t.Errorf("the UI page must stay public: %d", w.Code)
	}
	if w := do(h, "PUT", "/values/group.max", `{"value": 5}`, auth); w.Code != 200 {
		t.Errorf("with token: %d", w.Code)
	}

	cross := map[string]string{"Authorization": "Bearer s3cret", "Content-Type": "application/json", "Origin": "https://evil.example"}
	if w := do(h, "PUT", "/values/group.max", `{"value": 6}`, cross); w.Code != 403 {
		t.Errorf("cross-origin write: %d", w.Code)
	}
	same := map[string]string{"Authorization": "Bearer s3cret", "Content-Type": "application/json", "Origin": "http://example.com"}
	if w := do(h, "PUT", "/values/group.max", `{"value": 6}`, same); w.Code != 200 { // httptest uses Host example.com
		t.Errorf("same-origin write: %d", w.Code)
	}
}

// bus is an in-memory "database" shared by several Tunables, with the instant sync of a real Store.
type bus struct {
	mu       sync.Mutex
	saved    *testConfig
	watchers map[int]func()
}

// busStore is one instance's handle on the bus.
type busStore struct {
	b  *bus
	id int
}

func newBus() *bus { return &bus{watchers: map[int]func(){}} }

func (s *busStore) Load(dst any) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.b.saved != nil {
		*(dst.(*testConfig)) = *s.b.saved
	}
	return nil
}

func (s *busStore) Save(src any) error {
	c := *(src.(*testConfig))
	s.b.mu.Lock()
	s.b.saved = &c
	var others []func()
	for id, w := range s.b.watchers {
		if id != s.id {
			others = append(others, w)
		}
	}
	s.b.mu.Unlock()
	for _, w := range others {
		w()
	}
	return nil
}

func (s *busStore) Watch(onChange func()) (func(), error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.watchers[s.id] = onChange
	return func() {
		s.b.mu.Lock()
		defer s.b.mu.Unlock()
		delete(s.b.watchers, s.id)
	}, nil
}

func TestInstantSyncBetweenInstances(t *testing.T) {
	b := newBus()
	one := newTest(t, WithStore(&busStore{b: b, id: 1}))
	two := newTest(t, WithStore(&busStore{b: b, id: 2}))

	if err := one.Set("group.max", 55); err != nil {
		t.Fatal(err)
	}
	if got := two.Get().Group.Max; got != 55 {
		t.Errorf("the other instance must see the change at once, got %d", got)
	}
	if err := two.Set("group.color", "green"); err != nil {
		t.Fatal(err)
	}
	if got := one.Get(); got.Group.Color != "green" || got.Group.Max != 55 {
		t.Errorf("sync must work both ways and keep earlier changes: %+v", got.Group)
	}
	if err := one.Reset("group.max"); err != nil {
		t.Fatal(err)
	}
	if two.Get().Group.Max != 10 {
		t.Error("a reset must sync too")
	}

	two.Close()
	if err := one.Set("group.max", 99); err != nil {
		t.Fatal(err)
	}
	if two.Get().Group.Max != 10 {
		t.Error("a closed instance must stop following")
	}
	two.Close() // idempotent
}

type noWatchStore struct{ busStore }

func (*noWatchStore) Watch(func()) (func(), error) { return nil, errors.New("no listener") }

func TestWatchFailureFailsNew(t *testing.T) {
	if _, err := New(testConfig{}, WithStore(&noWatchStore{busStore{b: newBus()}})); err == nil {
		t.Error("when instant sync cannot start, New must say so instead of running without it")
	}
}

func TestPagePreloadsTheValues(t *testing.T) {
	tn := newTest(t, WithName("shop"), WithDescription("the <b>shop</b>"))
	if err := tn.Set("group.max", 42); err != nil {
		t.Fatal(err)
	}
	body := do(tn.Handler(), "GET", "/", "", nil).Body.String()

	for _, want := range []string{
		`<span class="bar-title" id="name">shop</span>`,
		`<h1 class="hero-title" id="heroName">shop</h1>`,
		`<p class="hero-desc" id="desc">the &lt;b&gt;shop&lt;/b&gt;</p>`, // escaped
		`"key":"group.max"`, `"value":42`, // the current value is in the page
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page must contain %s", want)
		}
	}
	if strings.Contains(body, `<script type="application/json" id="boot">null</script>`) {
		t.Error("the boot placeholder must be filled in")
	}
}

func TestPageTrimsTheDescription(t *testing.T) {
	tn := newTest(t, WithName("shop"), WithDescription("\nLine one\nLine two\n"))
	body := do(tn.Handler(), "GET", "/", "", nil).Body.String()
	if !strings.Contains(body, "<p class=\"hero-desc\" id=\"desc\">Line one\nLine two</p>") {
		t.Error("the description keeps its inner line breaks and loses the outer ones")
	}
}

func TestPageCannotBeBrokenOutOf(t *testing.T) {
	tn := newTest(t, WithName(`</script><img src=x onerror=alert(1)>`), WithDescription(`</script><script>alert(1)</script>`))
	body := do(tn.Handler(), "GET", "/", "", nil).Body.String()
	if strings.Contains(body, "<img src=x") || strings.Contains(body, "<script>alert(1)") {
		t.Error("the name and the description must be escaped in the page and in the embedded JSON")
	}
}

func TestPageWithATokenIsBare(t *testing.T) {
	tn := newTest(t, WithName("secret-shop"), WithToken("s3cret"))
	if err := tn.Set("group.max", 77); err != nil {
		t.Fatal(err)
	}
	body := do(tn.Handler(), "GET", "/", "", nil).Body.String()
	for _, leak := range []string{"secret-shop", "group.max", "77", "s3cret"} {
		if strings.Contains(body, `"`+leak+`"`) || strings.Contains(body, "shop ·") {
			t.Errorf("the public page must not carry %q when a token is set", leak)
		}
	}
	if !strings.Contains(body, `id="boot">null</script>`) {
		t.Error("without data the page must ask for the token and load by itself")
	}
}

func TestUIComponents(t *testing.T) {
	type cfg struct {
		Flag   bool          `json:"flag"`
		Check  bool          `json:"check" tune:"ui:checkbox"`
		Pick   string        `json:"pick" tune:"enum:a,b; ui:buttons"`
		Count  int           `json:"count"`
		Volume int           `json:"volume" tune:"min:0; max:10; ui:slider"`
		Ratio  float64       `json:"ratio" tune:"min:0; max:1; ui:slider"`
		Plain  int           `json:"plain" tune:"ui:input"`
		Name   string        `json:"name"`
		Notes  string        `json:"notes" tune:"ui:textarea"`
		Wait   time.Duration `json:"wait"`
	}
	tn, err := New(cfg{Pick: "a"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range tn.c.fields {
		got[f.key] = f.ui
	}
	want := map[string]string{
		"flag": "switch", "check": "checkbox", "pick": "buttons", "count": "counter", "volume": "slider",
		"ratio": "slider", "plain": "input", "name": "input", "notes": "textarea", "wait": "input",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: ui %q, want %q", k, got[k], w)
		}
	}

	type slider struct {
		A int `tune:"ui:slider"`
	}
	type buttonsOnString struct {
		A string `tune:"ui:buttons"`
	}
	type textareaOnInt struct {
		A int `tune:"ui:textarea"`
	}
	type unknownUI struct {
		A int `tune:"ui:dial"`
	}
	if _, err := New(slider{}); err == nil {
		t.Error("a slider without min and max must be rejected")
	}
	if _, err := New(buttonsOnString{}); err == nil {
		t.Error("buttons are for enums only")
	}
	if _, err := New(textareaOnInt{}); err == nil {
		t.Error("textarea is for strings only")
	}
	if _, err := New(unknownUI{}); err == nil {
		t.Error("an unknown component must be rejected")
	}
}

// Sibling groups must not add up: each one starts from the level of its parent.
func TestSiblingGroupsDoNotAddUpDepth(t *testing.T) {
	type leaf struct {
		X int `json:"x"`
	}
	type cfg struct {
		A leaf `json:"a"`
		B leaf `json:"b"`
		C leaf `json:"c"`
		D struct {
			E struct {
				F leaf `json:"f"`
			} `json:"e"`
		} `json:"d"`
	}
	tn, err := New(cfg{})
	if err != nil {
		t.Fatalf("siblings at level 1 followed by a 3-level chain must work: %v", err)
	}
	if len(tn.c.groups) != 6 {
		t.Errorf("groups: %d", len(tn.c.groups))
	}
}

func TestNestingIsLimitedToThreeLevels(t *testing.T) {
	type three struct {
		A struct {
			B struct {
				C struct {
					X int `json:"x"`
				} `json:"c"`
			} `json:"b"`
		} `json:"a"`
	}
	type four struct {
		A struct {
			B struct {
				C struct {
					D struct {
						X int `json:"x"`
					} `json:"d"`
				} `json:"c"`
			} `json:"b"`
		} `json:"a"`
	}
	tn, err := New(three{})
	if err != nil {
		t.Fatalf("3 levels must work: %v", err)
	}
	if tn.c.fields[0].key != "a.b.c.x" || len(tn.c.groups) != 3 {
		t.Errorf("key %q, groups %d", tn.c.fields[0].key, len(tn.c.groups))
	}
	if _, err := New(four{}); err == nil {
		t.Error("4 levels must be rejected")
	}
}

func TestFloatPrecision(t *testing.T) {
	type cfg struct {
		Ratio float64 `json:"ratio" tune:"default:0.2; min:0; max:1; ui:slider; precision:2"`
		Price float64 `json:"price"`
		Count int     `json:"count"`
	}
	tn, err := New(cfg{})
	if err != nil {
		t.Fatal(err)
	}
	var schema struct{ Fields []fieldDTO }
	if err := json.Unmarshal(do(tn.Handler(), "GET", "/schema", "", nil).Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	got := map[string]*int{}
	for _, f := range schema.Fields {
		got[f.Key] = f.Precision
	}
	if got["ratio"] == nil || *got["ratio"] != 2 {
		t.Errorf("ratio must carry precision 2: %v", got["ratio"])
	}
	if got["price"] != nil || got["count"] != nil {
		t.Error("a field without the option has no precision in the schema")
	}

	type onInt struct {
		A int `tune:"precision:2"`
	}
	type onString struct {
		A string `tune:"precision:2"`
	}
	type negative struct {
		A float64 `tune:"precision:-1"`
	}
	type tooMany struct {
		A float64 `tune:"precision:11"`
	}
	type notANumber struct {
		A float64 `tune:"precision:many"`
	}
	for name, err := range map[string]error{
		"int":        func() error { _, err := New(onInt{}); return err }(),
		"string":     func() error { _, err := New(onString{}); return err }(),
		"negative":   func() error { _, err := New(negative{}); return err }(),
		"too many":   func() error { _, err := New(tooMany{}); return err }(),
		"not number": func() error { _, err := New(notANumber{}); return err }(),
	} {
		if err == nil {
			t.Errorf("precision on %s must be rejected", name)
		}
	}
	if _, err := New(struct {
		A float64 `tune:"precision:0"`
	}{}); err != nil {
		t.Errorf("precision 0 is fine: %v", err)
	}
}
