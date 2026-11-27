package tunables

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/nikulex/gotunables/ui"
)

// protocol is the version of the source API (/meta, /schema, /values) that tuneboard speaks.
const protocol = 1

// Handler serves the source API and the built-in UI:
//
//	GET    /              the UI
//	GET    /meta          {name, description, protocol}
//	GET    /schema        {fields: [{key, type, desc, default, value, modified, enum, min, max}]}
//	PUT    /values/{key}  {"value": ...}  -> the updated field
//	DELETE /values/{key}  back to the default -> the updated field
//
// Mount it under a prefix with http.StripPrefix and a trailing slash:
//
//	mux.Handle("/tunables/", http.StripPrefix("/tunables", t.Handler()))
//
// Don't put it on a public listener: anyone who reaches it can change your settings (WithToken adds a bearer
// token, nothing more). Writes also require a JSON content type and a same-host Origin, and the reverse proxy
// must pass the original Host header.
func (t *Tunables[T]) Handler() http.Handler { return t.c.handler() }

type fieldDTO struct {
	Key       string   `json:"key"`
	Name      string   `json:"name,omitempty"`
	Type      string   `json:"type"`
	UI        string   `json:"ui"`
	Precision *int     `json:"precision,omitempty"` // digits after the point to show for a float
	Desc      string   `json:"desc,omitempty"`
	Default   any      `json:"default"`
	Value     any      `json:"value"`
	Modified  bool     `json:"modified"`
	Enum      []string `json:"enum,omitempty"`
	Min       any      `json:"min,omitempty"`
	Max       any      `json:"max,omitempty"`
}

func (c *core) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", c.serveUI)
	mux.HandleFunc("GET /meta", c.guard(false, c.handleMeta))
	mux.HandleFunc("GET /schema", c.guard(false, c.handleSchema))
	mux.HandleFunc("PUT /values/{key}", c.guard(true, c.handlePut))
	mux.HandleFunc("DELETE /values/{key}", c.guard(true, c.handleDelete))
	return mux
}

func (c *core) serveUI(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'")
	h.Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(c.page()); err != nil {
		c.opts.log.Warn("tunables: write ui", "error", err)
	}
}

// The four places of the page that the server fills in; the page works without them (the script then
// loads everything), but with them it shows the real settings on first paint.
const (
	htmlTitle = `<title>tunables</title>`
	htmlName  = `<span class="bar-title" id="name">tunables</span>`
	htmlHero  = `<h1 class="hero-title" id="heroName">tunables</h1>`
	htmlDesc  = `<p class="hero-desc" id="desc"></p>`
	htmlBoot  = `<script type="application/json" id="boot">null</script>`
)

// page returns the UI with the name, the description and the current values already in it, so the first paint
// needs no request. With a token the page is served bare: it is public, the values are not.
func (c *core) page() []byte {
	if c.opts.token != "" {
		return ui.HTML
	}
	boot, err := json.Marshal(map[string]any{"meta": c.metaPayload(), "schema": c.schemaPayload()}) // escapes < > &
	if err != nil {
		c.opts.log.Warn("tunables: preload the page", "error", err)
		return ui.HTML
	}
	name := html.EscapeString(c.opts.name)
	return []byte(strings.NewReplacer(
		htmlTitle, "<title>"+name+" · tunables</title>",
		htmlName, `<span class="bar-title" id="name">`+name+`</span>`,
		htmlHero, `<h1 class="hero-title" id="heroName">`+name+`</h1>`,
		htmlDesc, `<p class="hero-desc" id="desc">`+html.EscapeString(strings.TrimSpace(c.opts.description))+`</p>`,
		htmlBoot, `<script type="application/json" id="boot">`+string(boot)+`</script>`,
	).Replace(string(ui.HTML)))
}

// guard checks the bearer token and, for writes, the Origin.
func (c *core) guard(write bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.opts.token != "" {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(c.opts.token)) != 1 {
				c.writeError(w, http.StatusUnauthorized, "missing or wrong token")
				return
			}
		}
		if write {
			if o := r.Header.Get("Origin"); o != "" {
				u, err := url.Parse(o)
				if err != nil || u.Host != r.Host {
					c.writeError(w, http.StatusForbidden, "cross-origin write")
					return
				}
			}
		}
		next(w, r)
	}
}

func (c *core) metaPayload() map[string]any {
	return map[string]any{
		"name":        c.opts.name,
		"description": c.opts.description,
		"protocol":    protocol,
	}
}

func (c *core) schemaPayload() map[string]any {
	s := c.state.Load()
	fields := make([]fieldDTO, 0, len(c.fields))
	for _, f := range c.fields {
		fields = append(fields, c.dto(f, s))
	}
	groups := make([]groupDTO, 0, len(c.groups))
	for _, g := range c.groups {
		groups = append(groups, groupDTO{Key: g.key, Name: g.name, Desc: g.desc})
	}
	return map[string]any{"groups": groups, "fields": fields}
}

func (c *core) handleMeta(w http.ResponseWriter, _ *http.Request) {
	c.writeJSON(w, http.StatusOK, c.metaPayload())
}

func (c *core) handleSchema(w http.ResponseWriter, _ *http.Request) {
	c.writeJSON(w, http.StatusOK, c.schemaPayload())
}

func (c *core) handlePut(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		c.writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		c.writeError(w, http.StatusBadRequest, "body must be {\"value\": ...}")
		return
	}
	key := r.PathValue("key")
	c.respondChange(w, key, c.set(key, body.Value))
}

func (c *core) handleDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	c.respondChange(w, key, c.reset(key))
}

func (c *core) respondChange(w http.ResponseWriter, key string, err error) {
	switch {
	case errors.Is(err, ErrUnknownKey):
		c.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidValue):
		msg := err.Error()
		var ve *valueError
		if errors.As(err, &ve) {
			msg = ve.reason // "500 is above the maximum 100": the key is already on screen
		}
		c.writeError(w, http.StatusBadRequest, msg)
	case err != nil:
		c.opts.log.Error("tunables: change failed", "key", key, "error", err)
		c.writeError(w, http.StatusInternalServerError, "could not save the change")
	default:
		c.writeJSON(w, http.StatusOK, c.dto(c.byKey[key], c.state.Load()))
	}
}

type groupDTO struct {
	Key  string `json:"key"`
	Name string `json:"name,omitempty"`
	Desc string `json:"desc,omitempty"`
}

func (c *core) dto(f *field, s *snapshot) fieldDTO {
	cur := f.read(s.cfg.Elem().FieldByIndex(f.index))
	return fieldDTO{
		Key:       f.key,
		Name:      f.name,
		Type:      f.apiType(),
		UI:        f.ui,
		Precision: f.prec,
		Desc:      f.desc,
		Default:   jsonValue(f.def),
		Value:     jsonValue(cur),
		Modified:  cur != f.def,
		Enum:      f.enum,
		Min:       f.bound(f.min),
		Max:       f.bound(f.max),
	}
}

func (c *core) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		c.opts.log.Warn("tunables: write response", "error", err)
	}
}

func (c *core) writeError(w http.ResponseWriter, status int, msg string) {
	c.writeJSON(w, status, map[string]string{"error": msg})
}
