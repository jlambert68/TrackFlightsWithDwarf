package flight

import (
	"github.com/jlambert/trackflights/internal/flight/adsb"
	"math"
	"time"
)

const localCPRReferenceRangeMeters = 180 * 1852.0

type cprValue struct {
	lat, lon int
	odd      bool
	received time.Time
}

type cprDecodeReference struct {
	latitude  float64
	longitude float64
	source    string
}

func updateCPR(m *adsb.Message, states map[string][2]*cprValue, receivedAt time.Time, reference cprDecodeReference) {
	p := states[m.ICAO]
	v := &cprValue{lat: m.CPRLat, lon: m.CPRLon, odd: m.CPROdd, received: receivedAt}
	if m.CPROdd {
		p[1] = v
	} else {
		p[0] = v
	}
	states[m.ICAO] = p
	if decodeGlobalCPR(m, p) {
		return
	}
	decodeLocalCPR(m, v, reference)
}

func decodeGlobalCPR(m *adsb.Message, p [2]*cprValue) bool {
	if p[0] == nil || p[1] == nil || p[0].received.Sub(p[1].received).Abs() > 10*time.Second {
		return false
	}
	ye, yo := float64(p[0].lat)/131072, float64(p[1].lat)/131072
	xe, xo := float64(p[0].lon)/131072, float64(p[1].lon)/131072
	j := math.Floor(59*ye - 60*yo + .5)
	latEven := 6 * (positiveMod(j, 60) + ye)
	if latEven >= 270 {
		latEven -= 360
	}
	latOdd := (360.0 / 59.0) * (positiveMod(j, 59) + yo)
	if latOdd >= 270 {
		latOdd -= 360
	}
	if cprNL(latEven) != cprNL(latOdd) {
		return false
	}
	latestOdd := p[1].received.After(p[0].received)
	lat := latEven
	if latestOdd {
		lat = latOdd
	}
	nl := cprNL(lat)
	ni := nl
	if latestOdd {
		ni = nl - 1
	}
	if ni < 1 {
		ni = 1
	}
	mm := math.Floor(xe*float64(nl-1) - xo*float64(nl) + .5)
	x := xe
	if latestOdd {
		x = xo
	}
	lon := 360 / float64(ni) * (positiveMod(mm, float64(ni)) + x)
	if lon > 180 {
		lon -= 360
	}
	m.Latitude, m.Longitude = &lat, &lon
	m.PositionTime = p[0].received
	if latestOdd {
		m.PositionTime = p[1].received
		m.PositionReference = "odd"
	} else {
		m.PositionReference = "even"
	}
	return true
}

func decodeLocalCPR(m *adsb.Message, value *cprValue, reference cprDecodeReference) bool {
	if reference.latitude < -90 || reference.latitude > 90 || reference.longitude < -180 || reference.longitude > 180 {
		return false
	}
	format := 0
	if value.odd {
		format = 1
	}
	y := float64(value.lat) / 131072
	x := float64(value.lon) / 131072
	dLat := 360 / float64(60-format)
	latitudeIndex := math.Floor(reference.latitude/dLat) + math.Floor(.5+positiveMod(reference.latitude, dLat)/dLat-y)
	latitude := dLat * (latitudeIndex + y)
	if latitude >= 270 {
		latitude -= 360
	}
	if latitude < -90 || latitude > 90 || math.Abs(latitude-reference.latitude) > dLat/2 {
		return false
	}
	longitudeZones := cprNL(latitude) - format
	if longitudeZones < 1 {
		longitudeZones = 1
	}
	dLon := 360 / float64(longitudeZones)
	longitudeIndex := math.Floor(reference.longitude/dLon) + math.Floor(.5+positiveMod(reference.longitude, dLon)/dLon-x)
	longitude := dLon * (longitudeIndex + x)
	if longitude > 180 {
		longitude -= 360
	} else if longitude < -180 {
		longitude += 360
	}
	if distanceMeters(reference.latitude, reference.longitude, latitude, longitude) > localCPRReferenceRangeMeters {
		return false
	}
	m.Latitude, m.Longitude = &latitude, &longitude
	m.PositionTime = value.received
	parity := "even"
	if value.odd {
		parity = "odd"
	}
	m.PositionReference = "local_" + reference.source + "_" + parity
	return true
}

func distanceMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6_371_000.0
	phi1, phi2 := lat1*math.Pi/180, lat2*math.Pi/180
	dPhi, dLambda := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dPhi/2)*math.Sin(dPhi/2) + math.Cos(phi1)*math.Cos(phi2)*math.Sin(dLambda/2)*math.Sin(dLambda/2)
	return earthRadius * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func positiveMod(value, divisor float64) float64 {
	return math.Mod(math.Mod(value, divisor)+divisor, divisor)
}
func cprNL(x float64) int {
	latitude := math.Abs(x)
	if latitude < 1e-9 {
		return 59
	}
	if math.Abs(latitude-87) < 1e-9 {
		return 2
	}
	if latitude > 87 {
		return 1
	}
	cosine := math.Cos(latitude * math.Pi / 180)
	angle := math.Acos(1 - (1-math.Cos(math.Pi/30))/(cosine*cosine))
	zones := int(math.Floor(2 * math.Pi / angle))
	return max(1, min(59, zones))
}
