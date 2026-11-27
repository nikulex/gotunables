package tunables

import (
	"net/http"
	"strings"
)

// PublicHandler serves the settings that are marked `tune:"public"` to whoever asks, read only: the values of the fields, and for a
// Feature whether it is on for the request. Nothing else is given (not the other settings, not the groups and the rules of a
// feature). The answer is JSON in the shape of the settings, by the keys of the fields:
//
//	{"maintenance": false, "banner": "…", "features": {"new_checkout": true}}
//
// info makes what a feature is asked about from the request (see Feature.IsEnabled); nil: a feature is asked about nothing.
// The handler has no authorization of its own, mount it where the front end can reach it. Its answers are never cached, since
// a feature is on for some requests and off for others.
//
//	type Config struct {
//		Banner string `json:"banner" tune:"public"`            // a field
//		Site   struct{ ... } `json:"site" tune:"public"`       // everything in the struct
//		Features struct {
//			NewCheckout tunables.Feature[Targeting] `json:"new_checkout" tune:"public"` // the answer for the request
//		} `json:"features"`
//	}
//	mux.Handle("GET /api/public", t.PublicHandler(func(r *http.Request) any { return NewInfo(r) }))
func (t *Tunables[T]) PublicHandler(info func(*http.Request) any) http.Handler {
	return t.c.publicHandler(info)
}

// featureChecker is what a Feature of any targeting can tell.
type featureChecker interface{ IsEnabled(info any) bool }

func (c *core) publicHandler(info func(*http.Request) any) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			c.writeError(w, http.StatusMethodNotAllowed, "read only")
			return
		}
		cfg := c.state.Load().cfg.Elem()
		out := map[string]any{}
		for _, f := range c.fields {
			if f.public {
				put(out, f.key, jsonValue(f.read(cfg.FieldByIndex(f.index))))
			}
		}
		var in any
		asked := false
		for _, ref := range c.features {
			if !ref.public {
				continue
			}
			if !asked && info != nil {
				in, asked = info(r), true
			}
			on := false
			if fc, ok := cfg.FieldByIndex(ref.index).Addr().Interface().(featureChecker); ok {
				on = fc.IsEnabled(in)
			}
			put(out, ref.key, on)
		}
		c.writeJSON(w, http.StatusOK, out)
	})
}

// put sets the value at a key of dots in nested maps: "features.new_checkout" is out["features"]["new_checkout"].
func put(out map[string]any, key string, v any) {
	parts := strings.Split(key, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := out[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			out[p] = next
		}
		out = next
	}
	out[parts[len(parts)-1]] = v
}
