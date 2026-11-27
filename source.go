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
//		Search: func(ctx context.Context, query string, limit, offset int) ([]tunables.Item[uint], error) { ... },
//		Get:    func(ctx context.Context, ids []uint) ([]tunables.Item[uint], error) { ... },
//	}))
//
// A source that is read by a token (a cursor: GitHub, Stripe, most APIs of today) sets SearchToken instead of Search.
//
//	type Config struct {
//		Admins tunables.Set[uint] `tune:"source:users"` // any number of them
//		Owner  *uint              `tune:"source:users"` // one, or nobody
//		Voters []uint             `tune:"source:users"` // in this order
//	}
//
// An id the source does not know can be added by hand too with `custom:true` (a chat that is not in the list yet): the
// page then offers "add as typed", and such an id is not checked against Get.
//
// The id type of the source is the type of the setting (or of an item of it): int, uint or string, of any size.
type DataSource[ID comparable] struct {
	// Search finds the entries that match what was typed: at most limit of them, after the first offset ones, in the same
	// order every time (the list is read page by page as it is scrolled, so a page has to follow the one before it). An
	// empty query is asked for too (the list that opens first): return the most useful ones. Make it quick and cheap:
	// the page calls it as someone types (after a pause) and as the list is scrolled, and every call is cut off after
	// 5 seconds.
	Search func(ctx context.Context, query string, limit, offset int) ([]Item[ID], error)
	// SearchToken is Search for a source that does not take an offset but a token: the first page is asked for with an empty
	// token, and the page says the token of the next one (Page.Next; empty: there is no next). Set it instead of Search, not
	// with it. The page keeps the token as it is given (it is opaque to the page: text of at most 1000 characters) and sends
	// it back to read on; the rest is as for Search: the same order every time, quick, cut off after 5 seconds.
	SearchToken func(ctx context.Context, query string, limit int, token string) (Page[ID], error)
	// Get returns the entries of these ids; an id that does not exist is just left out. The page uses it to show the chosen
	// values, and a change is refused if one of its ids is not found.
	Get func(ctx context.Context, ids []ID) ([]Item[ID], error)
}

// Page is a page of a source that is read by a token (DataSource.SearchToken).
type Page[ID comparable] struct {
	Items []Item[ID]
	Next  string // the token of the page after this one; empty: this is the last
}

// top reads the first n entries of a query, from either kind of source (the sources that are put together, see CombineSources).
func (ds DataSource[ID]) top(ctx context.Context, query string, n int) ([]Item[ID], error) {
	if ds.SearchToken == nil {
		return ds.Search(ctx, query, n, 0)
	}
	var out []Item[ID]
	token := ""
	for pages := 0; len(out) < n && pages < sourceMaxTopPages; pages++ {
		p, err := ds.SearchToken(ctx, query, n-len(out), token)
		if err != nil {
			return nil, err
		}
		out = append(out, p.Items...)
		if p.Next == "" || len(p.Items) == 0 {
			break
		}
		token = p.Next
	}
	return out[:min(n, len(out))], nil
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
		case (ds.Search == nil) == (ds.SearchToken == nil):
			fail("needs one of Search (an offset) and SearchToken (a token), not both")
			return
		case ds.Get == nil:
			fail("needs Get")
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
		var searchToken func(ctx context.Context, query string, limit int, token string) ([]sourceItem, string, error)
		if ds.SearchToken != nil {
			searchToken = func(ctx context.Context, query string, limit int, token string) ([]sourceItem, string, error) {
				p, err := ds.SearchToken(ctx, query, limit, token)
				return erase(p.Items), p.Next, err
			}
		}
		o.sources = append(o.sources, &source{
			name:        name,
			idType:      idType,
			searchToken: searchToken,
			search: func(ctx context.Context, query string, limit, offset int) ([]sourceItem, error) {
				if ds.Search == nil {
					return nil, nil
				}
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
	sourceTimeout     = 5 * time.Second
	sourceMaxQuery    = 100 // characters of a query
	sourceMaxLimit    = 50
	sourceMaxOffset   = 10000 // how deep the list is scrolled: further than this, type more of the name
	sourceDefLimit    = 20
	sourceMaxItemIDs  = 100  // ids of one /items request
	sourceMaxToken    = 1000 // characters of a token of a page
	sourceMaxTopPages = 20   // pages of a source with tokens that are read to put it together with others
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
	// searchToken is set for a source that is read by a token, and then search is not used
	searchToken func(ctx context.Context, query string, limit int, token string) (items []sourceItem, next string, err error)
	get         func(ctx context.Context, ids []any) ([]sourceItem, error) // the ids are of idType
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
	if f.source == "" || f.custom || v == nil { // a custom id is the person's word, the source may not know it
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
