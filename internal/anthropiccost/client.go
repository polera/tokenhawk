package anthropiccost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	Source         = "anthropic_admin_cost"
	maxChunkDays   = 31
)

type Client struct {
	AdminKey  string
	BaseURL   string
	HTTP      *http.Client
	UserAgent string
}

type Ledger struct {
	Costs       []core.ReportedCost
	CoveredDays []time.Time
}

type costResponse struct {
	Data []struct {
		StartingAt time.Time `json:"starting_at"`
		Results    []struct {
			Amount   string  `json:"amount"`
			Currency string  `json:"currency"`
			Model    *string `json:"model"`
		} `json:"results"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	Next    string `json:"next_page"`
}

func New(adminKey string) *Client {
	return &Client{
		AdminKey:  adminKey,
		BaseURL:   defaultBaseURL,
		HTTP:      &http.Client{Timeout: 20 * time.Second},
		UserAgent: "Tokenhawk",
	}
}

// Fetch returns authoritative cost rows and explicit coverage for [start, end).
// Anthropic accepts at most 31 daily buckets per request, so longer lookbacks
// are split without exposing that API constraint to callers.
func (c *Client) Fetch(ctx context.Context, start, end time.Time) (Ledger, error) {
	start, end = utcDay(start), utcDay(end)
	if !start.Before(end) {
		return Ledger{}, fmt.Errorf("anthropic cost range must contain at least one UTC day")
	}
	amounts := map[string]int64{}
	covered := map[string]time.Time{}
	for chunkStart := start; chunkStart.Before(end); {
		chunkEnd := chunkStart.AddDate(0, 0, maxChunkDays)
		if chunkEnd.After(end) {
			chunkEnd = end
		}
		if err := c.fetchChunk(ctx, chunkStart, chunkEnd, amounts, covered); err != nil {
			return Ledger{}, err
		}
		chunkStart = chunkEnd
	}
	costs := make([]core.ReportedCost, 0, len(amounts))
	for key, amount := range amounts {
		parts := strings.SplitN(key, "\x00", 2)
		day, err := time.Parse("2006-01-02", parts[0])
		if err != nil {
			return Ledger{}, err
		}
		costs = append(costs, core.ReportedCost{
			Provider: core.Claude, Day: day, Model: parts[1],
			AmountNanoUSD: amount, Source: Source,
		})
	}
	sort.Slice(costs, func(i, j int) bool {
		if !costs[i].Day.Equal(costs[j].Day) {
			return costs[i].Day.Before(costs[j].Day)
		}
		return costs[i].Model < costs[j].Model
	})
	coveredDays := make([]time.Time, 0, len(covered))
	for _, day := range covered {
		coveredDays = append(coveredDays, day)
	}
	sort.Slice(coveredDays, func(i, j int) bool { return coveredDays[i].Before(coveredDays[j]) })
	return Ledger{Costs: costs, CoveredDays: coveredDays}, nil
}

func (c *Client) fetchChunk(ctx context.Context, start, end time.Time, amounts map[string]int64, covered map[string]time.Time) error {
	page := ""
	for {
		u, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/v1/organizations/cost_report")
		if err != nil {
			return err
		}
		q := u.Query()
		q.Set("starting_at", start.Format(time.RFC3339))
		q.Set("ending_at", end.Format(time.RFC3339))
		q.Set("bucket_width", "1d")
		q.Add("group_by[]", "description")
		q.Set("limit", strconv.Itoa(maxChunkDays))
		if page != "" {
			q.Set("page", page)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("x-api-key", c.AdminKey)
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return fmt.Errorf("anthropic cost report: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("anthropic cost report: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("anthropic cost report: %w", closeErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			message := strings.TrimSpace(string(body))
			if len(message) > 500 {
				message = message[:500]
			}
			return fmt.Errorf("anthropic cost report returned %s: %s", resp.Status, message)
		}
		var result costResponse
		if err = json.Unmarshal(body, &result); err != nil {
			return fmt.Errorf("anthropic cost report response: %w", err)
		}
		for _, bucket := range result.Data {
			bucketDay := utcDay(bucket.StartingAt)
			day := bucketDay.Format("2006-01-02")
			covered[day] = bucketDay
			for _, row := range bucket.Results {
				if row.Currency != "" && row.Currency != "USD" {
					return fmt.Errorf("anthropic cost report returned unsupported currency %q", row.Currency)
				}
				amount, err := centsToNanoUSD(row.Amount)
				if err != nil {
					return fmt.Errorf("anthropic cost report amount %q: %w", row.Amount, err)
				}
				model := ""
				if row.Model != nil {
					model = *row.Model
				}
				key := day + "\x00" + model
				if amount > 0 && amounts[key] > math.MaxInt64-amount {
					return fmt.Errorf("anthropic cost report amount overflow")
				}
				if amount < 0 && amounts[key] < math.MinInt64-amount {
					return fmt.Errorf("anthropic cost report amount overflow")
				}
				amounts[key] += amount
			}
		}
		if !result.HasMore {
			return nil
		}
		if result.Next == "" || result.Next == page {
			return fmt.Errorf("anthropic cost report returned an invalid pagination cursor")
		}
		page = result.Next
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// centsToNanoUSD converts a decimal amount expressed in cents. Seven decimal
// places in cents map exactly to nanodollars; any finer precision is rounded.
func centsToNanoUSD(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty decimal")
	}
	sign := int64(1)
	if value[0] == '-' || value[0] == '+' {
		if value[0] == '-' {
			sign = -1
		}
		value = value[1:]
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("invalid decimal")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 || whole > math.MaxInt64/10_000_000 {
		return 0, fmt.Errorf("invalid decimal")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" {
			return 0, fmt.Errorf("invalid decimal")
		}
	}
	for _, r := range fraction {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid decimal")
		}
	}
	roundUp := len(fraction) > 7 && fraction[7] >= '5'
	if len(fraction) > 7 {
		fraction = fraction[:7]
	}
	fraction += strings.Repeat("0", 7-len(fraction))
	frac := int64(0)
	if fraction != "" {
		frac, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid decimal")
		}
	}
	nano := whole*10_000_000 + frac
	if roundUp {
		if nano == math.MaxInt64 {
			return 0, fmt.Errorf("decimal overflow")
		}
		nano++
	}
	return sign * nano, nil
}
