package apxtrace

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSnapshot_CapturesMethodURIHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "https://example.com/foo?x=1", nil)
	r.Header.Set("X-Foo", "bar")
	s := Snapshot(r)
	require.Equal(t, "GET", s.Method)
	require.Equal(t, "/foo?x=1", s.URI)
	require.Equal(t, "bar", s.Headers.Get("X-Foo"))
}

func TestDiffSnapshots_DetectsHeaderAddRemoveChange(t *testing.T) {
	before := RequestSnapshot{
		Method:  "GET",
		URI:     "/a",
		Headers: http.Header{"X-Keep": {"1"}, "X-Remove": {"x"}, "X-Change": {"old"}},
	}
	after := RequestSnapshot{
		Method:  "GET",
		URI:     "/a",
		Headers: http.Header{"X-Keep": {"1"}, "X-Add": {"y"}, "X-Change": {"new"}},
	}
	diff := DiffSnapshots(before, after)
	require.Contains(t, diff.HeadersAdded, "X-Add")
	require.Contains(t, diff.HeadersRemoved, "X-Remove")
	require.Contains(t, diff.HeadersChanged, "X-Change")
	require.NotContains(t, diff.HeadersChanged, "X-Keep")
	require.Empty(t, diff.URIChange)
}

func TestDiffSnapshots_URIAndMethodChange(t *testing.T) {
	before := RequestSnapshot{Method: "GET", URI: "/a", Headers: http.Header{}}
	after := RequestSnapshot{Method: "POST", URI: "/b", Headers: http.Header{}}
	diff := DiffSnapshots(before, after)
	require.Equal(t, "/a", diff.URIChange.Before)
	require.Equal(t, "/b", diff.URIChange.After)
	require.Equal(t, "GET", diff.MethodChange.Before)
	require.Equal(t, "POST", diff.MethodChange.After)
}

func TestDiffSnapshots_DetectsMultiValueHeaderChange(t *testing.T) {
	before := RequestSnapshot{
		Method:  "GET",
		URI:     "/a",
		Headers: http.Header{"Set-Cookie": {"a=1", "b=2"}},
	}
	after := RequestSnapshot{
		Method:  "GET",
		URI:     "/a",
		Headers: http.Header{"Set-Cookie": {"a=1"}},
	}
	diff := DiffSnapshots(before, after)
	require.Contains(t, diff.HeadersChanged, "Set-Cookie")
}

func TestDiffSnapshots_OrderingIsSorted(t *testing.T) {
	before := RequestSnapshot{Headers: http.Header{}}
	after := RequestSnapshot{
		Headers: http.Header{"Z-Last": {"1"}, "A-First": {"1"}, "M-Mid": {"1"}},
	}
	diff := DiffSnapshots(before, after)
	require.Equal(t, []string{"A-First", "M-Mid", "Z-Last"}, diff.HeadersAdded)
}
