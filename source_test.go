package tunables

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type pickConfig struct {
	Admins Set[uint] `json:"admins" tune:"source:users; default:1; maxlen:3"`
	Owner  *uint     `json:"owner" tune:"source:users; desc:One or nobody"`
	Order  []uint    `json:"order" tune:"source:users"`
	Chat   *int64    `json:"chat" tune:"source:chats"`
	Code   string    `json:"code" tune:"source:codes"`
	Free   []int64   `json:"free" tune:"source:chats; custom:true"`
	Anyone *uint     `json:"anyone" tune:"source:users; custom:true"`
}

var testUsers = map[uint]string{1: "Ann", 2: "Bob", 3: "Annie", 4: "Carl"}

func usersSource() Option {
	return WithSource("users", DataSource[uint]{
		Search: func(_ context.Context, q string, limit, offset int) ([]Item[uint], error) {
			var out []Item[uint]
			for id := uint(1); id <= 4; id++ {
				if strings.Contains(strings.ToLower(testUsers[id]), strings.ToLower(q)) {
					out = append(out, Item[uint]{ID: id, Name: testUsers[id], ImageSrc: "https://img.example/" + testUsers[id] + ".png"})
				}
			}
			out = out[min(offset, len(out)):]
			return out[:min(limit, len(out))], nil
		},
		Get: func(_ context.Context, ids []uint) ([]Item[uint], error) {
			var out []Item[uint]
			for _, id := range ids {
				if n, ok := testUsers[id]; ok {
					out = append(out, Item[uint]{ID: id, Name: n})
				}
			}
			return out, nil
		},
	})
}

func chatsSource() Option {
	return WithSource("chats", DataSource[int64]{
		Search: func(context.Context, string, int, int) ([]Item[int64], error) {
			return []Item[int64]{{ID: -100, Name: "Dev chat"}}, nil
		},
		Get: func(_ context.Context, ids []int64) ([]Item[int64], error) {
			var out []Item[int64]
			for _, id := range ids {
				if id == -100 {
					out = append(out, Item[int64]{ID: id, Name: "Dev chat"})
				}
			}
			return out, nil
		},
	})
}

func codesSource() Option {
	return WithSource("codes", DataSource[string]{
		Search: func(context.Context, string, int, int) ([]Item[string], error) {
			return []Item[string]{{ID: "a1", Name: "A one"}}, nil
		},
		Get: func(_ context.Context, ids []string) ([]Item[string], error) {
			var out []Item[string]
			for _, id := range ids {
				if id == "a1" {
					out = append(out, Item[string]{ID: id, Name: "A one"})
				}
			}
			return out, nil
		},
	})
}

func newPick(t *testing.T, opts ...Option) *Tunables[pickConfig] {
	t.Helper()
	tn, err := New(pickConfig{}, append([]Option{usersSource(), chatsSource(), codesSource()}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func TestSourceFieldsHoldIDsOfEverySort(t *testing.T) {
	tn := newPick(t)
	c := tn.Get()
	if !c.Admins.Has(1) || c.Owner != nil || c.Order != nil || c.Chat != nil {
		t.Fatalf("defaults: %+v", c)
	}
	for key, v := range map[string]any{
		"admins": []uint{3, 2, 3},
		"owner":  uint(4),
		"order":  []uint{4, 1, 4},
		"chat":   int64(-100),
		"code":   "a1",
	} {
		if err := tn.Set(key, v); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	c = tn.Get()
	if !c.Admins.Has(2) || !c.Admins.Has(3) || c.Admins.Len() != 2 || c.Owner == nil || *c.Owner != 4 || !slices.Equal(c.Order, []uint{4, 1, 4}) || *c.Chat != -100 || c.Code != "a1" {
		t.Errorf("%+v", c)
	}
	// nobody: a pointer goes back to nil
	if err := tn.Set("owner", nil); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("chat", (*int64)(nil)); err != nil {
		t.Fatal(err)
	}
	if c := tn.Get(); c.Owner != nil || c.Chat != nil {
		t.Errorf("nobody: %+v", c)
	}
	// a pointer set to a value through a pointer
	owner := uint(2)
	if err := tn.Set("owner", &owner); err != nil {
		t.Fatal(err)
	}
	if c := tn.Get(); c.Owner == nil || *c.Owner != 2 {
		t.Errorf("owner %v", c.Owner)
	}
}

func TestSourceRefusesAnIDItDoesNotKnow(t *testing.T) {
	tn := newPick(t)
	h := tn.Handler()
	for _, tc := range []struct{ key, body string }{
		{"admins", `{"value":[1,99]}`},
		{"owner", `{"value":99}`},
		{"order", `{"value":[2,77]}`},
		{"chat", `{"value":5}`},
		{"code", `{"value":"zz"}`},
	} {
		w := do(h, "PUT", "/values/"+tc.key, tc.body, jsonCT)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no such item") {
			t.Errorf("%s: %d %s", tc.key, w.Code, w.Body)
		}
	}
	c := tn.Get()
	if c.Owner != nil || c.Order != nil || c.Code != "" || !c.Admins.Has(1) {
		t.Errorf("nothing may change: %+v", c)
	}
	// the ones that exist go through, and null is "nobody"
	if w := do(h, "PUT", "/values/owner", `{"value":2}`, jsonCT); w.Code != 200 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/values/owner", `{"value":null}`, jsonCT); w.Code != 200 || tn.Get().Owner != nil {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	// null is not for a field that is not a pointer
	if w := do(h, "PUT", "/values/code", `{"value":null}`, jsonCT); w.Code != http.StatusBadRequest {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	// the limits of a set still hold
	if w := do(h, "PUT", "/values/admins", `{"value":[1,2,3,4]}`, jsonCT); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at most 3") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}

func TestSourceFailureOnWriteIsNotTakenForABadValue(t *testing.T) {
	tn, err := New(struct {
		Who *uint `json:"who" tune:"source:users"`
	}{}, WithSource("users", DataSource[uint]{
		Search: func(context.Context, string, int, int) ([]Item[uint], error) { return nil, nil },
		Get: func(context.Context, []uint) ([]Item[uint], error) {
			return nil, errors.New("db is down: password=secret")
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := do(tn.Handler(), "PUT", "/values/who", `{"value":1}`, jsonCT)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if tn.Get().Who != nil {
		t.Error("nothing may change")
	}
}

func TestSourceInTheSchema(t *testing.T) {
	tn := newPick(t)
	w := do(tn.Handler(), "GET", "/schema", "", nil)
	var out struct{ Fields []fieldDTO }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]fieldDTO{}
	for _, f := range out.Fields {
		by[f.Key] = f
	}
	if f := by["admins"]; f.Type != "set" || f.Item != "int" || f.UI != "picker" || f.Source != "users" || f.Optional {
		t.Errorf("admins %+v", f)
	}
	if f := by["owner"]; f.Type != "int" || f.UI != "picker" || f.Source != "users" || !f.Optional || f.Value != nil || f.Default != nil || f.Modified {
		t.Errorf("owner %+v", f)
	}
	if f := by["order"]; f.Type != "list" || f.Item != "int" || f.Source != "users" {
		t.Errorf("order %+v", f)
	}
	if f := by["code"]; f.Type != "string" || f.Source != "codes" || f.Optional {
		t.Errorf("code %+v", f)
	}
	// "nobody" is in the raw JSON as null, not left out
	if !strings.Contains(w.Body.String(), `"default":null,"value":null,"modified":false,"source":"users","optional":true`) {
		t.Errorf("owner is not null in %s", w.Body)
	}
	_ = do(tn.Handler(), "PUT", "/values/owner", `{"value":3}`, jsonCT)
	w = do(tn.Handler(), "GET", "/schema", "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Fields {
		if f.Key == "owner" && (!f.Modified || f.Value != float64(3)) {
			t.Errorf("owner after a change %+v", f)
		}
	}
}

func TestSourceSearchAndItemsEndpoints(t *testing.T) {
	tn := newPick(t)
	h := tn.Handler()
	type resp struct {
		Items []struct {
			ID    any    `json:"id"`
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"items"`
		Error string `json:"error"`
	}
	get := func(path string) (int, resp) {
		w := do(h, "GET", path, "", nil)
		var r resp
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatalf("%s: %v %s", path, err, w.Body)
		}
		return w.Code, r
	}
	if code, r := get("/sources/users/search?q=ann"); code != 200 || len(r.Items) != 2 || r.Items[0].Name != "Ann" || r.Items[0].ID != float64(1) || r.Items[0].Image != "https://img.example/Ann.png" {
		t.Errorf("search: %d %+v", code, r)
	}
	if code, r := get("/sources/users/search"); code != 200 || len(r.Items) != 4 { // an empty query is asked for too
		t.Errorf("empty query: %d %+v", code, r)
	}
	if code, r := get("/sources/users/search?q=ZZZ"); code != 200 || r.Items == nil || len(r.Items) != 0 {
		t.Errorf("nothing found must be [], not null: %d %+v", code, r)
	}
	if code, r := get("/sources/users/search?limit=2"); code != 200 || len(r.Items) != 2 {
		t.Errorf("limit: %d %+v", code, r)
	}
	if code, r := get("/sources/users/items?id=2&id=4&id=99"); code != 200 || len(r.Items) != 2 || r.Items[0].Name != "Bob" {
		t.Errorf("items: %d %+v", code, r)
	}
	if code, r := get("/sources/chats/items?id=-100"); code != 200 || len(r.Items) != 1 || r.Items[0].ID != float64(-100) {
		t.Errorf("a signed id: %d %+v", code, r)
	}
	if code, r := get("/sources/codes/items?id=a1"); code != 200 || len(r.Items) != 1 || r.Items[0].ID != "a1" {
		t.Errorf("a string id: %d %+v", code, r)
	}
	if code, r := get("/sources/users/items"); code != 200 || r.Items == nil || len(r.Items) != 0 {
		t.Errorf("no ids: %d %+v", code, r)
	}
	for _, path := range []string{"/sources/nope/search", "/sources/nope/items?id=1"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("%s: %d", path, code)
		}
	}
	for _, path := range []string{"/sources/users/items?id=abc", "/sources/users/items?id=-1", "/sources/users/items?id=1.5"} {
		if code, r := get(path); code != http.StatusBadRequest || r.Error == "" {
			t.Errorf("%s: %d %+v", path, code, r)
		}
	}
	many := "/sources/users/items?id=1" + strings.Repeat("&id=1", sourceMaxItemIDs)
	if code, _ := get(many); code != http.StatusBadRequest {
		t.Errorf("too many ids: %d", code)
	}
	if w := do(h, "POST", "/sources/users/search", "", nil); w.Code == 200 {
		t.Errorf("a source is read only: %d", w.Code)
	}
}

func TestSourceSearchGetsTheQueryAndALimitInRange(t *testing.T) {
	var gotQ string
	var gotLimit int
	tn, err := New(struct {
		Who *uint `json:"who" tune:"source:s"`
	}{}, WithSource("s", DataSource[uint]{
		Search: func(_ context.Context, q string, limit, _ int) ([]Item[uint], error) {
			gotQ, gotLimit = q, limit
			out := make([]Item[uint], 500) // a source that ignores the limit
			for i := range out {
				out[i] = Item[uint]{ID: uint(i), Name: "x"}
			}
			return out, nil
		},
		Get: func(context.Context, []uint) ([]Item[uint], error) { return nil, nil },
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	for path, want := range map[string]int{"/sources/s/search": sourceDefLimit, "/sources/s/search?limit=1000": sourceMaxLimit, "/sources/s/search?limit=0": 1, "/sources/s/search?limit=-5": 1, "/sources/s/search?limit=x": sourceDefLimit} {
		w := do(h, "GET", path, "", nil)
		var r struct{ Items []any }
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		if gotLimit != want+1 || len(r.Items) != want { // the source is asked for one more than the page: that tells whether there is a next one
			t.Errorf("%s: the source got %d, the page got %d, want %d", path, gotLimit, len(r.Items), want)
		}
	}
	do(h, "GET", "/sources/s/search?q=++"+strings.Repeat("ж", 300), "", nil)
	if n := len([]rune(gotQ)); n != sourceMaxQuery {
		t.Errorf("a query is cut to %d characters, the source got %d", sourceMaxQuery, n)
	}
}

func TestSourceErrorsAreNotShownToThePage(t *testing.T) {
	tn, err := New(struct {
		Who *uint `json:"who" tune:"source:s"`
	}{}, WithSource("s", DataSource[uint]{
		Search: func(context.Context, string, int, int) ([]Item[uint], error) {
			return nil, errors.New("dial tcp 10.0.0.5: password=secret")
		},
		Get: func(context.Context, []uint) ([]Item[uint], error) {
			return nil, errors.New("dial tcp 10.0.0.5: password=secret")
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/sources/s/search?q=a", "/sources/s/items?id=1"} {
		w := do(tn.Handler(), "GET", path, "", nil)
		if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "10.0.0.5") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestSourcesNeedTheTokenToo(t *testing.T) {
	tn := newPick(t, WithToken("s3cret"))
	for _, path := range []string{"/sources/users/search", "/sources/users/items?id=1"} {
		if w := do(tn.Handler(), "GET", path, "", nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d", path, w.Code)
		}
		if w := do(tn.Handler(), "GET", path, "", map[string]string{"Authorization": "Bearer s3cret"}); w.Code != 200 {
			t.Errorf("%s with the token: %d", path, w.Code)
		}
	}
}

func TestSourceThePageAllowsPicturesOnly(t *testing.T) {
	w := do(newPick(t).Handler(), "GET", "/", "", nil)
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src https: data:") || strings.Contains(csp, "img-src *") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("csp %q", csp)
	}
}

func TestSourceInTheFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	tn := newPick(t, WithFile(path))
	for key, v := range map[string]any{"owner": uint(3), "admins": []uint{2, 4}, "chat": int64(-100)} {
		if err := tn.Set(key, v); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["owner"] != float64(3) || saved["chat"] != float64(-100) {
		t.Errorf("saved %s", b)
	}
	again := newPick(t, WithFile(path)).Get()
	if again.Owner == nil || *again.Owner != 3 || !again.Admins.Has(4) || *again.Chat != -100 {
		t.Errorf("reloaded %+v", again)
	}
	// "nobody" is saved as null and comes back as nil
	if err := tn.Set("owner", nil); err != nil {
		t.Fatal(err)
	}
	if o := newPick(t, WithFile(path)).Get().Owner; o != nil {
		t.Errorf("nobody came back as %v", *o)
	}
}

func TestSourceResetGoesBackToTheDefault(t *testing.T) {
	tn := newPick(t)
	if err := tn.Set("owner", uint(2)); err != nil {
		t.Fatal(err)
	}
	if err := tn.Reset("owner"); err != nil || tn.Get().Owner != nil {
		t.Errorf("owner %v %v", tn.Get().Owner, err)
	}
	if err := tn.Set("admins", []uint{4}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Reset("admins"); err != nil || !tn.Get().Admins.Has(1) || tn.Get().Admins.Len() != 1 {
		t.Errorf("admins %v %v", tn.Get().Admins, err)
	}
}

func TestBadSources(t *testing.T) {
	ok := WithSource("users", DataSource[uint]{
		Search: func(context.Context, string, int, int) ([]Item[uint], error) { return nil, nil },
		Get:    func(context.Context, []uint) ([]Item[uint], error) { return nil, nil },
	})
	type one struct {
		Who *uint `json:"who" tune:"source:users"`
	}
	for name, tc := range map[string]struct {
		cfg  func() error
		want string
	}{
		"not registered": {func() error { _, err := New(one{}); return err }, `source "users" is not registered`},
		"wrong id type": {func() error {
			_, err := New(struct {
				Who *int64 `json:"who" tune:"source:users"`
			}{}, ok)
			return err
		}, "has ids of type uint, the field holds int64"},
		"wrong item type": {func() error {
			_, err := New(struct {
				Who Set[uint32] `json:"who" tune:"source:users"`
			}{}, ok)
			return err
		}, "has ids of type uint, the field holds uint32"},
		"a bool": {func() error {
			_, err := New(struct {
				Who bool `json:"who" tune:"source:users"`
			}{}, ok)
			return err
		}, "source is only for ids"},
		"a float": {func() error {
			_, err := New(struct {
				Who []float64 `json:"who" tune:"source:users"`
			}{}, ok)
			return err
		}, "source is only for ids"},
		"an enum": {func() error {
			_, err := New(struct {
				Who string `json:"who" tune:"source:users; enum:a,b"`
			}{}, ok)
			return err
		}, "source is only for ids"},
		"a bound": {func() error {
			_, err := New(struct {
				Who uint `json:"who" tune:"source:users; min:1"`
			}{}, ok)
			return err
		}, "do not go with source"},
		"another component": {func() error {
			_, err := New(struct {
				Who uint `json:"who" tune:"source:users; ui:counter"`
			}{}, ok)
			return err
		}, "ui:counter is not for"},
		"a pointer without a source": {func() error {
			_, err := New(struct {
				Who *uint `json:"who"`
			}{})
			return err
		}, "unsupported type"},
		"a pointer to a list": {func() error {
			_, err := New(struct {
				Who *[]uint `json:"who" tune:"source:users"`
			}{}, ok)
			return err
		}, "is not a setting"},
		"twice": {func() error { _, err := New(one{}, ok, ok); return err }, "registered twice"},
		"no Get": {func() error {
			_, err := New(one{}, WithSource("users", DataSource[uint]{Search: func(context.Context, string, int, int) ([]Item[uint], error) { return nil, nil }}))
			return err
		}, "needs both Search and Get"},
		"a bad name": {func() error {
			_, err := New(one{}, WithSource("a/b", DataSource[uint]{}))
			return err
		}, "the name must be"},
		"an id that is not an id": {func() error {
			_, err := New(one{}, WithSource("x", DataSource[float64]{
				Search: func(context.Context, string, int, int) ([]Item[float64], error) { return nil, nil },
				Get:    func(context.Context, []float64) ([]Item[float64], error) { return nil, nil },
			}))
			return err
		}, "must be an integer or a string"},
	} {
		if err := tc.cfg(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// an unused source is fine
	if _, err := New(struct{ A bool }{}, ok); err != nil {
		t.Error(err)
	}
}

func TestSourceSearchPagesAndSaysWhetherThereIsMore(t *testing.T) {
	var gotOffset int
	var total = 7
	tn, err := New(struct {
		Who *uint `json:"who" tune:"source:s"`
	}{}, WithSource("s", DataSource[uint]{
		Search: func(_ context.Context, _ string, limit, offset int) ([]Item[uint], error) {
			gotOffset = offset
			var out []Item[uint]
			for i := offset; i < min(offset+limit, total); i++ {
				out = append(out, Item[uint]{ID: uint(i + 1), Name: "n"})
			}
			return out, nil
		},
		Get: func(context.Context, []uint) ([]Item[uint], error) { return nil, nil },
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	type page struct {
		Items []struct{ ID uint }
		More  bool
	}
	read := func(path string) page {
		w := do(h, "GET", path, "", nil)
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %v %s", path, w.Code, err, w.Body)
		}
		return p
	}
	// 7 entries, 3 to a page: 3 + 3 + 1, and only the last one has no next
	var seen []uint
	for offset, pages := 0, 0; ; pages++ {
		p := read("/sources/s/search?limit=3&offset=" + strconv.Itoa(offset))
		if gotOffset != offset {
			t.Errorf("the source got offset %d, want %d", gotOffset, offset)
		}
		for _, it := range p.Items {
			seen = append(seen, it.ID)
		}
		offset += len(p.Items)
		if !p.More {
			if pages != 2 {
				t.Errorf("the last page came after %d pages, want 3", pages+1)
			}
			break
		}
		if pages > 5 {
			t.Fatal("it does not end")
		}
	}
	if !slices.Equal(seen, []uint{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("seen %v", seen)
	}
	// a page that ends exactly where the data does has no next one
	total = 6
	if p := read("/sources/s/search?limit=3&offset=3"); len(p.Items) != 3 || p.More {
		t.Errorf("an exact last page: %+v", p)
	}
	// the offset is kept in range
	read("/sources/s/search?offset=-4")
	if gotOffset != 0 {
		t.Errorf("a negative offset is 0, the source got %d", gotOffset)
	}
	read("/sources/s/search?offset=99999999")
	if gotOffset != sourceMaxOffset {
		t.Errorf("a huge offset is cut to %d, the source got %d", sourceMaxOffset, gotOffset)
	}
	read("/sources/s/search?offset=abc")
	if gotOffset != 0 {
		t.Errorf("a bad offset is 0, the source got %d", gotOffset)
	}
}

func TestSourceCustomTakesAnIDTheSourceDoesNotKnow(t *testing.T) {
	var asked int
	tn, err := New(pickConfig{}, usersSource(), codesSource(), WithSource("chats", DataSource[int64]{
		Search: func(context.Context, string, int, int) ([]Item[int64], error) { return nil, nil },
		Get: func(_ context.Context, ids []int64) ([]Item[int64], error) {
			asked++
			return nil, nil // knows nobody
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	// a field with custom takes any id, and the source is not even asked
	if w := do(h, "PUT", "/values/free", `{"value":[-1001234567890, 5]}`, jsonCT); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if err := tn.Set("anyone", uint(424242)); err != nil {
		t.Fatal(err)
	}
	if asked != 0 {
		t.Errorf("the source was asked %d times for a custom field", asked)
	}
	c := tn.Get()
	if !slices.Equal(c.Free, []int64{-1001234567890, 5}) || c.Anyone == nil || *c.Anyone != 424242 {
		t.Errorf("%+v", c)
	}
	// a field without it still refuses what the source does not know
	if w := do(h, "PUT", "/values/owner", `{"value":424242}`, jsonCT); w.Code != http.StatusBadRequest {
		t.Errorf("owner: %d %s", w.Code, w.Body)
	}
	// the page is told
	w := do(h, "GET", "/schema", "", nil)
	var out struct{ Fields []fieldDTO }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Fields {
		if got, want := f.Custom, f.Key == "free" || f.Key == "anyone"; got != want {
			t.Errorf("%s: custom %v, want %v", f.Key, got, want)
		}
	}
}

func TestSourceCustomNeedsASource(t *testing.T) {
	for name, cfg := range map[string]any{
		"without a source": struct {
			Who uint `json:"who" tune:"custom:true"`
		}{},
		"a bad value": struct {
			Who uint `json:"who" tune:"source:users; custom:maybe"`
		}{},
	} {
		var err error
		switch c := cfg.(type) {
		case struct {
			Who uint `json:"who" tune:"custom:true"`
		}:
			_, err = New(c, usersSource())
		case struct {
			Who uint `json:"who" tune:"source:users; custom:maybe"`
		}:
			_, err = New(c, usersSource())
		}
		if err == nil || !strings.Contains(err.Error(), "custom") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// custom:false is the same as nothing
	if _, err := New(struct {
		Who uint `json:"who" tune:"source:users; custom:false"`
	}{}, usersSource()); err != nil {
		t.Error(err)
	}
}
