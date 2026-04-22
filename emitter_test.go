package apxtrace

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type fakeStreamer struct {
	mu   sync.Mutex
	adds []*redis.XAddArgs
	err  error
	slow time.Duration
}

func (f *fakeStreamer) XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd {
	if f.slow > 0 {
		time.Sleep(f.slow)
	}
	cmd := redis.NewStringCmd(ctx)
	if f.err != nil {
		cmd.SetErr(f.err)
		return cmd
	}
	f.mu.Lock()
	f.adds = append(f.adds, a)
	f.mu.Unlock()
	cmd.SetVal("0-1")
	return cmd
}

func (f *fakeStreamer) addCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.adds)
}

func TestEmitter_WritesToRedisStream(t *testing.T) {
	fake := &fakeStreamer{}
	e := NewEmitter(fake, "debug:trace:tok:events", 10)
	defer e.Close()

	e.Emit(Event{Type: EventClusterReceived, TsNs: 1, Source: SourceCluster})

	require.Eventually(t, func() bool { return fake.addCount() == 1 },
		time.Second, 10*time.Millisecond)
}

func TestEmitter_DropsWhenBufferFull(t *testing.T) {
	fake := &fakeStreamer{slow: 50 * time.Millisecond}
	e := NewEmitter(fake, "debug:trace:tok:events", 2)
	defer e.Close()

	for i := 0; i < 20; i++ {
		e.Emit(Event{Type: EventClusterReceived, TsNs: int64(i), Source: SourceCluster})
	}
	// Wait for drain
	time.Sleep(2 * time.Second)

	require.Greater(t, e.Dropped(), uint64(0), "expected some drops")
}
