package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/config/writer"
	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/override"
)

const (
	// DefaultHeartbeat is the default heartbeat interval of the event
	// stream. The client takes the connection for lost after three
	// intervals without a single event.
	DefaultHeartbeat = 15 * time.Second
	// aggregateEvery is the aggregation interval for the new exchanges and
	// the live override state: at most one update of each per interval.
	aggregateEvery = time.Second
	// maxPerEvent caps the exchanges of one exchanges event; the rest of
	// the interval is counted in dropped.
	maxPerEvent = 200
	// streamBuffer is how many exchanges a connection buffers between two
	// reads from the broker. With the buffer full the exchange is counted
	// as dropped: publishing never waits on a connection.
	streamBuffer = 1024
	// hubBuffer is how many configuration and history events a connection
	// buffers while it writes.
	hubBuffer = 64
	// writeTimeout caps each write to the stream. A client that does not
	// read within that deadline is disconnected and reconnects when it can.
	writeTimeout = 10 * time.Second
)

func (h *Handler) eventRoutes() {
	h.handle("/api/events", map[string]http.HandlerFunc{
		"GET": h.stream,
	})
	h.handle("/api/status", map[string]http.HandlerFunc{
		"GET": h.getStatus,
	})
}

// streamKey holds, in the request context, the signal that the server has
// gone out of service.
type streamKey struct{}

// StreamContext adds to ctx the stop signal, closed when the server that
// serves the request goes out of service (shutdown or port change). The
// event stream ends when it arrives, instead of holding the server's
// graceful Shutdown indefinitely; the client reconnects.
func StreamContext(ctx context.Context, stop <-chan struct{}) context.Context {
	return context.WithValue(ctx, streamKey{}, stop)
}

func streamStop(ctx context.Context) <-chan struct{} {
	stop, _ := ctx.Value(streamKey{}).(<-chan struct{})
	return stop // nil: never signaled
}

// event is an event ready for the stream, with its data already serialized.
type event struct {
	name string
	data []byte
}

// hub fans the configuration and history events out to the open
// connections. Publishing never waits: a connection with a full buffer
// loses the event, like any event lost on a disconnect, and the client
// reloads over REST whatever it shows.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newHub() *hub { return &hub{subs: map[chan event]struct{}{}} }

func (h *hub) subscribe() chan event {
	c := make(chan event, hubBuffer)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *hub) unsubscribe(c chan event) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}

func (h *hub) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := event{name, data}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

// configEvent is the event of an applied configuration change.
type configEvent struct {
	// Cause is api, reload or learning.
	Cause    string   `json:"cause"`
	Routes   []string `json:"routes"`
	Settings []string `json:"settings"`
}

// historyEvent is the event of the history being cleared or the backend
// being switched.
type historyEvent struct {
	// Cause is cleared or backend.
	Cause   string `json:"cause"`
	Backend string `json:"backend"`
}

// configChanged publishes every change the Writer applies, be it from the
// API, a reload or learning.
func (h *Handler) configChanged(c writer.Change) {
	routes, settings := c.Routes, c.Settings
	if routes == nil {
		routes = []string{}
	}
	if settings == nil {
		settings = []string{}
	}
	h.events.publish("config", configEvent{Cause: c.Cause, Routes: routes, Settings: settings})
}

// exchangesEvent aggregates the exchanges recorded in one interval.
type exchangesEvent struct {
	Items   []exchange.Exchange `json:"items"`
	Dropped uint64              `json:"dropped"`
}

type overridesEvent struct {
	Now   time.Time            `json:"now"`
	Items []override.LiveState `json:"items"`
}

type heartbeatEvent struct {
	Now time.Time `json:"now"`
}

// stream serves the panel's text/event-stream: hello on connect; the new
// exchanges aggregated into at most one update per second; the
// configuration and history changes; the live override state when it
// changes in a non-continuous way, also aggregated per second; the
// upstream availability when the status of one of them changes, checked
// on the same interval; and a periodic heartbeat, which keeps the
// connection alive with no traffic.
//
// Nothing here blocks the proxy: the exchanges arrive over the broker,
// which never waits on a subscriber, and every write has a deadline, so a
// slow client loses events (counted in dropped) and, once stalled, is
// disconnected.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// The write deadline applies to the connection: without clearing it on
	// the way out, a later request on the same connection would inherit the
	// expired deadline.
	defer rc.SetWriteDeadline(time.Time{})
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := h.rec.Broker().Subscribe(streamBuffer)
	defer sub.Close()
	evs := h.events.subscribe()
	defer h.events.unsubscribe(evs)

	var buf bytes.Buffer
	send := func(name string, data []byte) bool {
		buf.Reset()
		fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", name, data)
		rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := w.Write(buf.Bytes()); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	sendJSON := func(name string, v any) bool {
		data, err := json.Marshal(v)
		return err == nil && send(name, data)
	}

	if !sendJSON("hello", h.status()) {
		return
	}
	lastStates := statesKey(h.overrides.States())
	lastUpstreams := upstreamsKey(h.upstreamsReport())

	tick := time.NewTicker(aggregateEvery)
	defer tick.Stop()
	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()
	stop := streamStop(r.Context())
	var pending []exchange.Exchange
	var dropped uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stop:
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if len(pending) == maxPerEvent {
				// The newest ones stay; the oldest of the interval are
				// counted as dropped.
				pending = append(pending[:0], pending[1:]...)
				dropped++
			}
			pending = append(pending, e.Summary())
		case ev := <-evs:
			if !send(ev.name, ev.data) {
				return
			}
		case <-tick.C:
			dropped += sub.TakeDropped()
			if len(pending) > 0 || dropped > 0 {
				// With history exposure off, the exchanges do not go
				// out over the stream.
				if h.live.Load().Settings.HistoryExpose {
					if !sendJSON("exchanges", exchangesEvent{Items: pending, Dropped: dropped}) {
						return
					}
				}
				pending, dropped = nil, 0
			}
			states, now := h.overrides.States()
			if key := statesKey(states, now); key != lastStates {
				lastStates = key
				if !sendJSON("overrides", overridesEvent{Now: now, Items: states}) {
					return
				}
			}
			if ups := h.upstreamsReport(); upstreamsKey(ups) != lastUpstreams {
				lastUpstreams = upstreamsKey(ups)
				if !sendJSON("upstreams", ups) {
					return
				}
			}
		case <-beat.C:
			if !sendJSON("heartbeat", heartbeatEvent{Now: h.now().UTC()}) {
				return
			}
		}
	}
}

// statesKey summarizes the live override state without the remaining
// lifetime, which changes continuously and the panel counts down on its
// own: the overrides event only goes out when something changes in a
// non-continuous way, that is, an override appears, disappears, expires,
// is reset, is turned on or off, or is applied.
func statesKey(states []override.LiveState, _ time.Time) string {
	var b bytes.Buffer
	for _, s := range states {
		fmt.Fprintf(&b, "%s/%s:%t:%t:%v:%d:%d", s.Route, s.Override, s.Enabled, s.Active,
			s.RegisteredAt.UnixNano(), s.Applications, ptrVal(s.MaxApplications))
		if s.Expired != nil {
			b.WriteString(":" + *s.Expired)
		}
		if s.TTLRemainingMs == nil {
			b.WriteString(":nottl")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func ptrVal(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// statusBody is the summarized process state, also sent in the hello.
type statusBody struct {
	Version       string        `json:"version"`
	SchemaVersion int           `json:"schemaVersion"`
	StartedAt     time.Time     `json:"startedAt"`
	ConfigPath    string        `json:"configPath"`
	RoutesDir     string        `json:"routesDir"`
	Ports         statusPorts   `json:"ports"`
	History       statusHistory `json:"history"`
	Learning      statusLearn   `json:"learning"`
	Routes        int           `json:"routes"`
}

type statusPorts struct {
	Traffic int `json:"traffic"`
	Admin   int `json:"admin"`
}

type statusHistory struct {
	Backend string `json:"backend"`
	Record  bool   `json:"record"`
	Expose  bool   `json:"expose"`
}

type statusLearn struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) status() statusBody {
	snap := h.live.Load()
	s := snap.Settings
	b := statusBody{
		Version:       Version,
		SchemaVersion: config.SchemaVersion,
		StartedAt:     h.startedAt.UTC(),
		ConfigPath:    h.loader.ConfigPath,
		RoutesDir:     s.RoutesDir,
		Ports:         statusPorts{Traffic: s.TrafficPort, Admin: s.AdminPort},
		History:       statusHistory{Backend: h.history.Backend(), Record: s.HistoryRecord, Expose: s.HistoryExpose},
		Learning:      statusLearn{Enabled: s.LearningEnabled},
		Routes:        len(snap.Routes),
	}
	if h.ports != nil {
		b.Ports.Traffic, b.Ports.Admin = h.ports()
	}
	return b
}

// getStatus returns the summarized process state.
func (h *Handler) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.status())
}
