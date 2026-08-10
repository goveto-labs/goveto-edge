package analytics

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/oschwald/geoip2-golang"
)

type geoIPEnricher struct {
	cityPath   string
	asnPath    string
	mu         sync.Mutex
	cityReader *geoip2.Reader
	cityInfo   os.FileInfo
	asnReader  *geoip2.Reader
	asnInfo    os.FileInfo
}

func newGeoIPEnricher(cityPath string, asnPaths ...string) *geoIPEnricher {
	cityPath = strings.TrimSpace(cityPath)
	asnPath := ""
	if len(asnPaths) > 0 {
		asnPath = strings.TrimSpace(asnPaths[0])
	}
	if cityPath == "" && asnPath == "" {
		return nil
	}
	return &geoIPEnricher{cityPath: cityPath, asnPath: asnPath}
}

func (g *geoIPEnricher) enrich(events []WebRequestLog) {
	if g == nil || len(events) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	cityReader := currentGeoIPReader(g.cityPath, &g.cityReader, &g.cityInfo)
	asnReader := currentGeoIPReader(g.asnPath, &g.asnReader, &g.asnInfo)
	if cityReader == nil && asnReader == nil {
		return
	}
	for index := range events {
		ip := events[index].ClientIP
		if !ip.IsValid() || ip.IsUnspecified() {
			continue
		}
		netIP := net.IP(ip.Unmap().AsSlice())
		if cityReader != nil {
			record, err := cityReader.City(netIP)
			if err == nil {
				country := strings.ToUpper(record.Country.IsoCode)
				region := ""
				if len(record.Subdivisions) > 0 {
					region = strings.ToUpper(record.Subdivisions[0].IsoCode)
					if country != "" && region != "" {
						region = country + "-" + region
					}
				}
				events[index].Country = country
				events[index].Region = region
			}
		}
		if asnReader != nil {
			if record, err := asnReader.ISP(netIP); err == nil {
				events[index].ISP = formatISP(record.AutonomousSystemNumber, record.ISP, record.AutonomousSystemOrganization)
			} else if record, asnErr := asnReader.ASN(netIP); asnErr == nil {
				events[index].ISP = formatISP(record.AutonomousSystemNumber, "", record.AutonomousSystemOrganization)
			}
		}
	}
}

func formatISP(asn uint, isp, organization string) string {
	name := strings.TrimSpace(isp)
	if name == "" {
		name = strings.TrimSpace(organization)
	}
	if asn == 0 {
		return name
	}
	if name == "" {
		return fmt.Sprintf("AS%d", asn)
	}
	return fmt.Sprintf("AS%d · %s", asn, name)
}

func currentGeoIPReader(path string, reader **geoip2.Reader, previous *os.FileInfo) *geoip2.Reader {
	if path == "" {
		resetGeoIPReader(reader, previous)
		return nil
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		resetGeoIPReader(reader, previous)
		return nil
	}
	if *reader != nil && sameGeoIPFile(info, *previous) {
		return *reader
	}
	next, err := geoip2.Open(path)
	if err != nil {
		resetGeoIPReader(reader, previous)
		return nil
	}
	if *reader != nil {
		_ = (*reader).Close()
	}
	*reader = next
	*previous = info
	return next
}

func resetGeoIPReader(reader **geoip2.Reader, info *os.FileInfo) {
	if *reader != nil {
		_ = (*reader).Close()
	}
	*reader = nil
	*info = nil
}

func sameGeoIPFile(current, previous os.FileInfo) bool {
	return current != nil && previous != nil && os.SameFile(current, previous) &&
		current.Size() == previous.Size() && current.ModTime().Equal(previous.ModTime())
}
