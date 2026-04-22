package apxtrace

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type stubRT struct {
	resp *http.Response
	err  error
}

func (s *stubRT) RoundTrip(*http.Request) (*http.Response, error) { return s.resp, s.err }

func TestTraceTransport_NoActiveTrace_DelegatesCleanly(t *testing.T) {
	fr := newFakeRedis()
	_ = fr
	tt := &TraceTransport{inner: &stubRT{resp: &http.Response{StatusCode: 200, Header: http.Header{}}}}
	r := httptest.NewRequest("GET", "http://up.local/foo", nil)
	resp, err := tt.RoundTrip(r)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
}

func TestTraceTransport_ActiveTrace_EmitsRequestAndResponse(t *testing.T) {
	fr := newFakeRedis()
	tc := newTraceContextFor(t, "tok", fr)
	tt := &TraceTransport{
		inner:    &stubRT{resp: &http.Response{StatusCode: 502, Header: http.Header{"Server": {"nginx"}}}},
		redactor: DefaultRedactor(),
	}

	r := httptest.NewRequest("GET", "http://up.local/foo", nil)
	r = r.WithContext(withTrace(r.Context(), tc))

	_, err := tt.RoundTrip(r)
	require.NoError(t, err)

	require.Eventually(t, func() bool { return fr.addCount() >= 2 }, time.Second, 10*time.Millisecond)
}

func TestTraceTransport_Error_EmitsUpstreamError(t *testing.T) {
	fr := newFakeRedis()
	tc := newTraceContextFor(t, "tok", fr)
	tt := &TraceTransport{
		inner:    &stubRT{err: errors.New("dial tcp: timeout")},
		redactor: DefaultRedactor(),
	}

	r := httptest.NewRequest("GET", "http://up.local/foo", nil)
	r = r.WithContext(withTrace(r.Context(), tc))

	_, err := tt.RoundTrip(r)
	require.Error(t, err)

	require.Eventually(t, func() bool { return fr.addCount() >= 2 }, time.Second, 10*time.Millisecond)
}
