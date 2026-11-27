package prom

import (
	"testing"

	tunables "github.com/nikulex/gotunables"
	"github.com/prometheus/client_golang/prometheus"
)

type targeting struct {
	Country tunables.Rule[string] `json:"country"`
}

type settings struct {
	Features struct {
		Checkout tunables.Feature[targeting] `json:"checkout"`
	} `json:"features"`
}

type info struct{ Country string }

// counts reads the counter: the labels "feature group result" -> the count
func counts(t *testing.T, reg *prometheus.Registry) map[string]float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if f.GetName() != "tunables_feature_checks_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			l := map[string]string{}
			for _, p := range m.GetLabel() {
				l[p.GetName()] = p.GetValue()
			}
			out[l["feature"]+"|"+l["group"]+"|"+l["result"]] = m.GetCounter().GetValue()
		}
	}
	return out
}

func TestTheChecksAreCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	tn, err := tunables.New(settings{}, tunables.WithName("t"), Observer(reg))
	if err != nil {
		t.Fatal(err)
	}
	checkout := func() *tunables.Feature[targeting] { return &tn.Get().Features.Checkout }

	checkout().IsEnabled(info{Country: "rs"}) // switched off
	if err := tn.Set("features.checkout.enabled", true); err != nil {
		t.Fatal(err)
	}
	checkout().IsEnabled(info{Country: "rs"}) // no groups: everybody
	if err := tn.Set("features.checkout.groups", []tunables.Group[targeting]{
		{ID: "serbia", Name: "Serbia", Targeting: targeting{Country: tunables.Rule[string]{Include: tunables.NewSet("rs")}}},
	}); err != nil {
		t.Fatal(err)
	}
	checkout().IsEnabled(info{Country: "rs"})
	checkout().IsEnabled(info{Country: "rs"})
	checkout().IsEnabled(info{Country: "us"})

	got := counts(t, reg)
	want := map[string]float64{
		"features.checkout||off":      2, // switched off, and not in any group
		"features.checkout||on":       1,
		"features.checkout|Serbia|on": 2,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v, want %v (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("counted %v", got)
	}
}

func TestTwoTunablesShareTheCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	a, err := tunables.New(settings{}, tunables.WithName("a"), Observer(reg))
	if err != nil {
		t.Fatal(err)
	}
	b, err := tunables.New(settings{}, tunables.WithName("b"), Observer(reg)) // registered already: used
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Set("features.checkout.enabled", true); err != nil {
		t.Fatal(err)
	}
	a.Get().Features.Checkout.IsEnabled(info{})
	b.Get().Features.Checkout.IsEnabled(info{})
	if got := counts(t, reg); got["features.checkout||on"] != 1 || got["features.checkout||off"] != 1 {
		t.Errorf("%v", got)
	}
}

func TestTheNameCanBeChanged(t *testing.T) {
	reg := prometheus.NewRegistry()
	tn, err := tunables.New(settings{}, tunables.WithName("t"), Observer(reg, WithNamespace("iwillgo")))
	if err != nil {
		t.Fatal(err)
	}
	tn.Get().Features.Checkout.IsEnabled(info{})
	families, err := reg.Gather()
	if err != nil || len(families) != 1 || families[0].GetName() != "iwillgo_feature_checks_total" {
		t.Errorf("%v %v", families, err)
	}
}
