// Package prom counts the checks of the features of gotunables for Prometheus (and VictoriaMetrics, and anything that reads its
// format): how many times each feature was asked about, which of its groups let the request in, and what the answer was.
//
//	t, err := tunables.New(cfg, prom.Observer(prometheus.DefaultRegisterer))
//
// It is a counter, tunables_feature_checks_total, with the labels
//
//	feature  the key of the feature: "features.new_checkout"
//	group    the group that let the request in, by its name (its id when it has none); empty when the feature has no groups, or no
//	         group let the request in
//	result   "on" or "off"
//
// so the rate of a feature that is on is rate(tunables_feature_checks_total{result="on"}[5m]), and a group that has no hits is one
// that nobody is in. The checks that are counted are the calls of Feature.IsEnabled and Matcher.IsEnabled.
package prom

import (
	"errors"

	tunables "github.com/nikulex/gotunables"
	"github.com/prometheus/client_golang/prometheus"
)

type config struct {
	namespace, subsystem, name string
}

// Option changes the name of the counter.
type Option func(*config)

// WithNamespace puts a prefix before the name of the counter: iwillgo_feature_checks_total.
func WithNamespace(namespace string) Option { return func(c *config) { c.namespace = namespace } }

// WithSubsystem puts a word between the prefix and the name (the default is "feature").
func WithSubsystem(subsystem string) Option { return func(c *config) { c.subsystem = subsystem } }

// Observer makes the counter, registers it (one that is registered already, by another Tunables of the program, is used), and
// returns the option that tells it of the checks: tunables.New(cfg, prom.Observer(reg)).
func Observer(reg prometheus.Registerer, opts ...Option) tunables.Option {
	c := config{namespace: "tunables", subsystem: "feature", name: "checks_total"}
	for _, o := range opts {
		o(&c)
	}
	checks := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: c.namespace,
		Subsystem: c.subsystem,
		Name:      c.name,
		Help:      "Checks of a feature: which group let the request in, and the answer.",
	}, []string{"feature", "group", "result"})
	if err := reg.Register(checks); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			panic(err)
		}
		checks = already.ExistingCollector.(*prometheus.CounterVec)
	}
	return tunables.WithObserver(func(check tunables.Check) {
		result := "off"
		if check.Enabled {
			result = "on"
		}
		checks.WithLabelValues(check.Feature, check.Group, result).Inc()
	})
}
