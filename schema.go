package tunables

import (
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
)

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
	def   any // canonical default: bool, string, int64, uint64, float64 or time.Duration
	enum  []string
	min   *float64 // durations are in nanoseconds
	max   *float64
}

// group is a nested struct that has a display name or description.
type group struct{ key, name, desc string }

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
// Options are separated by ";", each is key:value. Known keys: key, name, desc, default, enum, min, max, ui, precision.
// `tune:"-"` skips the field.
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
		if !ok {
			return tune{}, fmt.Errorf("tune option %q must be key:value", part)
		}
		k = strings.ToLower(strings.TrimSpace(k))
		switch k {
		case "key", "name", "desc", "default", "enum", "min", "max", "ui", "precision":
		default:
			return tune{}, fmt.Errorf("unknown tune option %q (known: key, name, desc, default, enum, min, max, ui, precision)", k)
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
}

type walker struct {
	seen   map[string]bool
	fields []*field
	groups []*group
}

// walkFields reflects over the struct behind v (which must be addressable) and returns its leaves in
// declaration order. Defaults are the values already in v; a `default` option overrides (and is written into v).
func walkFields(t reflect.Type, v reflect.Value) ([]*field, []*group, error) {
	w := &walker{seen: map[string]bool{}}
	if err := w.walk(t, v, "", nil, 0); err != nil {
		return nil, nil, err
	}
	return w.fields, w.groups, nil
}

func (w *walker) walk(t reflect.Type, v reflect.Value, prefix string, index []int, depth int) error {
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
		if strings.Contains(name, ".") {
			return fmt.Errorf("tunables: field %s: name %q must not contain a dot", where, name)
		}
		key := join(prefix, name) // an unnamed embedded struct is flattened: key == prefix
		idx := append(slices.Clone(index), i)
		fv := v.Field(i)

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
				w.groups = append(w.groups, &group{key: key, name: gname, desc: tag.opts["desc"]})
			}
			if err := w.walk(sf.Type, fv, key, idx, inner); err != nil {
				return err
			}
			continue
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

func newField(sf reflect.StructField, key string, index []int, tag tune) (*field, error) {
	f := &field{key: key, index: index, typ: sf.Type, name: tag.opts["name"], desc: tag.opts["desc"]}

	switch k := sf.Type.Kind(); {
	case k == reflect.Bool:
		f.kind = kindBool
	case k == reflect.String:
		f.kind = kindString
	case sf.Type == durationType:
		f.kind = kindDuration
	case k >= reflect.Int && k <= reflect.Int64:
		f.kind = kindInt
	case k >= reflect.Uint && k <= reflect.Uint64:
		f.kind = kindUint
	case k == reflect.Float32 || k == reflect.Float64:
		f.kind = kindFloat
	default:
		return nil, fmt.Errorf("unsupported type %s (tag it `tune:\"-\"` to skip)", sf.Type)
	}

	if e, ok := tag.opts["enum"]; ok {
		if f.kind != kindString {
			return nil, fmt.Errorf("enum is only for string fields")
		}
		for _, o := range strings.Split(e, ",") {
			if o = strings.TrimSpace(o); o != "" {
				f.enum = append(f.enum, o)
			}
		}
		if len(f.enum) == 0 {
			return nil, fmt.Errorf("empty enum")
		}
		f.kind = kindEnum
	}

	for _, b := range []struct {
		opt string
		dst **float64
	}{{"min", &f.min}, {"max", &f.max}} {
		s, ok := tag.opts[b.opt]
		if !ok {
			continue
		}
		switch f.kind {
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
	if f.min != nil && f.max != nil && *f.min > *f.max {
		return nil, fmt.Errorf("min is greater than max")
	}

	if p, ok := tag.opts["precision"]; ok {
		if f.kind != kindFloat {
			return nil, fmt.Errorf("precision is only for float fields")
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 10 {
			return nil, fmt.Errorf("precision must be a whole number from 0 to 10, got %q", p)
		}
		f.prec = &n
	}

	allowed := uiOptions[f.apiType()]
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
	switch f.kind {
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
	switch f.kind {
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

// parse validates a JSON value for this field and returns it in canonical form.
func (f *field) parse(raw json.RawMessage) (any, error) {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return nil, fmt.Errorf("expected %s", f.apiType())
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
	if d, ok := v.(time.Duration); ok {
		return d.String()
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
