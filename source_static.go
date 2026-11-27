package tunables

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// StaticSource is a DataSource over a fixed list: it searches the names and the ids (the ones that start with what was
// typed come first, then the rest, in the order of the list), pages as it is asked, and finds by id.
func StaticSource[ID comparable](items []Item[ID]) DataSource[ID] {
	byID := make(map[ID]Item[ID], len(items))
	// the lower case of each, made once: a list can be long (the cities of a geo database), and a search runs as someone types
	type entry struct {
		item     Item[ID]
		name, id string
	}
	entries := make([]entry, len(items))
	for i, it := range items {
		byID[it.ID] = it
		entries[i] = entry{it, strings.ToLower(it.Name), strings.ToLower(fmt.Sprint(it.ID))}
	}
	return DataSource[ID]{
		Search: func(_ context.Context, query string, limit, offset int) ([]Item[ID], error) {
			q := strings.ToLower(strings.TrimSpace(query))
			type hit struct {
				item  Item[ID]
				score int // 0: the id is what was typed, 1: a name that starts with it, 2: contains it
			}
			var hits []hit
			for _, e := range entries {
				switch {
				case q == "":
					hits = append(hits, hit{e.item, 2})
				case e.id == q:
					hits = append(hits, hit{e.item, 0})
				case strings.HasPrefix(e.name, q) || strings.HasPrefix(e.id, q):
					hits = append(hits, hit{e.item, 1})
				case strings.Contains(e.name, q):
					hits = append(hits, hit{e.item, 2})
				}
			}
			slices.SortStableFunc(hits, func(a, b hit) int { return a.score - b.score })
			if offset >= len(hits) {
				return nil, nil
			}
			hits = hits[offset:]
			hits = hits[:min(limit, len(hits))]
			out := make([]Item[ID], len(hits))
			for i, h := range hits {
				out[i] = h.item
			}
			return out, nil
		},
		Get: func(_ context.Context, ids []ID) ([]Item[ID], error) {
			var out []Item[ID]
			for _, id := range ids {
				if it, ok := byID[id]; ok {
					out = append(out, it)
				}
			}
			return out, nil
		},
	}
}

// CombineSources is one source of several: what a program adds to a list that a package has (more countries, its own
// browsers), or any lists that go together. A search asks each source in turn, the first one's entries first, and a page is
// cut from what they said (the same id once: the first source's entry wins); an id is looked up in all of them. A source that
// fails fails the search: a list that is not whole is not shown as if it were.
//
//	tunables.WithSource("countries", tunables.CombineSources(httptargeting.Countries(), tunables.StaticSource(myCountries)))
func CombineSources[ID comparable](sources ...DataSource[ID]) DataSource[ID] {
	return DataSource[ID]{
		Search: func(ctx context.Context, query string, limit, offset int) ([]Item[ID], error) {
			seen := map[ID]bool{}
			var all []Item[ID]
			for _, s := range sources {
				found, err := s.Search(ctx, query, offset+limit, 0) // what a page needs of it, from the top
				if err != nil {
					return nil, err
				}
				for _, it := range found {
					if !seen[it.ID] {
						seen[it.ID] = true
						all = append(all, it)
					}
				}
				if len(all) >= offset+limit {
					break
				}
			}
			if offset >= len(all) {
				return nil, nil
			}
			all = all[offset:]
			return all[:min(limit, len(all))], nil
		},
		Get: func(ctx context.Context, ids []ID) ([]Item[ID], error) {
			seen := map[ID]bool{}
			var out []Item[ID]
			for _, s := range sources {
				found, err := s.Get(ctx, ids)
				if err != nil {
					return nil, err
				}
				for _, it := range found {
					if !seen[it.ID] {
						seen[it.ID] = true
						out = append(out, it)
					}
				}
			}
			return out, nil
		},
	}
}

// A source can be registered for every program that imports a package: a field refers to it by `tune:"source:name"`
// and needs no WithSource. A package built on tunables (the httptargeting module has the lists of countries, languages,
// browsers) registers its sources when it is imported. A source registered by the program with WithSource under the same
// name is used instead.
var (
	registeredMu      sync.RWMutex
	registeredSources = map[string]func() *source{}
)

// RegisterSource makes a data source that every program that imports the package has, see above. Call it from the
// package's init. A name is of letters, digits, '_', '-' or '.'; registering a name again replaces it.
func RegisterSource[ID comparable](name string, ds DataSource[ID]) {
	var o options
	WithSource(name, ds)(&o)
	if o.err != nil {
		panic(o.err)
	}
	registeredMu.Lock()
	defer registeredMu.Unlock()
	registeredSources[name] = func() *source { return o.sources[0] }
}

func registeredSource(name string) (*source, bool) {
	registeredMu.RLock()
	defer registeredMu.RUnlock()
	mk, ok := registeredSources[name]
	if !ok {
		return nil, false
	}
	return mk(), true
}
