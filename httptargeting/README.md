# httptargeting

`github.com/nikulex/gotunables/httptargeting` is a module of its own (`go get github.com/nikulex/gotunables/httptargeting`), so that
gotunables itself has no dependencies: the User-Agent is read by a library, and the lists are those of two others. It is the targeting of a web request, ready to use or to embed into your own:
`httptargeting.Info` (the address, the place it is in, the languages, the browser, the system, the kind of device, the host) and
`httptargeting.Targeting` (a Rule for each), and a `Parser` that reads the first from a request:

```go
web := httptargeting.NewParser(httptargeting.WithGeo(myGeo), httptargeting.WithClientIPHeader("X-Real-IP"))
m, _ := tunables.NewMatcher[httptargeting.Targeting, httptargeting.Info]()
...
info := web.Parse(r)
if m.On(&t.Get().Features.NewCheckout, &info) { ... }
```

Nothing in it is made up, and every provider can be replaced:

| what | provider | replace with |
|---|---|---|
| the User-Agent | `github.com/mileusna/useragent` (`httptargeting.DefaultUserAgentParser`) | `httptargeting.WithUserAgent(parser)`: an interface, or a function (`UserAgentFunc`) |
| the place of an address | none: you plug in the database you have (`httptargeting.GeoLocator`, or a `GeoFunc`) with `WithGeo` | the same: a MaxMind or DB-IP file read with `geoip2-golang`, a service; an adapter is a few lines (see `providers.go`) |
| the lists of countries and languages | `github.com/biter777/countries`, `github.com/emvi/iso-639-1` | a source of your own under the name `countries` or `languages` (`tunables.WithSource`) |
| the lists of browsers and systems | the names of `mileusna/useragent` | the sources `browsers`, `systems`: a program that plugs in another parser registers the names of its own |

The address is the one of the connection; behind your own proxy `WithClientIPHeader("X-Real-IP")` takes it from a header
(use it only when every request comes through that proxy: anyone else can write the header). Text values are lower case.
There are no lists of regions and cities: what a geo database calls them differs from one to the next, so they are typed
(or register your own source for the database you use). The lists are registered when the package is imported
(`tunables.RegisterSource`), and the rules refer to them as `source:countries`; the countries and languages also take a code that is
not in the list (`custom`: a geo database knows more than ISO). `tunables.StaticSource(items)` makes a source of a fixed
list of your own.

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
// m, _ := tunables.NewMatcher[MyTargeting, MyInfo](); m.On(&feature, &MyInfo{Info: web.Parse(r), Provider: "google", UserID: 7})
```

An embedded struct without a json name is flattened, so the settings page shows the rules of all of them together. The
nesting limit is three levels: a feature in a group (`features.new_checkout.country`) fits, one more group around it does not.


## Try it

```
cd httptargeting && go run ./demo    # open http://127.0.0.1:6767/#/features, then GET /try
```
