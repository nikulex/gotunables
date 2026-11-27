# httptargeting

`github.com/nikulex/gotunables/httptargeting` is a module of its own (`go get github.com/nikulex/gotunables/httptargeting`), so that
gotunables itself has no dependencies, and this one has a single one: the library that reads the User-Agent. The geo data and the lists
are **integrations, each a module of its own** in this repository (below): add the ones you use. It is the targeting of a web request, ready to use or to embed into your own:
`httptargeting.Info` (the address, the place it is in, the languages, the browser, the system, the kind of device, the host) and
`httptargeting.Targeting` (a Rule for each), and a `Parser` that reads the first from a request:

```go
web := httptargeting.NewParser(httptargeting.WithGeo(myGeo), httptargeting.WithClientIPHeader("X-Real-IP"))
...
info := web.Parse(r)
if t.Get().Features.NewCheckout.IsEnabled(&info) { ... }
```

Nothing in it is made up, and every provider can be replaced:

| what | provider | module | replace with |
|---|---|---|---|
| the User-Agent | `github.com/mileusna/useragent` (`httptargeting.DefaultUserAgentParser`) | this one | `httptargeting.WithUserAgent(parser)`: an interface, or a function (`UserAgentFunc`) |
| the lists of browsers, systems, devices | the names of `mileusna/useragent` (`httptargeting.Browsers()`, `Systems()`, `Devices()`) | this one | the sources `browsers`, `systems`: add to them with `tunables.CombineSources`, or replace them (a program that plugs in another parser registers the names of its own) |
| the place of an address; the lists of countries, regions, cities | a city database file in the MaxMind DB format (GeoLite2-City, DB-IP City Lite), `github.com/oschwald/maxminddb-golang` | `httptargeting/geo/mmdb` | any `httptargeting.GeoLocator` (or a `GeoFunc`) with `WithGeo`: a service, another file format, a paid API; and your own sources named `countries`, `regions`, `cities` |
| the lists of countries and languages by ISO | `github.com/biter777/countries`, `github.com/emvi/iso-639-1` | `httptargeting/geo/iso` | a source of your own under the name `countries` or `languages`, or the list with more in it: `tunables.WithSource("countries", tunables.CombineSources(iso.Countries(), mine))` |

Each module's own dependencies come with it only if you `go get` it: a program that takes its places from a database file has no use for
the ISO lists, and does not get them.

The address is the one of the connection; behind your own proxy `WithClientIPHeader("X-Real-IP")` takes it from a header
(use it only when every request comes through that proxy: anyone else can write the header). Text values are lower case.
The lists of countries, languages, regions and cities are empty until an integration gives them values (nothing is made up: the rules take
typed values meanwhile, `custom`); the lists of browsers, systems and devices are the parser's. Each integration is used with
`tunables.New(cfg, integration.Sources()...)`; a source registered by the program under the same name takes the place of any of them.
`tunables.StaticSource(items)` makes a source of a fixed list of your own.

**The geo database** (`httptargeting/geo/mmdb`, `go get github.com/nikulex/gotunables/httptargeting/geo/mmdb`) is one file for the place of the request and for the lists on the page:

```go
db, err := mmdb.Open("dbip-city-lite.mmdb")  // github.com/nikulex/gotunables/httptargeting/geo/mmdb
web := httptargeting.NewParser(httptargeting.WithGeo(db))
t, err := tunables.New(cfg, db.Sources()...) // the lists of the countries, regions and cities: the English names of the file
```

The lists are read from the file when the page first asks for them (about a second and a half for the whole world, then
they are kept); a region or a city is named once, with the countries that have one by that name ("Central (LK, RS)").

**The ISO lists** (`httptargeting/geo/iso`): `tunables.New(cfg, iso.Sources()...)` gives the countries (ISO 3166-1) and the languages (ISO 639-1).

**Extending** is embedding: put `httptargeting.Targeting` into your own targeting and `httptargeting.Info` into your own info,
and add Rules and fields of your own (the account provider, the signed-in user, a group):

```go
type MyTargeting struct {
    httptargeting.Targeting
    Provider tunables.Rule[string] `json:"provider" tune:"enum:google,telegram,email"`
    UserID   tunables.Rule[uint]   `json:"users" tune:"source:users"` // picked by name, see above
}
type MyInfo struct {
    httptargeting.Info
    Provider string
    UserID   uint
}
// feature.IsEnabled(&MyInfo{Info: web.Parse(r), Provider: "google", UserID: 7})
```

An embedded struct without a json name is flattened, so the settings page shows the rules of all of them together. The
nesting limit is three levels: a feature in a group (`features.new_checkout.country`) fits, one more group around it does not.


## Try it

```
cd httptargeting/demo && go run .    # the countries are the ISO lists; MMDB=file.mmdb takes them from a geo database. Open http://127.0.0.1:6767/#/features, then GET /try
```
