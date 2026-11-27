package tunables

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

var bootPattern = regexp.MustCompile(`(?s)<script type="application/json" id="boot">(.*?)</script>`)

func TestThePageHasTheNamesOfTheChosenAndATagOfTheSettings(t *testing.T) {
	tn, err := New(groupsConfig{}, WithName("t"), usersFiveSource())
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "g", Targeting: siteTargeting{UserID: Rule[uint]{Include: NewSet[uint](5)}}}}); err != nil {
		t.Fatal(err)
	}
	h := tn.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	m := bootPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("no boot data in the page")
	}
	var boot struct {
		Items map[string][]struct {
			ID   float64 `json:"id"`
			Name string  `json:"name"`
		} `json:"items"`
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal([]byte(m[1]), &boot); err != nil {
		t.Fatal(err)
	}
	if got := boot.Items["users"]; len(got) != 1 || got[0].ID != 5 || got[0].Name != "five" {
		t.Errorf("the chosen, by its name: %+v", got)
	}

	// the schema is asked for with the tag the page came with: nothing changed, nothing is sent
	ask := func(tag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/schema", nil)
		if tag != "" {
			req.Header.Set("If-None-Match", tag)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := ask(boot.Tag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("unchanged: %d %q", rec.Code, rec.Body)
	}
	full := ask("")
	if full.Code != 200 || full.Header().Get("ETag") != boot.Tag {
		t.Errorf("without a tag: %d, tag %q, the page's %q", full.Code, full.Header().Get("ETag"), boot.Tag)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{}); err != nil {
		t.Fatal(err)
	}
	if rec := ask(boot.Tag); rec.Code != 200 || rec.Header().Get("ETag") == boot.Tag {
		t.Errorf("changed: %d, the tag %q is the old one", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestASourceThatFailsLeavesTheNamesToThePage(t *testing.T) {
	down := false
	src := WithSource("users", DataSource[uint]{
		Search: func(_ context.Context, _ string, _, _ int) ([]Item[uint], error) { return nil, nil },
		Get: func(_ context.Context, ids []uint) ([]Item[uint], error) {
			if down {
				return nil, errors.New("down")
			}
			return []Item[uint]{{ID: 5, Name: "five"}}, nil
		},
	})
	tn, err := New(groupsConfig{}, WithName("t"), src)
	if err != nil {
		t.Fatal(err)
	}
	if err := tn.Set("checkout.groups", []Group[siteTargeting]{{ID: "g", Targeting: siteTargeting{UserID: Rule[uint]{Include: NewSet[uint](5)}}}}); err != nil {
		t.Fatal(err)
	}
	down = true
	rec := httptest.NewRecorder()
	tn.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	m := bootPattern.FindStringSubmatch(rec.Body.String())
	if rec.Code != 200 || m == nil {
		t.Fatalf("the page is served: %d", rec.Code)
	}
	var boot struct {
		Items map[string][]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(m[1]), &boot); err != nil {
		t.Fatal(err)
	}
	if len(boot.Items["users"]) != 0 {
		t.Errorf("nothing is known of the names: %v", boot.Items)
	}
}
