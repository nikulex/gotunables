package tunables

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"time"
)

// Item is one entry of a DataSource: what the UI shows for an id.
type Item[ID comparable] struct {
	ID       ID
	Name     string
	ImageSrc string // a small picture (it is drawn 24px): an https URL or a data:image/... URI; empty: none
}

// DataSource lets a setting pick its values from data that lives elsewhere (a database, an API): users, communities,
// products. The setting holds only the ids; the page finds the entries by typing, and shows the ones already chosen
// by name and picture.
//
//	tunables.New(Config{}, tunables.WithSource("users", tunables.DataSource[uint]{
//		Search: func(ctx context.Context, query string, limit int) ([]tunables.Item[uint], error) { ... },
//		Get:    func(ctx context.Context, ids []uint) ([]tunables.Item[uint], error) { ... },
//	}))
//
//	type Config struct {
//		Admins tunables.Set[uint] `tune:"source:users"` // any number of them
//		Owner  *uint              `tune:"source:users"` // one, or nobody
//		Voters []uint             `tune:"source:users"` // in this order
//	}
//
// The id type of the source is the type of the setting (or of an item of it): int, uint or string, of any size.
type DataSource[ID comparable] struct {
	// Search finds the entries that match what was typed: at most limit of them, after the first offset ones, in the same
	// order every time (the list is read page by page as it is scrolled, so a page has to follow the one before it). An
	// empty query is asked for too (the list that opens first): return the most useful ones. Make it quick and cheap:
	// the page calls it as someone types (after a pause) and as the list is scrolled, and every call is cut off after
	// 5 seconds.
	Search func(ctx context.Context, query string, limit, offset int) ([]Item[ID], error)
	// Get returns the entries of these ids; an id that does not exist is just left out. The page uses it to show the chosen
	// values, and a change is refused if one of its ids is not found.
	Get func(ctx context.Context, ids []ID) ([]Item[ID], error)
}

// WithSource registers a data source under a name; a setting refers to it with `tune:"source:name"`.
func WithSource[ID comparable](name string, ds DataSource[ID]) Option {
	return func(o *options) {
		fail := func(format string, args ...any) {
			if o.err == nil {
				o.err = fmt.Errorf("tunables: source %q: %s", name, fmt.Sprintf(format, args...))
			}
		}
		idType := reflect.TypeFor[ID]()
		switch {
		case !sourceName.MatchString(name):
			fail("the name must be letters, digits, '_', '-' or '.'")
			return
		case ds.Search == nil || ds.Get == nil:
			fail("needs both Search and Get")
			return
		}
		if k, err := scalarKind(idType); err != nil || (k != kindInt && k != kindUint && k != kindString) {
			fail("the id type %s must be an integer or a string", idType)
			return
		}
		erase := func(items []Item[ID]) []sourceItem {
			out := make([]sourceItem, len(items))
			for i, it := range items {
				out[i] = sourceItem{ID: it.ID, Name: it.Name, Image: it.ImageSrc}
			}
			return out
		}
		o.sources = append(o.sources, &source{
			name:   name,
			idType: idType,
			search: func(ctx context.Context, query string, limit, offset int) ([]sourceItem, error) {
				items, err := ds.Search(ctx, query, limit, offset)
				return erase(items), err
			},
			get: func(ctx context.Context, ids []any) ([]sourceItem, error) {
				typed := make([]ID, len(ids))
				for i, id := range ids {
					typed[i] = id.(ID)
				}
				items, err := ds.Get(ctx, typed)
				return erase(items), err
			},
		})
	}
}

var sourceName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

const (
	sourceTimeout    = 5 * time.Second
	sourceMaxQuery   = 100 // characters of a query
	sourceMaxLimit   = 50
	sourceMaxOffset  = 10000 // how deep the list is scrolled: further than this, type more of the name
	sourceDefLimit   = 20
	sourceMaxItemIDs = 100 // ids of one /items request
)

// sourceItem is an entry on the wire.
type sourceItem struct {
	ID    any    `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// source is a DataSource with its id type erased.
type source struct {
	name   string
	idType reflect.Type
	search func(ctx context.Context, query string, limit, offset int) ([]sourceItem, error)
	get    func(ctx context.Context, ids []any) ([]sourceItem, error) // the ids are of idType
}

// ids converts canonical values of a setting (int64, uint64, string) to the source's id type.
func (s *source) ids(canon []any) []any {
	out := make([]any, len(canon))
	for i, c := range canon {
		out[i] = reflect.ValueOf(c).Convert(s.idType).Interface()
	}
	return out
}

// parseID reads an id of the source's type from text (a query parameter).
func (s *source) parseID(text string) (any, error) {
	v := reflect.New(s.idType).Elem()
	switch k := s.idType.Kind(); {
	case k == reflect.String:
		v.SetString(text)
	case k >= reflect.Int && k <= reflect.Int64:
		n, err := strconv.ParseInt(text, 10, s.idType.Bits())
		if err != nil {
			return nil, fmt.Errorf("bad id %q", text)
		}
		v.SetInt(n)
	default:
		n, err := strconv.ParseUint(text, 10, s.idType.Bits())
		if err != nil {
			return nil, fmt.Errorf("bad id %q", text)
		}
		v.SetUint(n)
	}
	return v.Interface(), nil
}

// canonOf turns an id of the source into the canonical value of a setting (int64, uint64 or string) to compare with.
func canonOf(id any) any {
	v := reflect.ValueOf(id)
	switch k := v.Kind(); {
	case k == reflect.String:
		return v.String()
	case k >= reflect.Int && k <= reflect.Int64:
		return v.Int()
	default:
		return v.Uint()
	}
}

// verify checks that every id a setting is about to hold exists in its source. Only a write is checked: a saved id
// whose entry was deleted later stays (the page shows it as not found), it does not break the program.
func (c *core) verify(f *field, v any) error {
	if f.source == "" || v == nil {
		return nil
	}
	var canon []any
	if f.collection() {
		canon = v.([]any)
	} else {
		canon = []any{v}
	}
	if len(canon) == 0 {
		return nil
	}
	src := c.sources[f.source]
	ctx, cancel := context.WithTimeout(context.Background(), sourceTimeout)
	defer cancel()
	found, err := src.get(ctx, src.ids(canon))
	if err != nil {
		return fmt.Errorf("tunables: source %q: %w", f.source, err)
	}
	have := make(map[any]bool, len(found))
	for _, it := range found {
		have[canonOf(it.ID)] = true
	}
	for _, id := range canon {
		if !have[id] {
			return &valueError{key: f.key, reason: fmt.Sprintf("no such item in %s: %v", f.source, id)}
		}
	}
	return nil
}
