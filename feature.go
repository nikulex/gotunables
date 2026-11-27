package tunables

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unsafe"
)

// Rule is one thing a targeting is about (the country, the browser, the account provider...): the values that are in
// it and the values that are kept out. Both are sets, so everything that goes for a Set goes for them (`enum`, `source`,
// `custom`, minlen/maxlen... on the Rule's tag are the options of its two sets).
//
//	Country tunables.Rule[string] `json:"country" tune:"name:Country; desc:ISO codes: ua, rs"`
//
// A value matches a Rule when Include is empty or has it, and Exclude does not have it. A Rule with nothing in it
// matches everything.
type Rule[V comparable] struct {
	Include Set[V] `json:"include" tune:"name:Only"`
	Exclude Set[V] `json:"exclude" tune:"name:Except"`
}

// Match reports whether the value is in the Rule.
func (r Rule[V]) Match(v V) bool {
	return (r.Include.Len() == 0 || r.Include.Has(v)) && !r.Exclude.Has(v)
}

// MatchAny is Match for something that has several values (the languages of a browser): one of them has to be in, and
// none of them may be out.
func (r Rule[V]) MatchAny(values ...V) bool {
	in := r.Include.Len() == 0
	for _, v := range values {
		if r.Exclude.Has(v) {
			return false
		}
		if r.Include.Has(v) {
			in = true
		}
	}
	return in
}

func (Rule[V]) ruleElem() reflect.Type { return reflect.TypeFor[V]() }

// The page draws a Rule as one card with its two sets side by side.
func (Rule[V]) groupKind() groupKind { return kindRule }

// ruleMarker is how the Rules of a targeting are found.
type ruleMarker interface{ ruleElem() reflect.Type }

// Group is one named targeting of a Feature: the Rules of a kind of request ("Serbia, mobile", "Beta testers"). The name is for
// the people who read the settings.
type Group[T any] struct {
	ID        string `json:"id"`                // what the page and the API call it: letters, digits, '-' and '_'; made when it is empty
	Name      string `json:"name"`              // what is shown, and what the metrics call the group that let a request in; "Group A", "Group B"... when none is given
	Percent   *int   `json:"percent,omitempty"` // the share of the traffic the group is for, 0 to 100: all of it when it is nil
	Random    bool   `json:"random,omitempty"`  // the share is by the luck of each check, not by the key of the info
	Targeting T      `json:"targeting"`
}

func (Group[T]) groupTargeting() reflect.Type { return reflect.TypeFor[T]() }

// groupMarker is how the Groups of a Feature are found.
type groupMarker interface{ groupTargeting() reflect.Type }

// label is how a group is called in the metrics: its name, or its id when it has none.
func (g *Group[T]) label() string {
	if g.Name != "" {
		return g.Name
	}
	return g.ID
}

// Feature is a setting that is on or off, and, when it is on, only for those that its targeting lets in. The targeting is
// Groups: each a named set of Rules (T is a struct of Rules: the targeting, see the httptargeting package for the one of a web
// request; embed structs into it to extend it). A request is in the targeting when it is in any of the groups, and a
// group is one that has every one of its Rules letting the request in. A Feature with no group is for everybody.
//
//	type Config struct {
//		Features struct {
//			NewCheckout tunables.Feature[Targeting] `json:"new_checkout" tune:"name:New checkout"`
//		} `json:"features"`
//	}
//
//	info := Info{Country: "rs"} // any struct that has a field for each Rule (see the httptargeting module for a web request's)
//	if t.Get().Features.NewCheckout.IsEnabled(info) { // enabled, and the request is in a group
//
// In the settings the groups are added on the page, each with a name and its own Rules; they are kept as one value,
// features.new_checkout.groups. A program that wants to know how often a feature is asked about, and what the answer was,
// gives tunables.New a WithObserver.
type Feature[T any] struct {
	Enabled bool       `json:"enabled" tune:"name:Enabled"`
	Groups  []Group[T] `json:"groups"`

	meta *featureMeta // set by the Tunables the feature is in: where the checks are reported
}

// Check is what an observer is told of a call of IsEnabled.
type Check struct {
	Feature string // the key of the feature: "features.new_checkout"
	Group   string // the group that let the request in (its name, or its id): empty when the feature has no groups, or when no group did
	Enabled bool   // the answer
}

// featureMeta is what a Feature of a Tunables knows of it.
type featureMeta struct {
	key     string
	observe func(Check)
}

// name is what the feature is called by a share of traffic: its key, or nothing when it is not in a Tunables.
func (f *Feature[T]) name() string {
	if f.meta == nil {
		return ""
	}
	return f.meta.key
}

func (f *Feature[T]) report(on bool, g *Group[T]) {
	if f.meta == nil || f.meta.observe == nil {
		return
	}
	c := Check{Feature: f.meta.key, Enabled: on}
	if g != nil {
		c.Group = g.label()
	}
	f.meta.observe(c)
}

// IsEnabled reports whether the feature is on for what the info says: it is enabled and the info is in a group of the targeting
// (see Targets for what the info is). For a place that is hot, make a Matcher.
func (f *Feature[T]) IsEnabled(info any) bool {
	if !f.Enabled {
		f.report(false, nil)
		return false
	}
	g, ok := f.find(info)
	f.report(ok, g)
	return ok
}

// Targets reports whether the info is in the targeting (in a group of it, or the feature has none), whether the feature is
// enabled or not.
//
// info is a struct (or a pointer to one) that has a field for each Rule, of the same name: a value of the Rule's type,
// or a slice of them (the languages). A field that is not there, or holds nothing (an empty text, 0), is unknown: a Rule
// that names who is in (Include) does not match it, one that only names who is out does. Embedded structs of the info
// work as they do for any Go struct, which is how the info is extended: embed the info of a web request into your own.
//
// The reflection that this needs is done once for a pair of types and kept; after that a check is a lookup of the plan, a
// copy of the info (unless it is a pointer) and the checks of the Rules. A Matcher is without even those.
func (f *Feature[T]) Targets(info any) bool {
	_, ok := f.find(info)
	return ok
}

// find is the first group the info is in (nil, true: there are no groups; nil, false: it is in none).
func (f *Feature[T]) find(info any) (*Group[T], bool) {
	if len(f.Groups) == 0 {
		return nil, true
	}
	iv := reflect.ValueOf(info)
	for iv.IsValid() && iv.Kind() == reflect.Interface {
		iv = iv.Elem()
	}
	var ip unsafe.Pointer
	var it reflect.Type
	switch {
	case !iv.IsValid() || (iv.Kind() == reflect.Pointer && iv.IsNil()): // nothing is known
		info = nil
	case iv.Kind() == reflect.Pointer && iv.Elem().Kind() == reflect.Struct:
		it, ip = iv.Type().Elem(), iv.UnsafePointer()
	case iv.Kind() == reflect.Struct:
		it = iv.Type()
		cp := reflect.New(it) // the checks go by the address of the struct
		cp.Elem().Set(iv)
		ip = cp.UnsafePointer()
	default:
		info = nil
	}
	if it != nil {
		if p := planOf(reflect.TypeFor[T](), it); p.ok {
			var key func(uint32) (uint32, bool)
			if p.key != nil {
				key = func(seed uint32) (uint32, bool) { return p.key(ip, seed) }
			}
			for i := range f.Groups {
				g := &f.Groups[i]
				if runRules(p.rules, unsafe.Pointer(&g.Targeting), ip) && g.inShare(f.name(), key) {
					return g, true
				}
			}
			return nil, false
		}
	}
	var key func(uint32) (uint32, bool)
	if kv, ok := infoKeyValue(info); ok {
		key = func(seed uint32) (uint32, bool) { return keyHashOfValue(kv, seed) }
	}
	for i := range f.Groups {
		g := &f.Groups[i]
		if targets(reflect.ValueOf(g.Targeting), info) && g.inShare(f.name(), key) {
			return g, true
		}
	}
	return nil, false
}

// The page adds the groups of a Feature, and the rules of a group from a list, and folds them away while it is off.
func (Feature[T]) groupKind() groupKind { return kindFeature }

// CheckInfo tells whether the info type I and the targeting T go together, both ways: every Rule of T has a field of the
// same name in I, of the type of the Rule's values (or a slice of it), and every exported field of I has a Rule of its own
// in T. A mistake there would not be seen: a Rule that finds nothing in the info does nothing, and a field of the info that
// no Rule is for is something that cannot be targeted. A field that is not meant for targeting says so with the tag
// `target:"-"`; the fields of embedded structs count as the info's own. Call it once when the program starts.
func CheckInfo[T, I any]() error {
	it := reflect.TypeFor[I]()
	for it.Kind() == reflect.Pointer {
		it = it.Elem()
	}
	if it.Kind() != reflect.Struct {
		return fmt.Errorf("tunables: the info must be a struct, got %s", it)
	}
	var problems []string
	keys := infoKeysOf(it)
	if len(keys) > 1 {
		problems = append(problems, fmt.Sprintf("%s has %d fields tagged target:\"key\", only one is the key", it, len(keys)))
	}
	for _, k := range keys {
		if !validKeyType(k.field.Type) {
			problems = append(problems, fmt.Sprintf("%s.%s is the key but it is %s: a text or a whole number is", it, k.field.Name, k.field.Type))
		}
	}
	infoFields := infoFieldsOf(it)
	rules := rulesOf(reflect.TypeFor[T]())
	ruleNames := make(map[string]bool, len(rules))
	for _, r := range rules {
		ruleNames[r.name] = true
		f, ok := infoFields[r.name]
		sf := f.field
		if !ok { // a field the info has but that is not for targeting does not answer a Rule either
			if _, own := it.FieldByName(r.name); !own {
				problems = append(problems, fmt.Sprintf("%s has no field %s (%s) for the Rule", it, r.name, r.elem))
			} else {
				problems = append(problems, fmt.Sprintf("%s.%s is tagged target:\"-\", but there is a Rule for it", it, r.name))
			}
			continue
		}
		if sf.Type != r.elem && sf.Type != reflect.SliceOf(r.elem) {
			problems = append(problems, fmt.Sprintf("%s.%s is %s, the Rule is of %s", it, r.name, sf.Type, r.elem))
		}
	}
	names := make([]string, 0, len(infoFields))
	for name := range infoFields {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !ruleNames[name] && infoFields[name].field.Tag.Get("target") != "key" {
			problems = append(problems, fmt.Sprintf("%s.%s has no Rule in %s (tag it target:\"-\" if it is not for targeting)", it, name, reflect.TypeFor[T]()))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("tunables: %s", strings.Join(problems, "; "))
	}
	return nil
}

// infoField is a field of an info struct that is for targeting, with the way to it from the info itself.
type infoField struct {
	field reflect.StructField
	index []int // from the info struct, through the embedded ones
}

// infoFieldsOf are the exported fields of an info struct that are for targeting (not tagged `target:"-"`), by name; the
// fields of embedded structs are its own.
func infoFieldsOf(t reflect.Type) map[string]infoField {
	out := map[string]infoField{}
	var walk func(t reflect.Type, path []int)
	walk = func(t reflect.Type, path []int) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			idx := append(append([]int{}, path...), i)
			if sf.Anonymous {
				et := sf.Type
				for et.Kind() == reflect.Pointer {
					et = et.Elem()
				}
				if et.Kind() == reflect.Struct { // its fields are the info's, whether the embedded type is exported or not
					walk(et, idx)
					continue
				}
			}
			if !sf.IsExported() || sf.Tag.Get("target") == "-" {
				continue
			}
			if _, ok := out[sf.Name]; !ok { // the field of the outer struct hides the one of the embedded, as in Go
				out[sf.Name] = infoField{field: sf, index: idx}
			}
		}
	}
	walk(t, nil)
	return out
}

// rulePlan is one Rule of a targeting: where it is, how its info is named and of what type its values are.
type rulePlan struct {
	index []int
	name  string
	elem  reflect.Type
}

var plans sync.Map // reflect.Type of a targeting -> []rulePlan

// rulesOf finds the Rules of a targeting struct, in the structs inside it too (embedded or not).
func rulesOf(t reflect.Type) []rulePlan {
	if p, ok := plans.Load(t); ok {
		return p.([]rulePlan)
	}
	var out []rulePlan
	var walk func(t reflect.Type, index []int)
	walk = func(t reflect.Type, index []int) {
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if !sf.IsExported() && !sf.Anonymous {
				continue
			}
			idx := append(append([]int{}, index...), i)
			if m, ok := reflect.Zero(sf.Type).Interface().(ruleMarker); ok {
				out = append(out, rulePlan{index: idx, name: sf.Name, elem: m.ruleElem()})
			} else if sf.Type.Kind() == reflect.Struct {
				walk(sf.Type, idx)
			}
		}
	}
	if t.Kind() == reflect.Struct {
		walk(t, nil)
	}
	plans.Store(t, out)
	return out
}

// targets reports whether the info is in every Rule of the targeting value tv.
func targets(tv reflect.Value, info any) bool {
	iv := reflect.ValueOf(info)
	for iv.IsValid() && (iv.Kind() == reflect.Pointer || iv.Kind() == reflect.Interface) {
		if iv.IsNil() {
			iv = reflect.Value{}
			break
		}
		iv = iv.Elem()
	}
	for _, r := range rulesOf(tv.Type()) {
		rule := tv.FieldByIndex(r.index)
		include, exclude := rule.Field(0), rule.Field(1)
		if include.Len() == 0 && exclude.Len() == 0 {
			continue // nothing asked
		}
		values := infoValues(iv, r)
		if len(values) == 0 { // not known
			if include.Len() > 0 {
				return false
			}
			continue
		}
		in := include.Len() == 0
		for _, v := range values {
			if exclude.MapIndex(v).IsValid() {
				return false
			}
			if include.MapIndex(v).IsValid() {
				in = true
			}
		}
		if !in {
			return false
		}
	}
	return true
}

// infoValues are the values the info has for a Rule (none when it has no such field or it holds nothing).
func infoValues(iv reflect.Value, r rulePlan) []reflect.Value {
	if !iv.IsValid() || iv.Kind() != reflect.Struct {
		return nil
	}
	sf, ok := iv.Type().FieldByName(r.name)
	if !ok || !sf.IsExported() {
		return nil
	}
	fv, err := iv.FieldByIndexErr(sf.Index) // an embedded pointer that is nil has nothing
	if err != nil {
		return nil
	}
	switch {
	case fv.Type() == r.elem:
		if fv.IsZero() {
			return nil
		}
		return []reflect.Value{fv}
	case fv.Type() == reflect.SliceOf(r.elem):
		out := make([]reflect.Value, 0, fv.Len())
		for i := 0; i < fv.Len(); i++ {
			if !fv.Index(i).IsZero() {
				out = append(out, fv.Index(i))
			}
		}
		return out
	}
	return nil
}

// Matcher checks the info of a request against a Feature with no reflection and nothing allocated for each check: what
// reflection is needed (where each Rule is in the targeting, where its info is in the info) is worked out once, when the
// Matcher is made. Make one for each pair of a targeting and an info, at the start of the program, and keep it.
//
//	m, err := tunables.NewMatcher[Targeting, Info]() // the targeting and the info are checked against each other, as CheckInfo does
//	...
//	info := Info{Info: web.Parse(r), UserID: id}
//	if m.IsEnabled(&t.Get().Features.NewCheckout, &info) { ... }
//
// Feature.IsEnabled(info any) is the same without making one: it finds the plan of the two types by itself, once (the reflection is
// cached), and costs a lookup and a copy of the info for each check.
type Matcher[T, I any] struct {
	rules []ruleEval
	key   keyHash // nil: the info has no key
}

// NewMatcher prepares the checks of the info type I against the targeting T. It fails for what CheckInfo finds, and for an
// info or a targeting that has a Rule or a field behind an embedded pointer (embed by value: the checks go by offsets).
func NewMatcher[T, I any]() (*Matcher[T, I], error) {
	if err := CheckInfo[T, I](); err != nil {
		return nil, err
	}
	it := reflect.TypeFor[I]()
	if it.Kind() == reflect.Pointer {
		return nil, fmt.Errorf("tunables: the info of a Matcher is the struct itself, not %s", it)
	}
	rules, _, err := compileRules(reflect.TypeFor[T](), it, true)
	if err != nil {
		return nil, err
	}
	key, ok, err := compileKey(it)
	if err == nil && !ok {
		err = fmt.Errorf("the key is behind an embedded pointer: embed the struct itself")
	}
	if err != nil {
		return nil, fmt.Errorf("tunables: info %s: %w", it, err)
	}
	return &Matcher[T, I]{rules: rules, key: key}, nil
}

// Targets reports whether the info is in the targeting, with no reflection and nothing allocated: in a group of it, or the
// feature has none.
func (m *Matcher[T, I]) Targets(f *Feature[T], info *I) bool {
	_, ok := m.find(f, info)
	return ok
}

func (m *Matcher[T, I]) find(f *Feature[T], info *I) (*Group[T], bool) {
	if len(f.Groups) == 0 {
		return nil, true
	}
	ip := unsafe.Pointer(info)
	var key func(uint32) (uint32, bool)
	if m.key != nil {
		key = func(seed uint32) (uint32, bool) { return m.key(ip, seed) }
	}
	for i := range f.Groups {
		g := &f.Groups[i]
		if runRules(m.rules, unsafe.Pointer(&g.Targeting), ip) && g.inShare(f.name(), key) {
			return g, true
		}
	}
	return nil, false
}

// IsEnabled reports whether the feature is enabled and the info is in a group of its targeting (see Feature.IsEnabled).
func (m *Matcher[T, I]) IsEnabled(f *Feature[T], info *I) bool {
	if !f.Enabled {
		f.report(false, nil)
		return false
	}
	g, ok := m.find(f, info)
	f.report(ok, g)
	return ok
}

// ruleEval is the check of one Rule: whether it lets in the info. Both are pointers to the structs they are in.
type ruleEval func(targeting, info unsafe.Pointer) bool

func runRules(rules []ruleEval, tp, ip unsafe.Pointer) bool {
	for _, rule := range rules {
		if !rule(tp, ip) {
			return false
		}
	}
	return true
}

// Ways a Rule reads its info.
const (
	readValue   = iota // the info has a value of the Rule's type
	readSlice          // the info has a slice of them
	readUnknown        // the info has nothing for the Rule: it is not known
)

// compileRules makes the check of every Rule of the targeting type tt against the info type it. Strict: a Rule that the info
// has nothing for is a mistake. Not strict: it is unknown, as it is for Feature.IsEnabled; ok is false when a path to a field goes
// behind an embedded pointer, which an offset cannot follow (then the info is read by reflection).
func compileRules(tt, it reflect.Type, strict bool) (rules []ruleEval, ok bool, err error) {
	infoFields := infoFieldsOf(it)
	ok = true
	for _, r := range rulesOf(tt) {
		ruleOff, err := offsetOf(tt, r.index)
		if err != nil {
			return nil, false, fmt.Errorf("tunables: rule %s: %w", r.name, err)
		}
		mode, infoOff := readUnknown, uintptr(0)
		if f, found := infoFields[r.name]; found {
			switch {
			case f.field.Type == r.elem:
				mode = readValue
			case f.field.Type == reflect.SliceOf(r.elem):
				mode = readSlice
			}
			if mode != readUnknown {
				if infoOff, err = offsetOf(it, f.index); err != nil {
					if strict {
						return nil, false, fmt.Errorf("tunables: info field %s: %w", r.name, err)
					}
					ok = false
				}
			}
		}
		if mode == readUnknown && strict {
			return nil, false, fmt.Errorf("tunables: info %s has nothing for the Rule %s", it, r.name)
		}
		c, isRule := reflect.Zero(tt.FieldByIndex(r.index).Type).Interface().(ruleCompiler)
		if !isRule {
			return nil, false, fmt.Errorf("tunables: rule %s is not a Rule", r.name)
		}
		rules = append(rules, c.compile(ruleOff, infoOff, mode))
	}
	return rules, ok, nil
}

// offsetOf is where the field at the path is in the struct, in bytes. A struct behind a pointer on the way has no offset.
func offsetOf(t reflect.Type, index []int) (uintptr, error) {
	var off uintptr
	for i, n := range index {
		f := t.Field(n)
		off += f.Offset
		t = f.Type
		if i < len(index)-1 && t.Kind() == reflect.Pointer {
			return 0, fmt.Errorf("%s is an embedded pointer: embed the struct itself", f.Name)
		}
	}
	return off, nil
}

// plan is what is known of a targeting type and an info type once they have met: the checks of the Rules.
type plan struct {
	rules []ruleEval
	key   keyHash
	ok    bool // false: read the info by reflection
}

type planKey struct{ targeting, info reflect.Type }

var plansOfPairs sync.Map // planKey -> *plan

// planOf is the plan of a targeting and an info type, made the first time and then looked up.
func planOf(tt, it reflect.Type) *plan {
	pair := planKey{tt, it}
	if p, ok := plansOfPairs.Load(pair); ok {
		return p.(*plan)
	}
	rules, ok, err := compileRules(tt, it, false)
	key, keyOK, keyErr := compileKey(it)
	p := &plan{rules: rules, key: key, ok: ok && err == nil && keyOK && keyErr == nil}
	plansOfPairs.Store(pair, p)
	return p
}

// ruleCompiler is a Rule that makes its own check, for the type of its values: the check is Go code of that type, which is
// why it has no reflection in it.
type ruleCompiler interface {
	compile(ruleOff, infoOff uintptr, mode int) ruleEval
}

func (Rule[V]) compile(ruleOff, infoOff uintptr, mode int) ruleEval {
	var zero V
	switch mode {
	case readSlice:
		return func(tp, ip unsafe.Pointer) bool {
			r := (*Rule[V])(unsafe.Add(tp, ruleOff))
			if len(r.Include) == 0 && len(r.Exclude) == 0 {
				return true
			}
			in, known := len(r.Include) == 0, false
			for _, v := range *(*[]V)(unsafe.Add(ip, infoOff)) {
				if v == zero {
					continue
				}
				known = true
				if _, out := r.Exclude[v]; out {
					return false
				}
				if _, ok := r.Include[v]; ok {
					in = true
				}
			}
			if !known {
				return len(r.Include) == 0
			}
			return in
		}
	case readValue:
		return func(tp, ip unsafe.Pointer) bool {
			r := (*Rule[V])(unsafe.Add(tp, ruleOff))
			if len(r.Include) == 0 && len(r.Exclude) == 0 {
				return true
			}
			v := *(*V)(unsafe.Add(ip, infoOff))
			if v == zero { // not known: it is not in a list of those that are in, and not one that is out
				return len(r.Include) == 0
			}
			if _, out := r.Exclude[v]; out {
				return false
			}
			if len(r.Include) == 0 {
				return true
			}
			_, ok := r.Include[v]
			return ok
		}
	default: // the info knows nothing of it
		return func(tp, _ unsafe.Pointer) bool {
			r := (*Rule[V])(unsafe.Add(tp, ruleOff))
			return len(r.Include) == 0
		}
	}
}
