package tunables

import (
	"encoding/json"
	"reflect"
	"slices"
)

// Set is a set of items of any comparable type that settings can hold (bool, string, integers, floats,
// time.Duration). In the UI it is a row of removable chips with no repeats, and it is stored as a sorted JSON
// array. A nil Set is an empty one.
//
//	type Config struct {
//		Admins tunables.Set[int64] `json:"admins" tune:"desc:Telegram ids of the admins"`
//	}
//
//	if t.Get().Admins.Has(id) { ... }
type Set[T comparable] map[T]struct{}

// NewSet returns a Set of the items.
func NewSet[T comparable](items ...T) Set[T] {
	s := make(Set[T], len(items))
	for _, it := range items {
		s[it] = struct{}{}
	}
	return s
}

// Has reports whether v is in the set.
func (s Set[T]) Has(v T) bool { _, ok := s[v]; return ok }

// Len is the number of items.
func (s Set[T]) Len() int { return len(s) }

// Items returns the items, in no particular order.
func (s Set[T]) Items() []T {
	out := make([]T, 0, len(s))
	for it := range s {
		out = append(out, it)
	}
	return out
}

func (Set[T]) setElem() reflect.Type { return reflect.TypeFor[T]() }

// MarshalJSON writes the items as an array, sorted, so the stored value is the same every time.
func (s Set[T]) MarshalJSON() ([]byte, error) {
	items := s.Items()
	slices.SortFunc(items, func(a, b T) int { return cmpAny(a, b) })
	return json.Marshal(items)
}

// UnmarshalJSON reads an array of items; repeats are merged.
func (s *Set[T]) UnmarshalJSON(b []byte) error {
	var items []T
	if err := json.Unmarshal(b, &items); err != nil {
		return err
	}
	if items == nil {
		*s = nil
		return nil
	}
	*s = NewSet(items...)
	return nil
}
