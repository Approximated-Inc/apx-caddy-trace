package apxtrace

import (
	"net/http"
	"slices"
	"sort"
)

// RequestSnapshot captures the parts of a request we diff over time
// to detect edge-rule mutations.
type RequestSnapshot struct {
	Method  string
	URI     string
	Headers http.Header
}

// Snapshot captures the current state of the request.
func Snapshot(r *http.Request) RequestSnapshot {
	h := make(http.Header, len(r.Header))
	for k, vs := range r.Header {
		cp := make([]string, len(vs))
		copy(cp, vs)
		h[k] = cp
	}
	return RequestSnapshot{
		Method:  r.Method,
		URI:     r.URL.RequestURI(),
		Headers: h,
	}
}

// Change records a before/after value pair.
type Change struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// SnapshotDiff describes the difference between two snapshots.
type SnapshotDiff struct {
	HeadersAdded   []string `json:"headers_added,omitempty"`
	HeadersRemoved []string `json:"headers_removed,omitempty"`
	HeadersChanged []string `json:"headers_changed,omitempty"`
	URIChange      *Change  `json:"uri_change,omitempty"`
	MethodChange   *Change  `json:"method_change,omitempty"`
}

// DiffSnapshots computes the SnapshotDiff from before to after.
func DiffSnapshots(before, after RequestSnapshot) SnapshotDiff {
	var diff SnapshotDiff

	beforeKeys := map[string]bool{}
	for k := range before.Headers {
		beforeKeys[k] = true
	}
	afterKeys := map[string]bool{}
	for k := range after.Headers {
		afterKeys[k] = true
	}

	for k := range afterKeys {
		if !beforeKeys[k] {
			diff.HeadersAdded = append(diff.HeadersAdded, k)
		} else if !slices.Equal(before.Headers[k], after.Headers[k]) {
			diff.HeadersChanged = append(diff.HeadersChanged, k)
		}
	}
	for k := range beforeKeys {
		if !afterKeys[k] {
			diff.HeadersRemoved = append(diff.HeadersRemoved, k)
		}
	}

	if before.URI != after.URI {
		diff.URIChange = &Change{Before: before.URI, After: after.URI}
	}
	if before.Method != after.Method {
		diff.MethodChange = &Change{Before: before.Method, After: after.Method}
	}

	sort.Strings(diff.HeadersAdded)
	sort.Strings(diff.HeadersRemoved)
	sort.Strings(diff.HeadersChanged)
	return diff
}
