package tunables

import (
	"context"
	"sync"
	"time"
)

// preloadTimeout is how long the page waits for the sources to name what the settings hold: past it the page asks for itself.
const preloadTimeout = 2 * time.Second

// itemsPayload is the entries behind the ids that the settings hold now (in the fields, and in the groups of the features), by
// source: what the page shows for them, put into the page so that it makes no request for each. A source that does not answer
// in time is left out, and the page asks it as it would have.
func (c *core) itemsPayload(s *snapshot) map[string][]sourceItem {
	ids := map[string][]any{}
	seen := map[string]map[any]bool{}
	add := func(f *field, v any) {
		src := c.sources[f.source]
		if f.source == "" || src == nil || v == nil {
			return
		}
		vals, ok := v.([]any)
		if !ok {
			vals = []any{v}
		}
		if seen[f.source] == nil {
			seen[f.source] = map[any]bool{}
		}
		for _, x := range vals {
			if !seen[f.source][x] && len(ids[f.source]) < sourceMaxItemIDs {
				seen[f.source][x] = true
				ids[f.source] = append(ids[f.source], x)
			}
		}
	}
	cfg := s.cfg.Elem()
	for _, f := range c.fields {
		add(f, f.read(cfg.FieldByIndex(f.index)))
	}
	for _, gs := range c.groupSets {
		sl := cfg.FieldByIndex(gs.index)
		for i := 0; i < sl.Len(); i++ {
			for _, f := range gs.fields {
				add(f, f.read(sl.Index(i).Field(groupTargeting).FieldByIndex(f.index)))
			}
		}
	}
	out := map[string][]sourceItem{}
	if len(ids) == 0 {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), preloadTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, list := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src := c.sources[name]
			items, err := src.get(ctx, src.ids(list))
			if err != nil {
				c.opts.log.Warn("tunables: preload the entries of a source", "source", name, "error", err)
				return
			}
			mu.Lock()
			out[name] = cut(items, len(list))
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}
