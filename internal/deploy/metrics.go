package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// What the account's infrastructure is doing, for the Infra page: requests
// and errors per Worker over the last day, the hourly shape of them, and the
// databases with their size and queries. Read from Cloudflare's GraphQL
// Analytics API, which needs Account › Account Analytics › Read on the token —
// when that is missing the page says so rather than showing zeroes.

// Metrics is one provider account's usage over a window.
type Metrics struct {
	Since string `json:"since"`
	Until string `json:"until"`
	// Totals over the window.
	Requests    int64 `json:"requests"`
	Errors      int64 `json:"errors"`
	Subrequests int64 `json:"subrequests"`
	// Hourly is requests and errors per hour, oldest first.
	Hourly []MetricPoint `json:"hourly,omitempty"`
	// Workers is per-script usage, busiest first.
	Workers []WorkerMetric `json:"workers,omitempty"`
	// Databases are the account's D1 databases.
	Databases []DBMetric `json:"databases,omitempty"`
	// Scripts counts the account's Workers, used or not.
	Scripts int `json:"scripts"`
	// Error is why part of this could not be read, in words the owner can
	// act on. What could be read is still filled in.
	Error string `json:"error,omitempty"`
}

// MetricPoint is one hour.
type MetricPoint struct {
	At       string `json:"at"`
	Requests int64  `json:"requests"`
	Errors   int64  `json:"errors"`
}

// WorkerMetric is one Worker's usage.
type WorkerMetric struct {
	Name        string  `json:"name"`
	Requests    int64   `json:"requests"`
	Errors      int64   `json:"errors"`
	Subrequests int64   `json:"subrequests"`
	CPUP50      float64 `json:"cpu_p50_us"`
	CPUP99      float64 `json:"cpu_p99_us"`
}

// DBMetric is one D1 database.
type DBMetric struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	Tables      int    `json:"tables"`
	ReadQueries int64  `json:"read_queries"`
	WriteQuery  int64  `json:"write_queries"`
	RowsRead    int64  `json:"rows_read"`
	RowsWritten int64  `json:"rows_written"`
}

// MetricsReader is a provider that can report its account's usage.
type MetricsReader interface {
	Metrics(ctx context.Context, window time.Duration) *Metrics
}

// Metrics is the Cloudflare account's usage over window.
func (d *Cloudflare) Metrics(ctx context.Context, window time.Duration) *Metrics {
	if ok, _ := d.Connected(); !ok {
		return nil
	}
	return d.Client.Metrics(ctx, window)
}

const metricsTTL = 2 * time.Minute

var (
	metricsMu    sync.Mutex
	metricsCache = map[string]struct {
		at time.Time
		m  Metrics
	}{}
)

// Metrics reads usage over window, cached briefly per account: analytics lag
// by minutes anyway, and the page refreshes on its own.
func (c *Client) Metrics(ctx context.Context, window time.Duration) *Metrics {
	if c == nil || !c.Creds.OK() {
		return nil
	}
	key := fmt.Sprintf("%s|%s|%s", c.Base, c.Creds.AccountID, window)
	metricsMu.Lock()
	if hit, ok := metricsCache[key]; ok && time.Since(hit.at) < metricsTTL {
		metricsMu.Unlock()
		m := hit.m
		return &m
	}
	metricsMu.Unlock()

	until := time.Now().UTC().Truncate(time.Minute)
	since := until.Add(-window)
	m := &Metrics{Since: since.Format(time.RFC3339), Until: until.Format(time.RFC3339)}
	var errs []string

	var wg sync.WaitGroup
	var dbs []D1
	var dbErr, scriptsErr, workersErr, d1Err error
	var d1Use map[string]DBMetric
	wg.Add(4)
	go func() { defer wg.Done(); workersErr = c.workerMetrics(ctx, since, until, m) }()
	go func() { defer wg.Done(); dbs, dbErr = c.listD1(ctx) }()
	go func() { defer wg.Done(); m.Scripts, scriptsErr = c.countScripts(ctx) }()
	go func() { defer wg.Done(); d1Use, d1Err = c.d1Metrics(ctx, since, until) }()
	wg.Wait()

	if workersErr != nil {
		errs = append(errs, analyticsHint(workersErr))
	}
	if scriptsErr != nil {
		errs = append(errs, "Workers: "+scriptsErr.Error())
	}
	if dbErr != nil {
		errs = append(errs, "D1: "+dbErr.Error())
	}
	for _, d := range dbs {
		dm := DBMetric{ID: d.UUID, Name: d.Name, Bytes: d.FileSize, Tables: d.NumTables}
		if u, ok := d1Use[d.UUID]; ok {
			dm.ReadQueries, dm.WriteQuery, dm.RowsRead, dm.RowsWritten = u.ReadQueries, u.WriteQuery, u.RowsRead, u.RowsWritten
		}
		m.Databases = append(m.Databases, dm)
	}
	if d1Err != nil && workersErr == nil {
		errs = append(errs, "D1 analytics: "+d1Err.Error())
	}
	sort.Slice(m.Databases, func(i, j int) bool { return m.Databases[i].Bytes > m.Databases[j].Bytes })
	m.Error = strings.Join(dedupe(errs), " · ")

	// Only a complete answer is kept, as with the account (account.go).
	if m.Error == "" {
		metricsMu.Lock()
		metricsCache[key] = struct {
			at time.Time
			m  Metrics
		}{time.Now(), *m}
		metricsMu.Unlock()
	}
	return m
}

func analyticsHint(err error) string {
	s := err.Error()
	low := strings.ToLower(s)
	if strings.Contains(low, "not authorized") || strings.Contains(low, "authz") || strings.Contains(low, "permission") ||
		strings.Contains(low, "10000") || strings.Contains(low, "forbidden") {
		return "token chưa có quyền Account › Account Analytics › Read — metric không đọc được"
	}
	return "analytics: " + s
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// graphql runs one Analytics query.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	b, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/graphql", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Creds.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare graphql: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("cloudflare graphql: %s", resp.Status)
	}
	if len(r.Errors) > 0 {
		msgs := make([]string, 0, len(r.Errors))
		for _, e := range r.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("%s", strings.Join(dedupe(msgs), "; "))
	}
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return fmt.Errorf("cloudflare graphql: empty answer (%s)", resp.Status)
	}
	return json.Unmarshal(r.Data, out)
}

const workersQuery = `query($acct: string!, $since: Time!, $until: Time!) {
  viewer { accounts(filter: {accountTag: $acct}) {
    byScript: workersInvocationsAdaptive(limit: 200, filter: {datetime_geq: $since, datetime_leq: $until}, orderBy: [sum_requests_DESC]) {
      sum { requests errors subrequests }
      quantiles { cpuTimeP50 cpuTimeP99 }
      dimensions { scriptName }
    }
    byHour: workersInvocationsAdaptive(limit: 1000, filter: {datetime_geq: $since, datetime_leq: $until}, orderBy: [datetimeHour_ASC]) {
      sum { requests errors }
      dimensions { datetimeHour }
    }
  } }
}`

type wSum struct {
	Requests    int64 `json:"requests"`
	Errors      int64 `json:"errors"`
	Subrequests int64 `json:"subrequests"`
}

func (c *Client) workerMetrics(ctx context.Context, since, until time.Time, m *Metrics) error {
	var out struct {
		Viewer struct {
			Accounts []struct {
				ByScript []struct {
					Sum       wSum `json:"sum"`
					Quantiles struct {
						CPUTimeP50 float64 `json:"cpuTimeP50"`
						CPUTimeP99 float64 `json:"cpuTimeP99"`
					} `json:"quantiles"`
					Dimensions struct {
						ScriptName string `json:"scriptName"`
					} `json:"dimensions"`
				} `json:"byScript"`
				ByHour []struct {
					Sum        wSum `json:"sum"`
					Dimensions struct {
						DatetimeHour string `json:"datetimeHour"`
					} `json:"dimensions"`
				} `json:"byHour"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	err := c.graphql(ctx, workersQuery, map[string]any{
		"acct": c.Creds.AccountID, "since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339),
	}, &out)
	if err != nil {
		return err
	}
	if len(out.Viewer.Accounts) == 0 {
		return nil
	}
	a := out.Viewer.Accounts[0]
	for _, s := range a.ByScript {
		m.Requests += s.Sum.Requests
		m.Errors += s.Sum.Errors
		m.Subrequests += s.Sum.Subrequests
		m.Workers = append(m.Workers, WorkerMetric{
			Name: s.Dimensions.ScriptName, Requests: s.Sum.Requests, Errors: s.Sum.Errors,
			Subrequests: s.Sum.Subrequests, CPUP50: s.Quantiles.CPUTimeP50, CPUP99: s.Quantiles.CPUTimeP99,
		})
	}
	// The hourly series has a point for every hour of the window, zero or
	// not, so a quiet night reads as quiet and not as missing.
	byHour := map[string]MetricPoint{}
	for _, h := range a.ByHour {
		t, err := time.Parse(time.RFC3339, h.Dimensions.DatetimeHour)
		if err != nil {
			continue
		}
		k := t.UTC().Format(time.RFC3339)
		p := byHour[k]
		p.Requests += h.Sum.Requests
		p.Errors += h.Sum.Errors
		byHour[k] = p
	}
	for t := since.Truncate(time.Hour); !t.After(until); t = t.Add(time.Hour) {
		k := t.UTC().Format(time.RFC3339)
		p := byHour[k]
		p.At = k
		m.Hourly = append(m.Hourly, p)
	}
	return nil
}

const d1Query = `query($acct: string!, $since: Date!, $until: Date!) {
  viewer { accounts(filter: {accountTag: $acct}) {
    d1AnalyticsAdaptiveGroups(limit: 200, filter: {date_geq: $since, date_leq: $until}) {
      sum { readQueries writeQueries rowsRead rowsWritten }
      dimensions { databaseId }
    }
  } }
}`

func (c *Client) d1Metrics(ctx context.Context, since, until time.Time) (map[string]DBMetric, error) {
	var out struct {
		Viewer struct {
			Accounts []struct {
				Groups []struct {
					Sum struct {
						ReadQueries  int64 `json:"readQueries"`
						WriteQueries int64 `json:"writeQueries"`
						RowsRead     int64 `json:"rowsRead"`
						RowsWritten  int64 `json:"rowsWritten"`
					} `json:"sum"`
					Dimensions struct {
						DatabaseID string `json:"databaseId"`
					} `json:"dimensions"`
				} `json:"d1AnalyticsAdaptiveGroups"`
			} `json:"accounts"`
		} `json:"viewer"`
	}
	err := c.graphql(ctx, d1Query, map[string]any{
		"acct": c.Creds.AccountID, "since": since.Format("2006-01-02"), "until": until.Format("2006-01-02"),
	}, &out)
	if err != nil {
		return nil, err
	}
	res := map[string]DBMetric{}
	if len(out.Viewer.Accounts) == 0 {
		return res, nil
	}
	for _, g := range out.Viewer.Accounts[0].Groups {
		d := res[g.Dimensions.DatabaseID]
		d.ReadQueries += g.Sum.ReadQueries
		d.WriteQuery += g.Sum.WriteQueries
		d.RowsRead += g.Sum.RowsRead
		d.RowsWritten += g.Sum.RowsWritten
		res[g.Dimensions.DatabaseID] = d
	}
	return res, nil
}

// listD1 lists the account's D1 databases.
func (c *Client) listD1(ctx context.Context) ([]D1, error) {
	var out []D1
	err := c.do(ctx, http.MethodGet, c.acct()+"/d1/database?per_page=100", nil, &out)
	return out, err
}

// countScripts counts the account's Workers.
func (c *Client) countScripts(ctx context.Context) (int, error) {
	var out []struct {
		ID string `json:"id"`
	}
	if err := c.do(ctx, http.MethodGet, c.acct()+"/workers/scripts", nil, &out); err != nil {
		return 0, err
	}
	return len(out), nil
}
