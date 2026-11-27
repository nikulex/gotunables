package tunables

import (
	"cmp"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	durationType = reflect.TypeFor[time.Duration]()
	timeType     = reflect.TypeFor[time.Time]()
)

// Field kinds. The API type names are the same except that "uint" is reported as "int".
const (
	kindBool     = "bool"
	kindString   = "string"
	kindEnum     = "enum"
	kindInt      = "int"
	kindUint     = "uint"
	kindFloat    = "float"
	kindDuration = "duration"
	kindList     = "list" // []T of any of the above
	kindSet      = "set"  // Set[T]
)

// maxItems is the most items a list or a set may hold.
const maxItems = 1000

// field is one leaf of the settings struct.
type field struct {
	key   string // "checkout.button.color"
	index []int  // path for reflect.Value.FieldByIndex
	typ   reflect.Type
	kind  string
	name  string // display name (tune:"name:...")
	ui    string // the component that edits it, see uiOptions
	prec  *int   // digits shown after the point of a float (tune:"precision:2"); nil: the UI derives it from the step
	desc  string
	def   any // canonical default: bool, string, int64, uint64, float64 or time.Duration; a []any of them for a list or a set
	enum  []string
	min   *float64 // durations are in nanoseconds; for a list or a set the bounds of an item live in elem
	max   *float64

	elem           *field // a list or a set: the field of one item (kind, enum, min, max, precision)
	minLen, maxLen *int   // a list or a set: how many items

	source string // the DataSource the ids come from (the item's, for a list or a set)
	custom bool   // an id the source does not know may be added too (tune:"custom:true"): it is not checked against the source
	ptr    bool   // a *T: one entry or none (nil); typ is then T

	public bool // the public handler gives it to whoever asks (tune:"public")
}

// group is a nested struct that has a display name or description.
type group struct {
	key, name, desc string
	rule            bool   // a Rule: its two sets (Only, Except) are drawn side by side in one card
	collapsed       bool   // the page shows it folded until someone opens it (tune:"collapsed:true")
	feature         bool   // a Feature: its rules are added from a list, and hidden while it is off
	section         string // the heading of the list the rule is under (tune:"section:..." on the embedded struct it comes from)
}

func (f *field) collection() bool { return f.elem != nil }

func (f *field) apiType() string {
	if f.kind == kindUint {
		return kindInt
	}
	return f.kind
}

// tune is a parsed `tune:"..."` tag.
type tune struct {
	skip bool
	opts map[string]string
}

// parseTune parses `tune:"name:Max items; desc:Cart size; default:10; min:1; max:100"`.
// Options are separated by ";", each is key:value. Known keys: key, name, desc, default, enum, min, max, ui, precision,
// minlen, maxlen, source, custom, collapsed and flatten (groups only), public (a field, or a group: all in it; "public"
// alone is "public:true") and section (an embedded struct: the heading its rules have in the list they are added from). `tune:"-"` skips the field.
func parseTune(sf reflect.StructField) (tune, error) {
	v, ok := sf.Tag.Lookup("tune")
	if !ok {
		return tune{}, nil
	}
	if strings.TrimSpace(v) == "-" {
		return tune{skip: true}, nil
	}
	t := tune{opts: map[string]string{}}
	for _, part := range strings.Split(v, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, val, ok := strings.Cut(part, ":")
		if !ok && strings.EqualFold(part, "public") {
			k, val, ok = "public", "true", true
		}
		if !ok {
			return tune{}, fmt.Errorf("tune option %q must be key:value", part)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "key", "name", "desc", "default", "enum", "min", "max", "ui", "precision", "minlen", "maxlen", "source", "custom", "collapsed", "flatten", "public", "section":
		default:
			return tune{}, fmt.Errorf("unknown tune option %q (known: key, name, desc, default, enum, min, max, ui, precision, minlen, maxlen, source, custom, collapsed, flatten, public, section)", k)
		}
		t.opts[k] = strings.TrimSpace(val)
	}
	return t, nil
}

// maxDepth is how many levels of nested structs a schema may have (the UI draws 3 levels of headings).
const maxDepth = 3

// uiOptions lists the components a field can be edited with, by wire type; the first one is the default.
var uiOptions = map[string][]string{
	kindBool:     {"switch", "checkbox"},
	kindEnum:     {"select", "buttons"},
	kindInt:      {"counter", "slider", "input"},
	kindFloat:    {"counter", "slider", "input"},
	kindString:   {"input", "textarea"},
	kindDuration: {"input"},
	kindList:     {"chips"},
	kindSet:      {"chips"},
}

// uiPicker is the component of a field that picks from a DataSource.
const uiPicker = "picker"

type walker struct {
	seen      map[string]bool
	fields    []*field
	groups    []*group
	features  []*featureRef // the Features in the struct
	groupSets []*groupSet   // the Groups of each of them
}

// walkAll reflects over the struct behind v (which must be addressable) and returns its leaves in
// declaration order. Defaults are the values already in v; a `default` option overrides (and is written into v).
func walkAll(t reflect.Type, v reflect.Value) (*walker, error) {
	w := &walker{seen: map[string]bool{}}
	if err := w.walk(t, v, "", nil, 0, nil, false, ""); err != nil {
		return nil, err
	}
	return w, nil
}

// inheritedOptions are the options of a KindRule's tag that its two sets take over: they describe the values, which are
// the same for both.
var inheritedOptions = []string{"enum", "min", "max", "precision", "minlen", "maxlen", "source", "custom"}

func (w *walker) walk(t reflect.Type, v reflect.Value, prefix string, index []int, depth int, inherit map[string]string, public bool, section string) error {
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		embeddedStruct := sf.Anonymous && sf.Type.Kind() == reflect.Struct
		if !sf.IsExported() && !embeddedStruct {
			continue
		}
		where := join(prefix, sf.Name)
		tag, err := parseTune(sf)
		if err != nil {
			return fmt.Errorf("tunables: field %s: %w", where, err)
		}
		if tag.skip {
			continue
		}
		name, skip := fieldName(sf, tag)
		if skip {
			continue
		}
		if f, ok := tag.opts["flatten"]; ok { // a struct whose fields are the ones of the struct around it: one level less
			on, err := strconv.ParseBool(f)
			if err != nil {
				return fmt.Errorf("tunables: field %s: flatten must be true or false, got %q", where, f)
			}
			if on && sf.Type.Kind() != reflect.Struct {
				return fmt.Errorf("tunables: field %s: flatten is only for a struct", where)
			}
			if on {
				name = ""
			}
		}
		if strings.Contains(name, ".") {
			return fmt.Errorf("tunables: field %s: name %q must not contain a dot", where, name)
		}
		pub := public // a field says it for itself, a group for what is in it; false takes one out
		if p, ok := tag.opts["public"]; ok {
			if pub, err = strconv.ParseBool(p); err != nil {
				return fmt.Errorf("tunables: field %s: public must be true or false, got %q", where, p)
			}
		}
		key := join(prefix, name) // an unnamed embedded struct is flattened: key == prefix
		sec := section            // the rules of an embedded struct that has a section are listed under its heading
		if t, ok := tag.opts["section"]; ok {
			if sf.Type.Kind() != reflect.Struct {
				return fmt.Errorf("tunables: field %s: section is only for a struct", where)
			}
			sec = t
		}
		idx := append(slices.Clone(index), i)
		fv := v.Field(i)

		if sf.Type.Kind() == reflect.Slice && sf.Type.Elem().Kind() == reflect.Struct {
			if m, ok := reflect.Zero(sf.Type.Elem()).Interface().(groupMarker); ok { // the Groups of a Feature: one value, a list
				if name == "" {
					return fmt.Errorf("tunables: field %s: the groups of a feature need a name", where)
				}
				gs, err := newGroupSet(key, prefix, idx, sf.Type, m.groupTargeting())
				if err != nil {
					return err
				}
				if w.seen[key] {
					return fmt.Errorf("tunables: duplicate key %q", key)
				}
				w.seen[key] = true
				gs.def = reflect.MakeSlice(sf.Type, 0, 0)
				if !fv.IsNil() {
					gs.def = reflect.MakeSlice(sf.Type, fv.Len(), fv.Len())
					reflect.Copy(gs.def, fv)
				}
				w.groupSets = append(w.groupSets, gs)
				continue
			}
		}

		if sf.Type.Kind() == reflect.Struct && sf.Type != timeType {
			inner := depth // levels inside this struct; an unnamed embedded struct is flattened and adds none
			if name != "" {
				inner++
				if inner > maxDepth {
					return fmt.Errorf("tunables: field %s: structs nest deeper than %d levels", key, maxDepth)
				}
				gname := tag.opts["name"]
				if gname == "" {
					gname = humanize(name)
				}
				kind := kindOf(sf.Type)
				collapsed := false
				if c, ok := tag.opts["collapsed"]; ok {
					var err error
					if collapsed, err = strconv.ParseBool(c); err != nil {
						return fmt.Errorf("tunables: field %s: collapsed must be true or false, got %q", key, c)
					}
				}
				w.groups = append(w.groups, &group{key: key, name: gname, desc: tag.opts["desc"], rule: kind == kindRule, collapsed: collapsed, feature: kind == kindFeature, section: section})
				if kind == kindFeature {
					w.features = append(w.features, &featureRef{key: key, index: idx, public: pub})
				}
			}
			next := inherit
			if kindOf(sf.Type) == kindRule {
				next = map[string]string{}
				for _, k := range inheritedOptions {
					if val, ok := tag.opts[k]; ok {
						next[k] = val
					}
				}
			}
			inside := pub
			if kindOf(sf.Type) == kindFeature { // a feature is given as the answer for the request, not as its fields
				inside = false
			}
			if err := w.walk(sf.Type, fv, key, idx, inner, next, inside, sec); err != nil {
				return err
			}
			continue
		}

		if _, ok := tag.opts["collapsed"]; ok {
			return fmt.Errorf("tunables: field %s: collapsed is only for a group (a struct), the page folds those", key)
		}
		for k, val := range inherit { // the options of the Rule the set belongs to; its own come first
			if tag.opts == nil {
				tag.opts = map[string]string{}
			}
			if _, ok := tag.opts[k]; !ok {
				tag.opts[k] = val
			}
		}
		f, err := newField(sf, key, idx, tag)
		if err != nil {
			return fmt.Errorf("tunables: field %s: %w", key, err)
		}
		if f.name == "" {
			f.name = humanize(name)
		}
		if w.seen[key] {
			return fmt.Errorf("tunables: duplicate key %q", key)
		}
		w.seen[key] = true

		f.public = pub
		f.def = f.read(fv)
		if d, ok := tag.opts["default"]; ok {
			dv, err := f.parseText(d)
			if err != nil {
				return fmt.Errorf("tunables: field %s: default %q: %w", key, d, err)
			}
			f.write(fv, dv)
			f.def = dv
		}
		if err := f.check(f.def); err != nil {
			return fmt.Errorf("tunables: field %s: default %v: %w", key, f.def, err)
		}
		w.fields = append(w.fields, f)
	}
	return nil
}

// humanize makes a display name from a key segment: "max_items" -> "Max items".
func humanize(s string) string {
	s = strings.TrimSpace(strings.NewReplacer("_", " ", "-", " ").Replace(s))
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func join(prefix, name string) string {
	switch {
	case prefix == "":
		return name
	case name == "":
		return prefix
	}
	return prefix + "." + name
}

// fieldName resolves the key segment of a field: `tune:"key:..."`, then the json tag, then yaml, then the
// lower-cased Go name. An embedded struct with no name is flattened (empty name). `json:"-"` and `yaml:"-"`
// skip the field.
func fieldName(sf reflect.StructField, tag tune) (name string, skip bool) {
	if k := tag.opts["key"]; k != "" {
		return k, false
	}
	for _, t := range []string{"json", "yaml"} {
		v, ok := sf.Tag.Lookup(t)
		if !ok {
			continue
		}
		n, _, _ := strings.Cut(v, ",")
		if n == "-" {
			return "", true
		}
		if n != "" {
			return n, false
		}
	}
	if sf.Anonymous && sf.Type.Kind() == reflect.Struct {
		return "", false
	}
	return strings.ToLower(sf.Name), false
}

// setMarker is implemented by Set[T] only: it is how a field of a set type is recognized.
type setMarker interface{ setElem() reflect.Type }

// scalarKind maps a Go type to the kind of a single value.
func scalarKind(t reflect.Type) (string, error) {
	switch k := t.Kind(); {
	case k == reflect.Bool:
		return kindBool, nil
	case k == reflect.String:
		return kindString, nil
	case t == durationType:
		return kindDuration, nil
	case k >= reflect.Int && k <= reflect.Int64:
		return kindInt, nil
	case k >= reflect.Uint && k <= reflect.Uint64:
		return kindUint, nil
	case k == reflect.Float32 || k == reflect.Float64:
		return kindFloat, nil
	}
	return "", fmt.Errorf("unsupported type %s (tag it `tune:\"-\"` to skip)", t)
}

func newField(sf reflect.StructField, key string, index []int, tag tune) (*field, error) {
	typ := sf.Type
	f := &field{key: key, index: index, typ: typ, name: tag.opts["name"], desc: tag.opts["desc"], source: tag.opts["source"]}
	if typ.Kind() == reflect.Pointer && f.source != "" { // one entry or none
		typ = typ.Elem()
		f.typ, f.ptr = typ, true
	}

	// target is the field the value options (enum, min, max, precision) belong to: the field itself, or the
	// item of a list or a set
	target := f
	if m, ok := reflect.Zero(typ).Interface().(setMarker); ok {
		f.kind = kindSet
		f.elem = &field{key: key, typ: m.setElem()}
	} else if typ.Kind() == reflect.Slice {
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("unsupported type %s: bytes are not settings (tag it `tune:\"-\"` to skip)", sf.Type)
		}
		f.kind = kindList
		f.elem = &field{key: key, typ: typ.Elem()}
	}
	if f.ptr && f.collection() {
		return nil, fmt.Errorf("a pointer to %s is not a setting: a list or a set is empty or not, it needs no pointer", typ)
	}
	if f.collection() {
		k, err := scalarKind(f.elem.typ)
		if err != nil {
			return nil, fmt.Errorf("an item of %s: %w", sf.Type, err)
		}
		f.elem.kind = k
		target = f.elem
	} else {
		k, err := scalarKind(typ)
		if err != nil {
			return nil, err
		}
		f.kind = k
	}

	if e, ok := tag.opts["enum"]; ok {
		if target.kind != kindString {
			return nil, fmt.Errorf("enum is only for string fields")
		}
		for _, o := range strings.Split(e, ",") {
			if o = strings.TrimSpace(o); o != "" {
				target.enum = append(target.enum, o)
			}
		}
		if len(target.enum) == 0 {
			return nil, fmt.Errorf("empty enum")
		}
		target.kind = kindEnum
	}

	for _, b := range []struct {
		opt string
		dst **float64
	}{{"min", &target.min}, {"max", &target.max}} {
		s, ok := tag.opts[b.opt]
		if !ok {
			continue
		}
		switch target.kind {
		case kindInt, kindUint, kindFloat:
			x, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil, fmt.Errorf("bad %s %q", b.opt, s)
			}
			*b.dst = &x
		case kindDuration:
			d, err := time.ParseDuration(s)
			if err != nil {
				return nil, fmt.Errorf("bad %s %q", b.opt, s)
			}
			x := float64(d)
			*b.dst = &x
		default:
			return nil, fmt.Errorf("%s is only for numbers and durations", b.opt)
		}
	}
	if target.min != nil && target.max != nil && *target.min > *target.max {
		return nil, fmt.Errorf("min is greater than max")
	}

	if p, ok := tag.opts["precision"]; ok {
		if target.kind != kindFloat {
			return nil, fmt.Errorf("precision is only for float fields")
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 10 {
			return nil, fmt.Errorf("precision must be a whole number from 0 to 10, got %q", p)
		}
		target.prec = &n
	}

	for _, b := range []struct {
		opt string
		dst **int
	}{{"minlen", &f.minLen}, {"maxlen", &f.maxLen}} {
		s, ok := tag.opts[b.opt]
		if !ok {
			continue
		}
		if !f.collection() {
			return nil, fmt.Errorf("%s is only for lists and sets", b.opt)
		}
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > maxItems {
			return nil, fmt.Errorf("%s must be a whole number from 0 to %d, got %q", b.opt, maxItems, s)
		}
		*b.dst = &n
	}
	if f.minLen != nil && f.maxLen != nil && *f.minLen > *f.maxLen {
		return nil, fmt.Errorf("minlen is greater than maxlen")
	}

	if c, ok := tag.opts["custom"]; ok {
		on, err := strconv.ParseBool(c)
		if err != nil {
			return nil, fmt.Errorf("custom must be true or false, got %q", c)
		}
		if on && f.source == "" {
			return nil, fmt.Errorf("custom goes with source")
		}
		f.custom = on
	}
	if f.source != "" {
		if k := target.kind; k != kindInt && k != kindUint && k != kindString {
			return nil, fmt.Errorf("source is only for ids: an integer or a string, not %s", k)
		}
		if target.min != nil || target.max != nil || target.prec != nil {
			return nil, fmt.Errorf("min, max and precision do not go with source")
		}
	}

	allowed := uiOptions[f.apiType()]
	if f.source != "" {
		allowed = []string{uiPicker}
	}
	f.ui = allowed[0]
	if u, ok := tag.opts["ui"]; ok {
		if !slices.Contains(allowed, u) {
			return nil, fmt.Errorf("ui:%s is not for %s fields (use %s)", u, f.apiType(), strings.Join(allowed, " or "))
		}
		f.ui = u
	}
	if f.ui == "slider" && (f.min == nil || f.max == nil) {
		return nil, fmt.Errorf("ui:slider needs min and max")
	}
	return f, nil
}

// read returns the canonical value of the field in fv.
func (f *field) read(fv reflect.Value) any {
	if f.ptr { // one entry or none: nil
		if fv.IsNil() {
			return nil
		}
		g := *f
		g.ptr = false
		return g.read(fv.Elem())
	}
	switch f.kind {
	case kindList:
		out := make([]any, fv.Len())
		for i := range out {
			out[i] = f.elem.read(fv.Index(i))
		}
		return out
	case kindSet:
		out := make([]any, 0, fv.Len())
		for it := fv.MapRange(); it.Next(); {
			out = append(out, f.elem.read(it.Key()))
		}
		slices.SortFunc(out, cmpAny)
		return out
	case kindBool:
		return fv.Bool()
	case kindString, kindEnum:
		return fv.String()
	case kindInt:
		return fv.Int()
	case kindUint:
		return fv.Uint()
	case kindFloat:
		x := fv.Float()
		if f.typ.Kind() == reflect.Float32 { // 0.1 as float32 should read as 0.1, not 0.10000000149
			if y, err := strconv.ParseFloat(strconv.FormatFloat(x, 'g', -1, 32), 64); err == nil {
				x = y
			}
		}
		return x
	default: // duration
		return time.Duration(fv.Int())
	}
}

// write stores a canonical value (already validated) into fv.
func (f *field) write(fv reflect.Value, v any) {
	if f.ptr {
		if v == nil {
			fv.SetZero()
			return
		}
		g := *f
		g.ptr = false
		p := reflect.New(f.typ)
		g.write(p.Elem(), v)
		fv.Set(p)
		return
	}
	switch f.kind {
	case kindList:
		items := v.([]any)
		if len(items) == 0 {
			fv.SetZero()
			return
		}
		out := reflect.MakeSlice(f.typ, len(items), len(items))
		for i, it := range items {
			f.elem.write(out.Index(i), it)
		}
		fv.Set(out)
	case kindSet:
		items := v.([]any)
		if len(items) == 0 {
			fv.SetZero()
			return
		}
		out := reflect.MakeMapWithSize(f.typ, len(items))
		for _, it := range items {
			k := reflect.New(f.elem.typ).Elem()
			f.elem.write(k, it)
			out.SetMapIndex(k, reflect.Zero(f.typ.Elem()))
		}
		fv.Set(out)
	case kindBool:
		fv.SetBool(v.(bool))
	case kindString, kindEnum:
		fv.SetString(v.(string))
	case kindInt:
		fv.SetInt(v.(int64))
	case kindUint:
		fv.SetUint(v.(uint64))
	case kindFloat:
		fv.SetFloat(v.(float64))
	default:
		fv.SetInt(int64(v.(time.Duration)))
	}
}

// parseText parses a tag value ("10", "true", "500ms", "blue") into a canonical value.
func (f *field) parseText(s string) (any, error) {
	if f.collection() {
		return f.parseTextList(s)
	}
	raw := json.RawMessage(s)
	if f.kind == kindString || f.kind == kindEnum || f.kind == kindDuration {
		b, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return f.parse(raw)
}

// parseTextList parses the default of a list or a set: a JSON array ([1,2,3], ["a","b"]), or the items
// separated by commas (1,2,3).
func (f *field) parseTextList(s string) (any, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "[") {
		return f.parse(json.RawMessage(s))
	}
	quote := f.elem.kind == kindString || f.elem.kind == kindEnum || f.elem.kind == kindDuration
	items := []json.RawMessage{}
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		if quote {
			b, err := json.Marshal(part)
			if err != nil {
				return nil, err
			}
			items = append(items, b)
		} else {
			items = append(items, json.RawMessage(part))
		}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("bad item in %q: %w", s, err)
	}
	return f.parse(raw)
}

// parseList validates a JSON array for a list or a set. Every item goes through the checks of the item
// field; a set drops repeats and is kept sorted.
func (f *field) parseList(raw json.RawMessage) (any, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("expected a list of %s", f.elem.apiType())
	}
	if len(items) > maxItems {
		return nil, fmt.Errorf("at most %d items", maxItems)
	}
	out := make([]any, 0, len(items))
	seen := map[any]bool{}
	for i, r := range items {
		v, err := f.elem.parse(r)
		if err != nil {
			return nil, fmt.Errorf("item %d: %w", i+1, err)
		}
		if f.kind == kindSet {
			if seen[v] {
				continue
			}
			seen[v] = true
		}
		out = append(out, v)
	}
	if f.kind == kindSet {
		slices.SortFunc(out, cmpAny)
	}
	if err := f.checkLen(len(out)); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *field) checkLen(n int) error {
	if f.minLen != nil && n < *f.minLen {
		return fmt.Errorf("needs at least %d items, got %d", *f.minLen, n)
	}
	if f.maxLen != nil && n > *f.maxLen {
		return fmt.Errorf("at most %d items, got %d", *f.maxLen, n)
	}
	return nil
}

// cmpAny orders two canonical values of the same kind (and the items of a Set when it is written).
func cmpAny(a, b any) int {
	x, y := reflect.ValueOf(a), reflect.ValueOf(b)
	switch x.Kind() {
	case reflect.Bool:
		return cmp.Compare(btoi(x.Bool()), btoi(y.Bool()))
	case reflect.String:
		return cmp.Compare(x.String(), y.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return cmp.Compare(x.Int(), y.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return cmp.Compare(x.Uint(), y.Uint())
	case reflect.Float32, reflect.Float64:
		return cmp.Compare(x.Float(), y.Float())
	}
	return 0
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// same reports whether two canonical values of this field are equal.
func (f *field) same(a, b any) bool {
	if f.collection() {
		return slices.Equal(a.([]any), b.([]any))
	}
	return a == b
}

// parse validates a JSON value for this field and returns it in canonical form.
func (f *field) parse(raw json.RawMessage) (any, error) {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		if f.ptr && s == "null" { // nothing chosen
			return nil, nil
		}
		return nil, fmt.Errorf("expected %s", f.apiType())
	}
	if f.collection() {
		return f.parseList(raw)
	}
	var v any
	var err error
	switch f.kind {
	case kindBool:
		var b bool
		err = json.Unmarshal(raw, &b)
		v = b
	case kindString, kindEnum:
		var s string
		err = json.Unmarshal(raw, &s)
		v = s
	case kindInt:
		var n int64
		if err = json.Unmarshal(raw, &n); err == nil && reflect.Zero(f.typ).OverflowInt(n) {
			return nil, fmt.Errorf("%d overflows %s", n, f.typ)
		}
		v = n
	case kindUint:
		var n uint64
		if err = json.Unmarshal(raw, &n); err == nil && reflect.Zero(f.typ).OverflowUint(n) {
			return nil, fmt.Errorf("%d overflows %s", n, f.typ)
		}
		v = n
	case kindFloat:
		var x float64
		if err = json.Unmarshal(raw, &x); err == nil && f.typ.Kind() == reflect.Float32 && reflect.Zero(f.typ).OverflowFloat(x) {
			return nil, fmt.Errorf("%g overflows %s", x, f.typ)
		}
		v = x
	case kindDuration:
		var s string
		if err = json.Unmarshal(raw, &s); err == nil {
			var d time.Duration
			if d, err = time.ParseDuration(s); err != nil {
				return nil, fmt.Errorf("bad duration %q (use 500ms, 30s, 5m)", s)
			}
			v = d
		}
	}
	if err != nil {
		return nil, fmt.Errorf("expected %s", f.apiType())
	}
	if err := f.check(v); err != nil {
		return nil, err
	}
	return v, nil
}

// check validates a canonical value against enum and min/max.
func (f *field) check(v any) error {
	if v == nil { // a pointer with nothing chosen
		return nil
	}
	if f.collection() {
		items := v.([]any)
		if err := f.checkLen(len(items)); err != nil {
			return err
		}
		for i, it := range items {
			if err := f.elem.check(it); err != nil {
				return fmt.Errorf("item %d: %w", i+1, err)
			}
		}
		return nil
	}
	if f.kind == kindEnum {
		if s := v.(string); !slices.Contains(f.enum, s) {
			return fmt.Errorf("%q is not one of %s", s, strings.Join(f.enum, ", "))
		}
	}
	if f.min == nil && f.max == nil {
		return nil
	}
	var x float64
	switch n := v.(type) {
	case int64:
		x = float64(n)
	case uint64:
		x = float64(n)
	case float64:
		x = n
	case time.Duration:
		x = float64(n)
	default:
		return nil
	}
	if f.min != nil && x < *f.min {
		return fmt.Errorf("%s is below the minimum %s", display(f, v), display(f, *f.min))
	}
	if f.max != nil && x > *f.max {
		return fmt.Errorf("%s is above the maximum %s", display(f, v), display(f, *f.max))
	}
	return nil
}

// display formats a canonical value or a min/max bound (float64) for messages.
func display(f *field, v any) string {
	if x, ok := v.(float64); ok && f.kind == kindDuration {
		v = time.Duration(x)
	}
	return fmt.Sprint(v)
}

// jsonValue converts a canonical value to what goes over the wire.
func jsonValue(v any) any {
	switch x := v.(type) {
	case time.Duration:
		return x.String()
	case []any:
		out := make([]any, len(x))
		for i, it := range x {
			out[i] = jsonValue(it)
		}
		return out
	}
	return v
}

// bound converts a min/max bound to what goes over the wire (nil when unset).
func (f *field) bound(b *float64) any {
	if b == nil {
		return nil
	}
	if f.kind == kindDuration {
		return time.Duration(*b).String()
	}
	return *b
}
