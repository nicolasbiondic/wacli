package calls

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// EventHub fans call events out to `wacli calls watch` subscribers as NDJSON
// lines. Publishing never blocks: a subscriber that does not keep up loses
// lines (counted in its "dropped" field) instead of slowing the session.
type EventHub struct {
	mu     sync.Mutex
	subs   map[*hubSub]struct{}
	max    int
	buffer int
	closed bool
}

type hubSub struct {
	ch      chan []byte
	dropped int
}

// ErrTooManyWatchers is returned when the hub is at its subscriber cap.
var ErrTooManyWatchers = errors.New("too many call watchers")

// NewEventHub returns a hub with at most maxSubs subscribers, each with a
// buffer of buffer lines.
func NewEventHub(maxSubs, buffer int) *EventHub {
	if maxSubs <= 0 {
		maxSubs = 8
	}
	if buffer <= 0 {
		buffer = 64
	}
	return &EventHub{subs: map[*hubSub]struct{}{}, max: maxSubs, buffer: buffer}
}

// hubLine is the same envelope `--events` uses on stderr ({"event","data","ts"}),
// plus how many lines this subscriber lost before this one.
type hubLine struct {
	Event   string         `json:"event"`
	Data    map[string]any `json:"data,omitempty"`
	TS      int64          `json:"ts"`
	Dropped int            `json:"dropped,omitempty"`
}

// Publish sends one event to every subscriber.
func (h *EventHub) Publish(event string, fields map[string]any) {
	line := hubLine{Event: event, Data: fields, TS: time.Now().UTC().UnixMilli()}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.subs) == 0 {
		return
	}
	for sub := range h.subs {
		line.Dropped = sub.dropped
		b, err := json.Marshal(line)
		if err != nil {
			return
		}
		b = append(b, '\n')
		select {
		case sub.ch <- b:
			sub.dropped = 0
		default:
			sub.dropped++
		}
	}
}

// Subscribe registers a subscriber. The returned cancel must be called.
func (h *EventHub) Subscribe() (<-chan []byte, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, errors.New("call event hub is closed")
	}
	if len(h.subs) >= h.max {
		return nil, nil, ErrTooManyWatchers
	}
	sub := &hubSub{ch: make(chan []byte, h.buffer)}
	h.subs[sub] = struct{}{}
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subs[sub]; ok {
				delete(h.subs, sub)
				close(sub.ch)
			}
			h.mu.Unlock()
		})
	}
	return sub.ch, cancel, nil
}

// Watchers returns the number of subscribers.
func (h *EventHub) Watchers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close ends every subscription.
func (h *EventHub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for sub := range h.subs {
		delete(h.subs, sub)
		close(sub.ch)
	}
}
