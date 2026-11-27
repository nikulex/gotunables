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
	for _, it := range items {
		byID[it.ID] = it
	}
	return DataSource[ID]{
		Search: func(_ context.Context, query string, limit, offset int) ([]Item[ID], error) {
			q := strings.ToLower(strings.TrimSpace(query))
			type hit struct {
				item  Item[ID]
				score int // 0: the id is what was typed, 1: a name that starts with it, 2: contains it
			}
			var hits []hit
			for _, it := range items {
				name, id := strings.ToLower(it.Name), strings.ToLower(fmt.Sprint(it.ID))
				switch {
				case q == "":
					hits = append(hits, hit{it, 2})
				case id == q:
					hits = append(hits, hit{it, 0})
				case strings.HasPrefix(name, q) || strings.HasPrefix(id, q):
					hits = append(hits, hit{it, 1})
				case strings.Contains(name, q):
					hits = append(hits, hit{it, 2})
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
