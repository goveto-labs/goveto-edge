package analytics

import (
	"context"
	"sync"
	"time"
)

type LiveFilter struct {
	ClusterID string
	SiteID    string
	NodeID    string
}

type LiveRequestLog struct {
	EventTime     time.Time `json:"event_time"`
	RequestID     string    `json:"request_id,omitempty"`
	ClusterID     string    `json:"cluster_id"`
	NodeID        string    `json:"node_id"`
	SiteID        string    `json:"site_id"`
	ConfigVersion uint64    `json:"config_version"`
	Hostname      string    `json:"hostname"`
	Method        string    `json:"method"`
	Path          string    `json:"path"`
	StatusCode    uint16    `json:"status_code"`
	DurationUS    uint64    `json:"duration_us"`
	CacheStatus   string    `json:"cache_status"`
	WAFAction     string    `json:"waf_action,omitempty"`
}

type liveSubscriber struct {
	filter LiveFilter
	events chan LiveRequestLog
}

type LiveBroker struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]liveSubscriber
}

func NewLiveBroker() *LiveBroker {
	return &LiveBroker{subscribers: map[uint64]liveSubscriber{}}
}

func (b *LiveBroker) Subscribe(ctx context.Context, filter LiveFilter, buffer int) <-chan LiveRequestLog {
	if buffer < 1 {
		buffer = 256
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	events := make(chan LiveRequestLog, buffer)
	b.subscribers[id] = liveSubscriber{filter: filter, events: events}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subscribers, id)
		b.mu.Unlock()
	}()
	return events
}

func (b *LiveBroker) Publish(events []WebRequestLog) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, event := range events {
		live := LiveRequestLog{
			EventTime: event.EventTime, RequestID: event.RequestID, ClusterID: event.ClusterID,
			NodeID: event.NodeID, SiteID: event.SiteID, ConfigVersion: event.ConfigVersion,
			Hostname: event.Hostname, Method: event.Method, Path: event.Path,
			StatusCode: event.StatusCode, DurationUS: uint64(event.Duration.Microseconds()),
			CacheStatus: event.CacheStatus, WAFAction: event.WAFAction,
		}
		for _, subscriber := range b.subscribers {
			if !subscriber.filter.matches(event) {
				continue
			}
			select {
			case subscriber.events <- live:
			default:
				// Slow subscribers lose live updates without slowing durable ingest.
			}
		}
	}
}

func (f LiveFilter) matches(event WebRequestLog) bool {
	return (f.ClusterID == "" || f.ClusterID == event.ClusterID) &&
		(f.SiteID == "" || f.SiteID == event.SiteID) &&
		(f.NodeID == "" || f.NodeID == event.NodeID)
}
