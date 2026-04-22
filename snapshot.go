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

// HeaderChange records a single header's before/after state across a diff.
// Values are already redacted where the header name is in the sensitive set.
type HeaderChange struct {
	Name   string   `json:"name"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// SnapshotDiff describes the difference between two snapshots.
type SnapshotDiff struct {
	HeadersAdded   []HeaderChange `json:"headers_added,omitempty"`
	HeadersRemoved []HeaderChange `json:"headers_removed,omitempty"`
	HeadersChanged []HeaderChange `json:"headers_changed,omitempty"`
	URIChange      *Change        `json:"uri_change,omitempty"`
	MethodChange   *Change        `json:"method_change,omitempty"`
}

// DiffSnapshots computes the SnapshotDiff from before to after. Sensitive
// header values are redacted using DefaultRedactor before being emitted.
func DiffSnapshots(before, after RequestSnapshot) SnapshotDiff {
	var diff SnapshotDiff
	red := DefaultRedactor()

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
			diff.HeadersAdded = append(diff.HeadersAdded, HeaderChange{
				Name:  k,
				After: red.redactValues(k, after.Headers[k]),
			})
		} else if !slices.Equal(before.Headers[k], after.Headers[k]) {
			diff.HeadersChanged = append(diff.HeadersChanged, HeaderChange{
				Name:   k,
				Before: red.redactValues(k, before.Headers[k]),
				After:  red.redactValues(k, after.Headers[k]),
			})
		}
	}
	for k := range beforeKeys {
		if !afterKeys[k] {
			diff.HeadersRemoved = append(diff.HeadersRemoved, HeaderChange{
				Name:   k,
				Before: red.redactValues(k, before.Headers[k]),
			})
		}
	}

	sort.Slice(diff.HeadersAdded, func(i, j int) bool { return diff.HeadersAdded[i].Name < diff.HeadersAdded[j].Name })
	sort.Slice(diff.HeadersRemoved, func(i, j int) bool { return diff.HeadersRemoved[i].Name < diff.HeadersRemoved[j].Name })
	sort.Slice(diff.HeadersChanged, func(i, j int) bool { return diff.HeadersChanged[i].Name < diff.HeadersChanged[j].Name })

	if before.URI != after.URI {
		diff.URIChange = &Change{Before: before.URI, After: after.URI}
	}
	if before.Method != after.Method {
		diff.MethodChange = &Change{Before: before.Method, After: after.Method}
	}
	return diff
}
