package tunables

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type publicConfig struct {
	Secret string `json:"secret"`
	Banner string `json:"banner" tune:"public"`
	Site   struct {
		Name   string   `json:"name"`
		Hidden string   `json:"hidden" tune:"public:false"`
		Items  []string `json:"items"`
	} `json:"site" tune:"public"`
	Features struct {
		Checkout Feature[siteTargeting] `json:"checkout" tune:"public"`
		Closed   Feature[siteTargeting] `json:"closed"`
	} `json:"features"`
}

func publicServer(t *testing.T) (*Tunables[publicConfig], http.Handler) {
	t.Helper()
	var cfg publicConfig
	cfg.Secret, cfg.Banner, cfg.Site.Name, cfg.Site.Hidden, cfg.Site.Items = "s3cret", "hello", "iwillgo", "h", []string{"a", "b"}
	cfg.Features.Checkout = Feature[siteTargeting]{Enabled: true}
	cfg.Features.Closed = Feature[siteTargeting]{Enabled: true}
	tn, err := New(cfg, WithName("t"), usersFiveSource())
	if err != nil {
		t.Fatal(err)
	}
	h := tn.PublicHandler(func(r *http.Request) any { return siteInfo{Country: r.URL.Query().Get("c")} })
	return tn, h
}

func getPublic(t *testing.T, h http.Handler, url string) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body)
	}
	return got, rec
}

func TestThePublicHandlerGivesWhatIsMarkedAndNothingElse(t *testing.T) {
	_, h := publicServer(t)
	got, rec := getPublic(t, h, "/")
	want := map[string]any{
		"banner":   "hello",
		"site":     map[string]any{"name": "iwillgo", "items": []any{"a", "b"}},
		"features": map[string]any{"checkout": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%d %v", rec.Code, rec.Header())
	}
	for _, leak := range []string{"s3cret", "closed", "groups", "targeting", "enabled", "hidden"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("%q is given: %s", leak, rec.Body)
		}
	}
}

func TestAPublicFeatureIsTheAnswerForTheRequest(t *testing.T) {
	tn, h := publicServer(t)
	if err := tn.Set("features.checkout.groups", []Group[siteTargeting]{{ID: "serbia", Targeting: siteTargeting{Country: Rule[string]{Include: NewSet("rs")}}}}); err != nil {
		t.Fatal(err)
	}
	for url, want := range map[string]bool{"/?c=rs": true, "/?c=us": false, "/": false} {
		got, _ := getPublic(t, h, url)
		if on := got["features"].(map[string]any)["checkout"]; on != want {
			t.Errorf("%s: %v, want %v", url, on, want)
		}
	}
	if err := tn.Set("features.checkout.enabled", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := getPublic(t, h, "/?c=rs"); got["features"].(map[string]any)["checkout"] != false {
		t.Error("switched off: off")
	}
}

func TestThePublicHandlerFollowsTheSettings(t *testing.T) {
	tn, h := publicServer(t)
	if err := tn.Set("banner", "changed"); err != nil {
		t.Fatal(err)
	}
	if got, _ := getPublic(t, h, "/"); got["banner"] != "changed" {
		t.Errorf("%v", got)
	}
}

func TestThePublicHandlerIsReadOnlyAndAsksNothingWhenNoInfo(t *testing.T) {
	tn, h := publicServer(t)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(m, "/", strings.NewReader("{}")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, rec.Code)
		}
	}
	got, _ := getPublic(t, tn.PublicHandler(nil), "/")
	if got["features"].(map[string]any)["checkout"] != true {
		t.Errorf("no groups: on for everybody, whatever is asked: %v", got)
	}
}

func TestPublicIsATagOfAFieldOrAGroup(t *testing.T) {
	type bare struct {
		A string `json:"a" tune:"public"`
		B string `json:"b" tune:"name:Bee; public:true"`
		C string `json:"c"`
	}
	tn, err := New(bare{A: "1", B: "2", C: "3"}, WithName("t"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := getPublic(t, tn.PublicHandler(nil), "/"); !reflect.DeepEqual(got, map[string]any{"a": "1", "b": "2"}) {
		t.Errorf("%v", got)
	}
	type wrong struct {
		A string `json:"a" tune:"public:maybe"`
	}
	if _, err := New(wrong{}, WithName("t")); err == nil || !strings.Contains(err.Error(), "public must be true or false") {
		t.Errorf("%v", err)
	}
	type nothing struct {
		A string `json:"a"`
	}
	tn2, err := New(nothing{A: "x"}, WithName("t"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := getPublic(t, tn2.PublicHandler(nil), "/"); len(got) != 0 {
		t.Errorf("nothing is marked, nothing is given: %v", got)
	}
}

func TestAPublicCheckIsReportedToTheObserver(t *testing.T) {
	var got []Check
	var cfg publicConfig
	cfg.Features.Checkout = Feature[siteTargeting]{Enabled: true}
	tn, err := New(cfg, WithName("t"), usersFiveSource(), WithObserver(func(c Check) { got = append(got, c) }))
	if err != nil {
		t.Fatal(err)
	}
	getPublic(t, tn.PublicHandler(nil), "/")
	if len(got) != 1 || got[0].Feature != "features.checkout" || !got[0].Enabled {
		t.Errorf("%+v", got)
	}
}
