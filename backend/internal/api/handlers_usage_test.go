package api_test

import (
	"net/http"
	"testing"

	"mcphub/internal/api"
	"mcphub/internal/usage"
)

func TestUsageIsReported(t *testing.T) {
	counts := usage.New()
	counts.Searched("files", "read")
	counts.Called("files", "read", false)
	counts.Called("files", "read", true)
	h := start(t, func(o *api.Options) { o.Usage = counts })

	var got usage.Snapshot
	decode(t, h.get(t, "/api/gateway/usage"), http.StatusOK, &got)

	if len(got.Entries) != 1 {
		t.Fatalf("entries = %+v, want the one tool", got.Entries)
	}
	e := got.Entries[0]
	if e.Server != "files" || e.Tool != "read" || e.Searched != 1 || e.Called != 2 || e.Failed != 1 {
		t.Errorf("entry = %+v, want files/read searched 1, called 2, failed 1", e)
	}
	if got.Since.IsZero() {
		t.Error("the snapshot does not say when counting began")
	}
}

// A gateway built without a counter has nothing to report, and says so
// with an empty answer rather than an error: the page asking is the tools
// page, which has plenty else to show.
func TestUsageWithoutACounterIsEmpty(t *testing.T) {
	h := start(t, nil)

	var got struct {
		Entries []usage.Entry `json:"entries"`
	}
	decode(t, h.get(t, "/api/gateway/usage"), http.StatusOK, &got)
	if got.Entries == nil || len(got.Entries) != 0 {
		t.Errorf("entries = %v, want an empty list", got.Entries)
	}
}

func TestUsageCanBeReset(t *testing.T) {
	counts := usage.New()
	counts.Called("files", "read", false)
	h := start(t, func(o *api.Options) { o.Usage = counts })

	decode(t, h.do(t, http.MethodDelete, "/api/gateway/usage", nil), http.StatusNoContent, nil)

	if got := counts.Snapshot().Entries; len(got) != 0 {
		t.Errorf("entries after reset = %+v, want none", got)
	}
}

func TestResettingUsageWithoutACounterFails(t *testing.T) {
	h := start(t, nil)
	resp := h.do(t, http.MethodDelete, "/api/gateway/usage", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
