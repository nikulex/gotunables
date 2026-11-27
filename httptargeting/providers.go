package httptargeting

import (
	"net/netip"
	"strings"

	useragent "github.com/mileusna/useragent"
)

// Geo is where an address is, as far as a provider knows.
type Geo struct {
	Country string // ISO 3166-1 alpha-2 ("RS"; it is put in lower case)
	Region  string // a region, a state
	City    string
}

// GeoLocator says where an address is: the provider of the place. The package has no database of its own, you plug in the one
// you have (a MaxMind or DB-IP file read with github.com/oschwald/geoip2-golang, a service); with none, the place is unknown.
//
//	type maxmind struct{ db *geoip2.Reader }
//
//	func (m maxmind) Locate(ip netip.Addr) httptargeting.Geo {
//		city, err := m.db.City(ip)
//		if err != nil || !city.HasData() {
//			return httptargeting.Geo{}
//		}
//		return httptargeting.Geo{Country: city.Country.ISOCode, Region: first(city.Subdivisions), City: city.City.Names.English}
//	}
type GeoLocator interface {
	Locate(ip netip.Addr) Geo
}

// GeoFunc is a GeoLocator made of a function.
type GeoFunc func(ip netip.Addr) Geo

// Locate calls the function.
func (f GeoFunc) Locate(ip netip.Addr) Geo { return f(ip) }

// UserAgent is what a User-Agent says, as the parser puts it: all lower case, empty when it is not known.
type UserAgent struct {
	Browser string // "chrome", "safari", "firefox", "edge", "samsung browser"... what the parser names
	OS      string // "windows", "macos", "ios", "android", "linux", "chromeos"...
	Device  string // "mobile", "tablet", "desktop" or "bot"
}

// UserAgentParser reads a User-Agent header: the provider of the browser, the system and the kind of device.
// DefaultUserAgentParser is the one that is used unless another is plugged in (WithUserAgent).
type UserAgentParser interface {
	Parse(userAgent string) UserAgent
}

// UserAgentFunc is a UserAgentParser made of a function.
type UserAgentFunc func(userAgent string) UserAgent

// Parse calls the function.
func (f UserAgentFunc) Parse(userAgent string) UserAgent { return f(userAgent) }

// DefaultUserAgentParser reads the User-Agent with github.com/mileusna/useragent.
var DefaultUserAgentParser UserAgentParser = UserAgentFunc(func(s string) UserAgent {
	p := useragent.Parse(s)
	out := UserAgent{Browser: strings.ToLower(p.Name), OS: strings.ToLower(p.OS)}
	switch { // a bot first: a crawler says it is a Chrome
	case p.Bot:
		out.Device = "bot"
	case p.Tablet:
		out.Device = "tablet"
	case p.Mobile:
		out.Device = "mobile"
	case p.Desktop:
		out.Device = "desktop"
	}
	return out
})
