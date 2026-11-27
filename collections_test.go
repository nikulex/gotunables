package tunables

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

type listConfig struct {
	Admins  Set[int64]      `json:"admins" tune:"desc:Telegram ids; default:5,3,5; min:1"`
	Tags    []string        `json:"tags" tune:"default:a,b; maxlen:4"`
	Colors  Set[string]     `json:"colors" tune:"enum:red,green,blue; default:red"`
	Waits   []time.Duration `json:"waits" tune:"default:1s,2m; min:10ms; max:1h"`
	Ratios  []float32       `json:"ratios" tune:"min:0; max:1; precision:2"`
	Flags   []bool          `json:"flags"`
	Small   Set[int8]       `json:"small"`
	Ports   Set[uint16]     `json:"ports" tune:"minlen:1; default:80"`
	Nothing []int           `json:"nothing"`
}

func TestCollectionsDefaultsAndReading(t *testing.T) {
	tn, err := New(listConfig{Flags: []bool{true, false}})
	if err != nil {
		t.Fatal(err)
	}
	c := tn.Get()
	if !c.Admins.Has(3) || !c.Admins.Has(5) || c.Admins.Has(4) || c.Admins.Len() != 2 {
		t.Errorf("admins %v", c.Admins)
	}
	if !slices.Equal(c.Tags, []string{"a", "b"}) || !c.Colors.Has("red") {
		t.Errorf("tags %v colors %v", c.Tags, c.Colors)
	}
	if !slices.Equal(c.Waits, []time.Duration{time.Second, 2 * time.Minute}) {
		t.Errorf("waits %v", c.Waits)
	}
	if !slices.Equal(c.Flags, []bool{true, false}) {
		t.Errorf("the value passed to New is the default: %v", c.Flags)
	}
	if c.Nothing != nil || c.Ratios != nil {
		t.Errorf("empty must stay nil: %v %v", c.Nothing, c.Ratios)
	}
}

func TestCollectionsSetValidateAndNeverShareMemory(t *testing.T) {
	tn, err := New(listConfig{})
	if err != nil {
		t.Fatal(err)
	}
	before := tn.Get()
	if err := tn.Set("admins", []int64{9, 1, 9}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("waits", []time.Duration{time.Second, 30 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("ratios", []float32{0.25, 1}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("colors", NewSet("blue", "green")); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("tags", []string{}); err != nil {
		t.Fatal(err)
	}
	c := tn.Get()
	if c.Admins.Len() != 2 || !c.Admins.Has(1) || !c.Admins.Has(9) {
		t.Errorf("admins %v", c.Admins)
	}
	if !slices.Equal(c.Waits, []time.Duration{time.Second, 30 * time.Minute}) || !slices.Equal(c.Ratios, []float32{0.25, 1}) {
		t.Errorf("waits %v ratios %v", c.Waits, c.Ratios)
	}
	if c.Tags != nil || before.Tags == nil || before.Admins.Has(9) {
		t.Errorf("an old snapshot must not change: %v / %v", before.Tags, before.Admins)
	}

	bad := []struct {
		key string
		val any
	}{
		{"admins", []int64{0}},                       // below min 1, item 1
		{"admins", []string{"x"}},                    // not numbers
		{"admins", 5},                                // not a list
		{"admins", nil},                              // null
		{"tags", []string{"a", "b", "c", "d", "e"}},  // maxlen 4
		{"colors", NewSet("purple")},                 // not in the enum
		{"waits", []time.Duration{time.Millisecond}}, // below min
		{"waits", []time.Duration{2 * time.Hour}},    // above max
		{"ratios", []float32{1.5}},
		{"small", []int{300}},   // overflows int8
		{"ports", []int{}},      // minlen 1
		{"ports", []int{70000}}, // overflows uint16
		{"ports", []int{-1}},    // negative uint
	}
	for _, b := range bad {
		if err := tn.Set(b.key, b.val); err == nil {
			t.Errorf("Set(%s, %v) must fail", b.key, b.val)
		}
	}
	if tn.Get().Admins.Len() != 2 {
		t.Error("a rejected value must change nothing")
	}
}

func TestCollectionsKeepOrderOfAListAndSortASet(t *testing.T) {
	tn, err := New(listConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("tags", []string{"z", "a", "z"}); err != nil {
		t.Fatal(err)
	}
	if got := tn.Get().Tags; !slices.Equal(got, []string{"z", "a", "z"}) {
		t.Errorf("a list keeps the order and the repeats: %v", got)
	}
	h := tn.Handler()
	w := do(h, "PUT", "/values/small", `{"value":[3,-1,2,3]}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var f fieldDTO
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Value, []any{float64(-1), float64(2), float64(3)}) || f.Type != "set" || f.Item != "int" {
		t.Errorf("a set is sorted and has no repeats: %+v", f)
	}
}

func TestCollectionsInTheSchemaAPI(t *testing.T) {
	tn, err := New(listConfig{})
	if err != nil {
		t.Fatal(err)
	}
	w := do(tn.Handler(), "GET", "/schema", "", nil)
	var out struct{ Fields []fieldDTO }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]fieldDTO{}
	for _, f := range out.Fields {
		by[f.Key] = f
	}
	if f := by["admins"]; f.Type != "set" || f.Item != "int" || f.UI != "chips" || f.Min != float64(1) || f.Modified {
		t.Errorf("admins %+v", f)
	}
	if f := by["tags"]; f.Type != "list" || f.Item != "string" || f.MaxLen == nil || *f.MaxLen != 4 {
		t.Errorf("tags %+v", f)
	}
	if f := by["colors"]; f.Item != "enum" || !slices.Equal(f.Enum, []string{"red", "green", "blue"}) {
		t.Errorf("colors %+v", f)
	}
	if f := by["waits"]; f.Item != "duration" || f.Min != "10ms" || f.Max != "1h0m0s" || !reflect.DeepEqual(f.Value, []any{"1s", "2m0s"}) {
		t.Errorf("waits %+v", f)
	}
	if f := by["ratios"]; f.Item != "float" || f.Precision == nil || *f.Precision != 2 || !reflect.DeepEqual(f.Value, []any{}) || f.Modified {
		t.Errorf("ratios %+v", f)
	}
	if f := by["ports"]; f.Item != "int" || f.MinLen == nil {
		t.Errorf("ports %+v", f)
	}
}

func TestCollectionsModifiedAndReset(t *testing.T) {
	tn, err := New(listConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("admins", []int64{5, 3}); err != nil { // the same as the default
		t.Fatal(err)
	}
	if f := tn.c.dto(tn.c.byKey["admins"], tn.c.state.Load()); f.Modified {
		t.Error("the same items in another order are the default of a set")
	}
	if err := tn.Set("admins", []int64{1}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Reset("admins"); err != nil {
		t.Fatal(err)
	}
	if c := tn.Get(); c.Admins.Len() != 2 || !c.Admins.Has(3) {
		t.Errorf("reset: %v", c.Admins)
	}
}

func TestCollectionsInTheFileStoreAreArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	tn, err := New(listConfig{}, WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("admins", []int64{30, 4, 200}); err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("small", NewSet[int8](-5, 5)); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	compact := func(r json.RawMessage) string {
		var b bytes.Buffer
		if err := json.Compact(&b, r); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	if compact(raw["admins"]) != "[4,30,200]" || compact(raw["small"]) != "[-5,5]" {
		t.Errorf("a set is stored as a sorted array: %s %s", raw["admins"], raw["small"])
	}
	tn.Close()
	again, err := New(listConfig{}, WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	if c := again.Get(); c.Admins.Len() != 3 || !c.Admins.Has(200) || !c.Small.Has(-5) {
		t.Errorf("reloaded: %v %v", c.Admins, c.Small)
	}
}

func TestInvalidSavedItemsFallBackToTheDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, []byte(`{"admins":[0,7],"tags":["a","b","c","d","e"],"waits":[3600000000000]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tn, err := New(listConfig{}, WithFile(path))
	if err != nil {
		t.Fatal(err)
	}
	c := tn.Get()
	if !c.Admins.Has(3) || c.Admins.Has(7) {
		t.Errorf("an invalid saved set falls back as a whole: %v", c.Admins)
	}
	if !slices.Equal(c.Tags, []string{"a", "b"}) {
		t.Errorf("tags %v", c.Tags)
	}
	if !slices.Equal(c.Waits, []time.Duration{time.Hour}) {
		t.Errorf("a valid saved list is used: %v", c.Waits)
	}
}

func TestBadCollectionSchemas(t *testing.T) {
	type bytes struct{ X []byte }
	type nested struct{ X [][]int }
	type structs struct{ X []struct{ A int } }
	type mapField struct{ X map[string]int }
	type sliceMinLen struct {
		X int `tune:"minlen:1"`
	}
	type enumList struct {
		X []int `tune:"enum:a,b"`
	}
	type badLens struct {
		X []int `tune:"minlen:5; maxlen:2"`
	}
	type badDefault struct {
		X []int `tune:"default:1,two"`
	}
	type defaultOverMax struct {
		X []int `tune:"default:1,200; max:100"`
	}
	type sliderList struct {
		X []int `tune:"ui:slider; min:1; max:2"`
	}
	check := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	_, err := New(bytes{})
	check("[]byte", err)
	_, err = New(nested{})
	check("[][]int", err)
	_, err = New(structs{})
	check("[]struct", err)
	_, err = New(mapField{})
	check("map", err)
	_, err = New(sliceMinLen{})
	check("minlen on an int", err)
	_, err = New(enumList{})
	check("enum on ints", err)
	_, err = New(badLens{})
	check("minlen > maxlen", err)
	_, err = New(badDefault{})
	check("bad default item", err)
	_, err = New(defaultOverMax{})
	check("default over max", err)
	_, err = New(sliderList{})
	check("slider on a list", err)
}

// Every number size works as a field, as a list item and as a set item.
type numbers struct {
	I   int     `json:"i"`
	I8  int8    `json:"i8" tune:"min:-100; max:100"`
	I16 int16   `json:"i16"`
	I32 int32   `json:"i32"`
	I64 int64   `json:"i64"`
	U   uint    `json:"u"`
	U8  uint8   `json:"u8"`
	U16 uint16  `json:"u16"`
	U32 uint32  `json:"u32"`
	U64 uint64  `json:"u64"`
	F32 float32 `json:"f32"`
	F64 float64 `json:"f64"`

	LI8  []int8       `json:"li8"`
	LU32 []uint32     `json:"lu32"`
	LF32 []float32    `json:"lf32"`
	LF64 []float64    `json:"lf64"`
	SI16 Set[int16]   `json:"si16"`
	SU64 Set[uint64]  `json:"su64"`
	SF32 Set[float32] `json:"sf32"`
}

func TestEveryNumberSize(t *testing.T) {
	tn, err := New(numbers{})
	if err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{
		"i": -5, "i8": -100, "i16": 32767, "i32": -2147483648, "i64": int64(9007199254740991), "u": 7, "u8": 255, "u16": 65535,
		"u32": 4294967295, "u64": 1 << 40, "f32": 0.1, "f64": 1e-9,
		"li8": []int8{-128, 127}, "lu32": []uint32{4294967295}, "lf32": []float32{0.1, 0.2}, "lf64": []float64{1.5},
		"si16": NewSet[int16](-1, 1), "su64": NewSet[uint64](1, 1<<40), "sf32": NewSet[float32](0.5),
	}
	for k, v := range ok {
		if err := tn.Set(k, v); err != nil {
			t.Errorf("Set(%s, %v): %v", k, v, err)
		}
	}
	over := map[string]any{
		"i8": 101, "i16": 32768, "i32": 2147483648, "u8": 256, "u16": 65536, "u32": 4294967296, "u": -1, "u64": -1,
		"f32": 1e39, "li8": []int{128}, "lu32": []int64{-1}, "si16": []int{40000}, "su64": []int{-3}, "lf32": []float64{1e39},
	}
	for k, v := range over {
		if err := tn.Set(k, v); err == nil {
			t.Errorf("Set(%s, %v) must fail", k, v)
		}
	}
	c := tn.Get()
	if c.I8 != -100 || c.U8 != 255 || c.U64 != 1<<40 || c.F32 != float32(0.1) || !slices.Equal(c.LF32, []float32{0.1, 0.2}) || !c.SU64.Has(1<<40) || !c.SF32.Has(0.5) {
		t.Errorf("%+v", c)
	}
	// the schema shows a float32 as the number it was written, not 0.10000000149
	w := do(tn.Handler(), "GET", "/schema", "", nil)
	var out struct{ Fields []fieldDTO }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Fields {
		switch f.Key {
		case "f32":
			if f.Value != 0.1 {
				t.Errorf("f32 %v", f.Value)
			}
		case "lf32":
			if !reflect.DeepEqual(f.Value, []any{0.1, 0.2}) {
				t.Errorf("lf32 %v", f.Value)
			}
		}
	}
}

func TestSetJSON(t *testing.T) {
	var s Set[string]
	if err := json.Unmarshal([]byte(`["b","a","b"]`), &s); err != nil || s.Len() != 2 {
		t.Fatalf("%v %v", s, err)
	}
	b, _ := json.Marshal(s)
	if string(b) != `["a","b"]` {
		t.Errorf("%s", b)
	}
	b, _ = json.Marshal(Set[int](nil))
	if string(b) != `[]` {
		t.Errorf("a nil set is an empty array: %s", b)
	}
	if err := json.Unmarshal([]byte(`{"a":1}`), &s); err == nil {
		t.Error("an object is not a set")
	}
}
