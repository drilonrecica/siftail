package auth

import (
	"sync"
	"time"
)

// displayZone is the zone every interface time renders in (DESIGN.md §12.2,
// ADR 0011 D7): UTC by default, or the IANA zone the operator chose in the
// time zone preference. Formats are fixed and locale-independent so columns
// never move, and every rendered time names its zone somewhere visible.
type displayZone struct {
	name string
	loc  *time.Location
}

var utcDisplayZone = displayZone{name: "UTC", loc: time.UTC}

// loadedZones caches successfully loaded zones by name. Only names that
// time.LoadLocation resolved are stored, so the cache is bounded by the size
// of the time zone database however many distinct values clients send.
var loadedZones sync.Map

// loadDisplayZone validates a preference value and resolves it: at most 64
// characters from [A-Za-z0-9_+/-] that time.LoadLocation resolves. "Local"
// is rejected because Go maps it to the server's own zone, which the browser
// never chose. Anything else falls back to UTC, reported as not ok.
func loadDisplayZone(name string) (displayZone, bool) {
	if name == "" || len(name) > 64 || name == "Local" {
		return utcDisplayZone, false
	}
	for index := 0; index < len(name); index++ {
		char := name[index]
		if !(char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' ||
			char >= '0' && char <= '9' || char == '_' || char == '+' ||
			char == '/' || char == '-') {
			return utcDisplayZone, false
		}
	}
	if name == "UTC" || name == "utc" {
		return utcDisplayZone, true
	}
	if cached, ok := loadedZones.Load(name); ok {
		return cached.(displayZone), true
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return utcDisplayZone, false
	}
	zone := displayZone{name: name, loc: location}
	loadedZones.Store(name, zone)
	return zone, true
}

// cookieValue is the token stored in the preference cookie's fourth field.
func (z displayZone) cookieValue() string {
	if z.isUTC() {
		return "utc"
	}
	return z.name
}

func (z displayZone) isUTC() bool {
	return z.name == "UTC"
}

// row formats a log-row timestamp: 15:04:05.000, or 02 Jan 15:04:05.000 when
// the queried range spans more than one day. The zone is labelled once, in
// the result summary and the tail line, not on every row.
func (z displayZone) row(value time.Time, showDate bool) string {
	if showDate {
		return value.In(z.loc).Format("02 Jan 15:04:05.000")
	}
	return value.In(z.loc).Format("15:04:05.000")
}

// rangeText formats an absolute range with its zone label.
func (z displayZone) rangeText(from, to time.Time) string {
	return from.In(z.loc).Format("02 Jan 2006 15:04") + "–" +
		to.In(z.loc).Format("02 Jan 2006 15:04") + " " + z.name
}

// clock formats an absolute time to the second without a zone label, for
// places that label the zone once in a heading.
func (z displayZone) clock(value time.Time) string {
	return value.In(z.loc).Format("02 Jan 2006 15:04:05")
}

// instant formats an absolute time to the second with its zone label, for
// Status, Sources, Servers, Backup, and the export dialog.
func (z displayZone) instant(value time.Time) string {
	return z.clock(value) + " " + z.name
}

// datetime is the machine-readable value every <time> carries: UTC RFC3339.
func datetime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
