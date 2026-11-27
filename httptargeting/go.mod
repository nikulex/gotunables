module github.com/nikulex/gotunables/httptargeting

go 1.24

require (
	github.com/biter777/countries v1.7.5
	github.com/emvi/iso-639-1 v1.1.1
	github.com/mileusna/useragent v1.3.5
	github.com/nikulex/gotunables v0.4.0
)

// While the next version of gotunables is not tagged: the one next to it. Remove it when it is, and require that version.
replace github.com/nikulex/gotunables => ../
