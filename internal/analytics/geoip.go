package analytics

import (
	"net"
	"os"
	"strings"
	"sync"

	"github.com/oschwald/geoip2-golang"
)

type geoIPEnricher struct {
	path   string
	mu     sync.Mutex
	reader *geoip2.Reader
	info   os.FileInfo
}

func newGeoIPEnricher(path string) *geoIPEnricher {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	return &geoIPEnricher{path: path}
}

func (g *geoIPEnricher) enrich(events []WebRequestLog) {
	if g == nil || len(events) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	reader := g.currentReader()
	if reader == nil {
		return
	}
	for index := range events {
		ip := events[index].ClientIP
		if !ip.IsValid() || ip.IsUnspecified() {
			continue
		}
		record, err := reader.City(net.IP(ip.Unmap().AsSlice()))
		if err != nil {
			continue
		}
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

func (g *geoIPEnricher) currentReader() *geoip2.Reader {
	info, err := os.Stat(g.path)
	if err != nil || !info.Mode().IsRegular() {
		g.reset()
		return nil
	}
	if g.reader != nil && sameGeoIPFile(info, g.info) {
		return g.reader
	}
	reader, err := geoip2.Open(g.path)
	if err != nil {
		g.reset()
		return nil
	}
	if g.reader != nil {
		_ = g.reader.Close()
	}
	g.reader = reader
	g.info = info
	return reader
}

func (g *geoIPEnricher) reset() {
	if g.reader != nil {
		_ = g.reader.Close()
	}
	g.reader = nil
	g.info = nil
}

func sameGeoIPFile(current, previous os.FileInfo) bool {
	return current != nil && previous != nil && os.SameFile(current, previous) &&
		current.Size() == previous.Size() && current.ModTime().Equal(previous.ModTime())
}
