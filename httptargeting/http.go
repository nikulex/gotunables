package httptargeting

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	tunables "github.com/nikulex/gotunables"
)

// Info is what a request says about whoever sent it: the info that Targeting is checked against. Embed it into your
// own struct to add what only you know (see the package doc). Text values are in lower case, so a Rule is written in lower case too.
// tunables.CheckInfo[Targeting, Info]() finds out whether the info and the Rules go together: the fields marked target:"-"
// are not for targeting.
type Info struct {
	IP        string   `target:"-"` // the address of the client (see WithClientIPHeader): there is no Rule of an address
	Country   string   // from the GeoLocator, empty without one
	Region    string   //
	City      string   //
	Language  string   `target:"-"` // the first language of Accept-Language: "en" (Languages is what a Rule is for)
	Languages []string // all of them by their weight, without the region part ("en-US" is "en")
	Browser   string   // from the UserAgentParser
	OS        string   //
	Device    string   // "mobile", "tablet", "desktop", "bot"
	Host      string   // the host asked for, without the port
}

// Targeting is the Rules for Info, with the lists they pick from (see sources.go). Embed it into your own targeting
// to add Rules of your own.
type Targeting struct {
	Country   tunables.Rule[string] `json:"country" tune:"name:Country; desc:Where the address is; source:countries; custom:true"`
	Region    tunables.Rule[string] `json:"region" tune:"name:Region; desc:As the geo database names it, in lower case; source:regions; custom:true"`
	City      tunables.Rule[string] `json:"city" tune:"name:City; desc:As the geo database names it, in lower case; source:cities; custom:true"`
	Languages tunables.Rule[string] `json:"languages" tune:"name:Browser language; desc:One of the languages of the browser has to be in; source:languages; custom:true"`
	Browser   tunables.Rule[string] `json:"browser" tune:"name:Browser; source:browsers; custom:true"`
	OS        tunables.Rule[string] `json:"os" tune:"name:System; source:systems; custom:true"`
	Device    tunables.Rule[string] `json:"device" tune:"name:Device; source:devices"`
	Host      tunables.Rule[string] `json:"host" tune:"name:Host; desc:The host that was asked for"`
}

// Option configures NewParser.
type Option func(*Parser)

// WithGeo plugs in where addresses are (a GeoLocator, or a GeoFunc). Without it the place is not known.
func WithGeo(g GeoLocator) Option { return func(h *Parser) { h.geo = g } }

// WithUserAgent plugs in another reader of the User-Agent than DefaultUserAgentParser.
func WithUserAgent(p UserAgentParser) Option { return func(h *Parser) { h.ua = p } }

// WithClientIPHeader takes the address of the client from a header ("X-Real-IP", "CF-Connecting-IP") that a proxy of
// yours puts there, instead of the address of the connection. Use it only when every request comes through that
// proxy: anyone else can write the header.
func WithClientIPHeader(name string) Option { return func(h *Parser) { h.ipHeader = name } }

// Parser reads the info of requests, with the providers it was given.
type Parser struct {
	geo      GeoLocator
	ua       UserAgentParser
	ipHeader string
}

// NewParser sets up the providers once; Parse is then called for each request.
func NewParser(opts ...Option) *Parser {
	p := &Parser{ua: DefaultUserAgentParser}
	for _, opt := range opts {
		opt(p)
	}
	if p.ua == nil {
		p.ua = DefaultUserAgentParser
	}
	return p
}

// Parse is NewParser(opts...).Parse(r), for a program that reads a request at one place only.
func Parse(r *http.Request, opts ...Option) Info { return NewParser(opts...).Parse(r) }

// Parse reads what a request says about its sender: the address, and the place it is in when there is a GeoLocator, the
// languages, the browser, the system and the kind of device, and the host.
func (h *Parser) Parse(r *http.Request) Info {
	info := Info{Host: strings.ToLower(hostOnly(r.Host))}

	ip, ok := netip.Addr{}, false
	if h.ipHeader != "" {
		ip, ok = parseIP(r.Header.Get(h.ipHeader)) // a header that is not an address is as if there were none
	}
	if !ok {
		ip, ok = parseIP(r.RemoteAddr)
	}
	if ok {
		info.IP = ip.String()
		if h.geo != nil {
			g := h.geo.Locate(ip)
			info.Country, info.Region, info.City = strings.ToLower(g.Country), strings.ToLower(g.Region), strings.ToLower(g.City)
		}
	}

	info.Languages = acceptedLanguages(r.Header.Get("Accept-Language"))
	if len(info.Languages) > 0 {
		info.Language = info.Languages[0]
	}
	ua := h.ua.Parse(r.UserAgent())
	info.Browser, info.OS, info.Device = strings.ToLower(ua.Browser), strings.ToLower(ua.OS), strings.ToLower(ua.Device)
	return info
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// parseIP reads an address from "1.2.3.4", "1.2.3.4:5678", "[::1]:80" or a list of them (the first one: X-Forwarded-For).
func parseIP(s string) (netip.Addr, bool) {
	s, _, _ = strings.Cut(s, ",")
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap(), true
	}
	if ip, err := netip.ParseAddr(s); err == nil {
		return ip.Unmap(), true
	}
	return netip.Addr{}, false
}

// acceptedLanguages are the languages of an Accept-Language header by their weight, without the region ("en-US" is "en")
// and without repeats.
func acceptedLanguages(header string) []string {
	type lang struct {
		code string
		q    float64
	}
	var langs []lang
	for _, part := range strings.Split(header, ",") {
		code, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		code = strings.ToLower(strings.TrimSpace(code))
		code, _, _ = strings.Cut(code, "-")
		if code == "" || code == "*" {
			continue
		}
		q := 1.0
		if _, v, ok := strings.Cut(strings.ReplaceAll(params, " ", ""), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q <= 0 {
			continue
		}
		langs = append(langs, lang{code, q})
	}
	// stable by weight: the order in the header decides between equal ones
	for i := 1; i < len(langs); i++ {
		for j := i; j > 0 && langs[j].q > langs[j-1].q; j-- {
			langs[j], langs[j-1] = langs[j-1], langs[j]
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range langs {
		if !seen[l.code] {
			seen[l.code] = true
			out = append(out, l.code)
		}
	}
	return out
}
