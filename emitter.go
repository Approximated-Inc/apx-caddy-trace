package apxtrace

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// streamer is the subset of *redis.Client used by Emitter. Allows fakes.
type streamer interface {
	XAdd(ctx context.Context, args *redis.XAddArgs) *redis.StringCmd
}

// Emitter writes events to a single Redis stream asynchronously.
// Bounded buffer drops oldest on overflow.
type Emitter struct {
	streamKey string
	client    streamer
	ch        chan Event
	done      chan struct{}
	wg        sync.WaitGroup
	dropped   uint64
}

const streamMaxLen int64 = 1000

// NewEmitter spawns a goroutine draining events. Call Close to flush + stop.
func NewEmitter(c streamer, streamKey string, bufferSize int) *Emitter {
	if bufferSize <= 0 {
		bufferSize = 64
	}
	e := &Emitter{
		streamKey: streamKey,
		client:    c,
		ch:        make(chan Event, bufferSize),
		done:      make(chan struct{}),
	}
	e.wg.Add(1)
	go e.run()
	return e
}

// Emit enqueues an event. Drops oldest buffered event on overflow.
func (e *Emitter) Emit(evt Event) {
	select {
	case e.ch <- evt:
	default:
		// Drop oldest to make room.
		select {
		case <-e.ch:
			atomic.AddUint64(&e.dropped, 1)
			metricEventsDropped.Inc()
		default:
		}
		select {
		case e.ch <- evt:
		default:
			atomic.AddUint64(&e.dropped, 1)
			metricEventsDropped.Inc()
		}
	}
}

// Dropped returns the cumulative count of dropped events.
func (e *Emitter) Dropped() uint64 { return atomic.LoadUint64(&e.dropped) }

// Close signals shutdown and waits for the drain goroutine to exit.
func (e *Emitter) Close() {
	close(e.done)
	e.wg.Wait()
}

func (e *Emitter) run() {
	defer e.wg.Done()
	for {
		select {
		case evt := <-e.ch:
			e.write(evt)
		case <-e.done:
			// Drain remaining
			for {
				select {
				case evt := <-e.ch:
					e.write(evt)
				default:
					return
				}
			}
		}
	}
}

func (e *Emitter) write(evt Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body, err := json.Marshal(evt)
	if err != nil {
		metricRedisErrors.Inc()
		return
	}
	args := &redis.XAddArgs{
		Stream: e.streamKey,
		MaxLen: streamMaxLen,
		Approx: true,
		Values: map[string]interface{}{"e": body},
	}
	start := time.Now()
	if err := e.client.XAdd(ctx, args).Err(); err != nil {
		metricRedisErrors.Inc()
		return
	}
	metricRedisWriteDuration.Observe(time.Since(start).Seconds())
	metricEventsEmittedTotal.WithLabelValues(evt.Type).Inc()
}
