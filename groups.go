package tunables

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
	"unsafe"
)

const (
	maxGroups     = 50  // the groups of one feature
	maxGroupName  = 100 // characters of a name
	groupIDLength = 32
)

var groupIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// The fields of a Group, in the order they are declared.
const (
	groupID        = 0
	groupName      = 1
	groupPercent   = 2
	groupRandom    = 3
	groupTargeting = 4
)

// featureRef is a Feature in the settings struct: where it is, and what it is called.
type featureRef struct {
	key   string
	index []int
}

// groupSet is the Groups of one Feature: a list of named targetings that is kept, read and changed as one value
// (features.new_checkout.groups), so that a change of it is one step, and never a group with its Rules not yet there.
type groupSet struct {
	key     string // "features.new_checkout.groups"
	feature string // "features.new_checkout"
	index   []int  // the slice in the settings struct
	typ     reflect.Type
	targets reflect.Type
	fields  []*field // the leaves of the targeting, their keys relative to a group: "country.include"
	groups  []*group // the Rules of the targeting (and the structs around them), their keys relative too
	def     reflect.Value
}

// newGroupSet reads the template of the groups: the fields of one targeting.
func newGroupSet(key, feature string, index []int, typ, targeting reflect.Type) (*groupSet, error) {
	if targeting.Kind() != reflect.Struct {
		return nil, fmt.Errorf("tunables: field %s: the targeting of the groups must be a struct, got %s", key, targeting)
	}
	w, err := walkAll(targeting, reflect.New(targeting).Elem())
	if err != nil {
		return nil, fmt.Errorf("tunables: field %s: %w", key, err)
	}
	if len(w.groupSets) > 0 || len(w.features) > 0 {
		return nil, fmt.Errorf("tunables: field %s: a targeting has no features in it", key)
	}
	return &groupSet{key: key, feature: feature, index: index, typ: typ, targets: targeting, fields: w.fields, groups: w.groups}, nil
}

func newGroupID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil { // never: the system's source of randomness
		return "g" + hex.EncodeToString([]byte(fmt.Sprint(len(b))))
	}
	return "g" + hex.EncodeToString(b[:])
}

// parse reads the groups of a change: the JSON the page and the API send (the same as is kept), and checks them.
func (gs *groupSet) parse(c *core, raw json.RawMessage) (reflect.Value, error) {
	sl := reflect.New(gs.typ)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(sl.Interface()); err != nil {
		return reflect.Value{}, &valueError{key: gs.key, reason: "groups must be a list of {id, name, percent, random, targeting}: " + err.Error()}
	}
	out := sl.Elem()
	if out.IsNil() {
		out = reflect.MakeSlice(gs.typ, 0, 0)
	}
	if err := gs.check(c, out, true); err != nil {
		return reflect.Value{}, err
	}
	return out, nil
}

// check validates the groups, one by one: strict (a change) is an error with what is wrong, else (what is loaded) a group that
// is wrong is made right: an id is made, a value that is not valid is emptied.
func (gs *groupSet) check(c *core, sl reflect.Value, strict bool) error {
	bad := func(format string, args ...any) error {
		return &valueError{key: gs.key, reason: fmt.Sprintf(format, args...)}
	}
	if sl.Len() > maxGroups {
		return bad("at most %d groups, got %d", maxGroups, sl.Len())
	}
	seen := map[string]bool{}
	named := map[string]bool{} // the names there are: a group that has none is given one that is not among them
	for i := 0; i < sl.Len(); i++ {
		g := sl.Index(i).Field(groupName)
		name := strings.TrimSpace(g.String())
		g.SetString(name)
		if name != "" {
			named[name] = true
		}
	}
	for i := 0; i < sl.Len(); i++ {
		g := sl.Index(i)
		id := g.Field(groupID).String()
		switch {
		case id == "" && !strict, id == "":
			id = newGroupID()
		case !groupIDPattern.MatchString(id):
			if strict {
				return bad("group %d: the id must be letters, digits, '-' or '_', at most %d characters", i+1, groupIDLength)
			}
			id = newGroupID()
		}
		for seen[id] {
			if strict && g.Field(groupID).String() == id {
				return bad("group %d: the id %q is there twice", i+1, id)
			}
			id = newGroupID()
		}
		seen[id] = true
		g.Field(groupID).SetString(id)
		name := g.Field(groupName).String()
		if utf8.RuneCountInString(name) > maxGroupName {
			if strict {
				return bad("group %d: the name is at most %d characters", i+1, maxGroupName)
			}
			g.Field(groupName).SetString(string([]rune(name)[:maxGroupName]))
		}
		if g.Field(groupName).String() == "" { // nothing was typed: "Group A", "Group B"... by its place, or the next that is free
			n := i + 1
			for named[groupLetters(n)] {
				n++
			}
			g.Field(groupName).SetString(groupLetters(n))
			named[g.Field(groupName).String()] = true
		}
		if pv := g.Field(groupPercent); !pv.IsNil() {
			switch p := int(pv.Elem().Int()); {
			case p < 0 || p > 100:
				if strict {
					return bad("group %d: the percent is from 0 to 100", i+1)
				}
				pv.Set(reflect.Zero(pv.Type()))
			case p == 100: // all of the traffic is what a group is for when it says nothing
				pv.Set(reflect.Zero(pv.Type()))
			}
		}
		label := g.Field(groupName).String()
		for _, f := range gs.fields {
			fv := g.Field(groupTargeting).FieldByIndex(f.index)
			v := f.read(fv)
			err := f.check(v)
			if err == nil && strict {
				full := *f
				full.key = gs.key + "." + id + "." + f.key
				if verr := c.verify(&full, v); verr != nil {
					err = verr
					if ve, ok := verr.(*valueError); ok {
						err = fmt.Errorf("%s", ve.reason)
					}
				}
			}
			if err != nil {
				if strict {
					return bad("%s, %s: %v", label, f.key, err)
				}
				c.opts.log.Warn("tunables: saved value is invalid, using the default", "key", gs.key+"."+id+"."+f.key, "error", err)
				f.write(fv, f.def)
			}
		}
	}
	return nil
}

// clone is a copy of the groups that can be changed without changing them (a snapshot is not changed once it is published).
func (gs *groupSet) clone(sl reflect.Value) reflect.Value {
	if sl.IsNil() {
		return sl
	}
	out := reflect.MakeSlice(gs.typ, sl.Len(), sl.Len())
	reflect.Copy(out, sl)
	return out
}

// bind tells the features of a config where to report their checks. cfg is a *T, not yet shared.
func (c *core) bind(cfg reflect.Value) {
	if c.opts.observer == nil {
		return
	}
	for _, ref := range c.features {
		fv := cfg.Elem().FieldByIndex(ref.index)
		mf := fv.FieldByName("meta")
		if !mf.IsValid() || !mf.CanAddr() {
			continue
		}
		*(**featureMeta)(unsafe.Pointer(mf.UnsafeAddr())) = &featureMeta{key: ref.key, observe: c.opts.observer}
	}
}

// groupItemDTO is one group on the wire: the values of its fields by their keys relative to the group.
type groupItemDTO struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Percent *int           `json:"percent"`
	Random  bool           `json:"random"`
	Values  map[string]any `json:"values"`
}

// groupSetDTO is the groups of a feature on the wire: what a group is made of (the fields and the Rules, to make a new one
// from) and the groups there are.
type groupSetDTO struct {
	Key     string         `json:"key"`
	Feature string         `json:"feature"`
	Max     int            `json:"max"`
	Fields  []fieldDTO     `json:"fields"`
	Groups  []groupDTO     `json:"groups"`
	Items   []groupItemDTO `json:"items"`
}

func (c *core) groupSetDTO(gs *groupSet, s *snapshot) groupSetDTO {
	d := groupSetDTO{Key: gs.key, Feature: gs.feature, Max: maxGroups, Fields: []fieldDTO{}, Groups: []groupDTO{}, Items: []groupItemDTO{}}
	for _, f := range gs.fields {
		d.Fields = append(d.Fields, c.dtoOf(f, f.def))
	}
	for _, g := range gs.groups {
		d.Groups = append(d.Groups, groupDTO{Key: g.key, Name: g.name, Desc: g.desc, Rule: g.rule, Collapsed: g.collapsed})
	}
	sl := s.cfg.Elem().FieldByIndex(gs.index)
	for i := 0; i < sl.Len(); i++ {
		g := sl.Index(i)
		item := groupItemDTO{ID: g.Field(groupID).String(), Name: g.Field(groupName).String(), Random: g.Field(groupRandom).Bool(), Values: map[string]any{}}
		if pv := g.Field(groupPercent); !pv.IsNil() {
			p := int(pv.Elem().Int())
			item.Percent = &p
		}
		for _, f := range gs.fields {
			item.Values[f.key] = jsonValue(f.read(g.Field(groupTargeting).FieldByIndex(f.index)))
		}
		d.Items = append(d.Items, item)
	}
	return d
}

// groupLetters is the standard name of the group in the n-th place: "Group A" ... "Group Z", "Group AA"...
func groupLetters(n int) string {
	var letters []byte
	for ; n > 0; n = (n - 1) / 26 {
		letters = append([]byte{byte('A' + (n-1)%26)}, letters...)
	}
	return "Group " + string(letters)
}
