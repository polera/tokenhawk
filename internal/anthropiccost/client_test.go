package anthropiccost

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestFetchPaginatesAndChunksCostReport(t *testing.T) {
	requests := 0
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.Header.Get("x-api-key") != "admin-secret" || r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("missing Anthropic authentication headers: %#v", r.Header)
		}
		if got := r.URL.Query()["group_by[]"]; len(got) != 1 || got[0] != "description" {
			t.Errorf("group_by = %#v", got)
		}
		start, end, page := r.URL.Query().Get("starting_at"), r.URL.Query().Get("ending_at"), r.URL.Query().Get("page")
		body := ""
		switch {
		case start == "2026-01-01T00:00:00Z" && end == "2026-02-01T00:00:00Z" && page == "":
			body = `{"data":[{"starting_at":"2026-01-01T00:00:00Z","results":[{"amount":"123.456789","currency":"USD","model":"claude-opus-5"},{"amount":"0.000001","currency":"USD","model":"claude-opus-5"}]}],"has_more":true,"next_page":"next"}`
		case start == "2026-01-01T00:00:00Z" && end == "2026-02-01T00:00:00Z" && page == "next":
			body = `{"data":[{"starting_at":"2026-01-02T00:00:00Z","results":[{"amount":"50","currency":"USD","model":null}]},{"starting_at":"2026-01-03T00:00:00Z","results":[]}],"has_more":false,"next_page":null}`
		case start == "2026-02-01T00:00:00Z" && end == "2026-02-03T00:00:00Z" && page == "":
			body = `{"data":[{"starting_at":"2026-02-01T00:00:00Z","results":[{"amount":"1.5","currency":"USD","model":"claude-haiku-4-5"}]}],"has_more":false,"next_page":null}`
		default:
			t.Errorf("unexpected request: %s", r.URL.String())
			return &http.Response{StatusCode: http.StatusBadRequest, Status: "400 Bad Request", Body: io.NopCloser(strings.NewReader("unexpected")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})

	client := New("admin-secret")
	client.HTTP = &http.Client{Transport: transport}
	ledger, err := client.Fetch(context.Background(),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.FixedZone("west", -5*60*60)),
		time.Date(2026, 2, 3, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || len(ledger.CoveredDays) != 4 || len(ledger.Costs) != 3 {
		t.Fatalf("requests=%d coverage=%d costs=%#v", requests, len(ledger.CoveredDays), ledger.Costs)
	}
	amounts := map[string]int64{}
	for _, cost := range ledger.Costs {
		amounts[cost.Day.Format("2006-01-02")+"\x00"+cost.Model] = cost.AmountNanoUSD
	}
	if got := amounts["2026-01-01\x00claude-opus-5"]; got != 1_234_567_900 {
		t.Fatalf("fractional cents were not aggregated exactly: %d", got)
	}
	if got := amounts["2026-01-02\x00"]; got != 500_000_000 {
		t.Fatalf("model-free service cost = %d", got)
	}
	if got := amounts["2026-02-01\x00claude-haiku-4-5"]; got != 15_000_000 {
		t.Fatalf("Haiku cost = %d", got)
	}
}

func TestCentsToNanoUSDRoundsOnlyBeyondStoredPrecision(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
	}{
		{"1", 10_000_000},
		{"1.2345678", 12_345_678},
		{"1.23456785", 12_345_679},
		{"-0.5", -5_000_000},
	} {
		got, err := centsToNanoUSD(test.value)
		if err != nil || got != test.want {
			t.Errorf("centsToNanoUSD(%q) = (%d, %v), want %d", test.value, got, err, test.want)
		}
	}
}
