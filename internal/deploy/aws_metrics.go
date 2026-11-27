package deploy

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	cetypes "github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// AWS usage for the Infra page: requests and 5xx per hour from CloudWatch,
// for bomclaw's CloudFront distributions and the region's App Runner
// services, and the month's spend from Cost Explorer. Cost Explorer bills
// each call ($0.01), so its answer is kept for hours, not minutes.

type awsMetricKey struct{ kind, name string }

// Metrics is the account's usage over window.
func (a *AWS) Metrics(ctx context.Context, window time.Duration) *Metrics {
	if ok, _ := a.Connected(); !ok {
		return nil
	}
	until := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	since := until.Add(-window)
	m := &Metrics{Since: since.Format(time.RFC3339), Until: until.Format(time.RFC3339), Unit: "Resource"}
	var errs []string

	hourly := map[string]*MetricPoint{}
	per := map[awsMetricKey]*WorkerMetric{}
	add := func(k awsMetricKey, project string, ts []time.Time, req, errv []float64) {
		w := per[k]
		if w == nil {
			w = &WorkerMetric{Name: k.name, Kind: k.kind, Project: project}
			per[k] = w
		}
		for i, t := range ts {
			h := t.UTC().Truncate(time.Hour).Format(time.RFC3339)
			p := hourly[h]
			if p == nil {
				p = &MetricPoint{At: h}
				hourly[h] = p
			}
			r := int64(req[i])
			e := int64(0)
			if i < len(errv) {
				e = int64(errv[i])
			}
			p.Requests += r
			p.Errors += e
			w.Requests += r
			w.Errors += e
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := a.cloudFrontMetrics(ctx, since, until, func(id, project string, ts []time.Time, req, errv []float64) {
			mu.Lock()
			defer mu.Unlock()
			add(awsMetricKey{"CloudFront", id}, project, ts, req, errv)
		}); err != nil {
			mu.Lock()
			errs = append(errs, "CloudFront: "+awsHint(err))
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		if err := a.runnerMetrics(ctx, since, until, func(name string, ts []time.Time, req, errv []float64) {
			mu.Lock()
			defer mu.Unlock()
			add(awsMetricKey{"App Runner", name}, name, ts, req, errv)
		}); err != nil {
			mu.Lock()
			errs = append(errs, "App Runner: "+awsHint(err))
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		c, err := a.cost(ctx)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			errs = append(errs, "chi phí: "+awsHint(err))
			return
		}
		m.Cost = c
	}()
	wg.Wait()

	for _, w := range per {
		m.Requests += w.Requests
		m.Errors += w.Errors
		m.Workers = append(m.Workers, *w)
	}
	m.Scripts = len(per)
	sort.Slice(m.Workers, func(i, j int) bool { return m.Workers[i].Requests > m.Workers[j].Requests })
	for t := since; t.Before(until); t = t.Add(time.Hour) {
		k := t.Format(time.RFC3339)
		p := MetricPoint{At: k}
		if h := hourly[k]; h != nil {
			p = *h
		}
		m.Hourly = append(m.Hourly, p)
	}
	sort.Strings(errs)
	m.Error = strings.Join(errs, " · ")
	return m
}

// awsHint turns an access refusal into what to grant.
func awsHint(err error) string {
	s := err.Error()
	if strings.Contains(s, "AccessDenied") || strings.Contains(s, "not authorized") {
		return "credentials thiếu quyền đọc (" + firstLine(s) + ")"
	}
	return firstLine(s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// series runs GetMetricData and returns each query's points by id.
func series(ctx context.Context, c *cloudwatch.Client, since, until time.Time, qs []cwtypes.MetricDataQuery) (map[string]cwtypes.MetricDataResult, error) {
	out := map[string]cwtypes.MetricDataResult{}
	for len(qs) > 0 {
		n := min(len(qs), 500)
		var next *string
		for {
			page, err := c.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{StartTime: &since, EndTime: &until,
				MetricDataQueries: qs[:n], NextToken: next, ScanBy: cwtypes.ScanByTimestampAscending})
			if err != nil {
				return nil, err
			}
			for _, r := range page.MetricDataResults {
				id := aws.ToString(r.Id)
				prev := out[id]
				prev.Id = r.Id
				prev.Timestamps = append(prev.Timestamps, r.Timestamps...)
				prev.Values = append(prev.Values, r.Values...)
				out[id] = prev
			}
			if page.NextToken == nil {
				break
			}
			next = page.NextToken
		}
		qs = qs[n:]
	}
	return out, nil
}

func query(id, ns, metric, stat string, dims map[string]string) cwtypes.MetricDataQuery {
	var d []cwtypes.Dimension
	for k, v := range dims {
		d = append(d, cwtypes.Dimension{Name: aws.String(k), Value: aws.String(v)})
	}
	return cwtypes.MetricDataQuery{Id: aws.String(id), ReturnData: aws.Bool(true), MetricStat: &cwtypes.MetricStat{
		Metric: &cwtypes.Metric{Namespace: aws.String(ns), MetricName: aws.String(metric), Dimensions: d},
		Period: aws.Int32(3600), Stat: aws.String(stat)}}
}

// aligned is b's values at a's timestamps (0 where b has none).
func aligned(a, b cwtypes.MetricDataResult) []float64 {
	at := map[int64]float64{}
	for i, t := range b.Timestamps {
		at[t.Unix()] = b.Values[i]
	}
	out := make([]float64, len(a.Timestamps))
	for i, t := range a.Timestamps {
		out[i] = at[t.Unix()]
	}
	return out
}

func (a *AWS) cloudFrontMetrics(ctx context.Context, since, until time.Time, add func(id, project string, ts []time.Time, req, errv []float64)) error {
	dists, err := a.distributions(ctx, false)
	if err != nil {
		return err
	}
	var qs []cwtypes.MetricDataQuery
	names := map[string]string{}
	for i, d := range dists {
		c := aws.ToString(d.Comment)
		if !strings.HasPrefix(c, "bomclaw:") {
			continue
		}
		dims := map[string]string{"DistributionId": aws.ToString(d.Id), "Region": "Global"}
		qs = append(qs, query(fmt.Sprintf("r%d", i), "AWS/CloudFront", "Requests", "Sum", dims),
			query(fmt.Sprintf("e%d", i), "AWS/CloudFront", "5xxErrorRate", "Average", dims))
		names[fmt.Sprint(i)] = strings.TrimPrefix(c, "bomclaw:")
	}
	if len(qs) == 0 {
		return nil
	}
	cfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		return err
	}
	res, err := series(ctx, cloudwatch.NewFromConfig(cfg), since, until, qs)
	if err != nil {
		return err
	}
	for i, d := range dists {
		k := fmt.Sprint(i)
		if _, ok := names[k]; !ok {
			continue
		}
		r := res["r"+k]
		rate := aligned(r, res["e"+k])
		errs := make([]float64, len(rate))
		for j := range rate {
			errs[j] = r.Values[j] * rate[j] / 100
		}
		add(aws.ToString(d.Id), names[k], r.Timestamps, r.Values, errs)
	}
	return nil
}

func (a *AWS) runnerMetrics(ctx context.Context, since, until time.Time, add func(name string, ts []time.Time, req, errv []float64)) error {
	svcs, err := a.services(ctx, a.Region, false)
	if err != nil {
		return err
	}
	var qs []cwtypes.MetricDataQuery
	for i, s := range svcs {
		arn := aws.ToString(s.ServiceArn)
		dims := map[string]string{"ServiceName": aws.ToString(s.ServiceName), "ServiceID": arn[strings.LastIndex(arn, "/")+1:]}
		qs = append(qs, query(fmt.Sprintf("r%d", i), "AWS/AppRunner", "Requests", "Sum", dims),
			query(fmt.Sprintf("e%d", i), "AWS/AppRunner", "5xxStatusResponses", "Sum", dims))
	}
	if len(qs) == 0 {
		return nil
	}
	cfg, err := a.regional(ctx, a.Region)
	if err != nil {
		return err
	}
	res, err := series(ctx, cloudwatch.NewFromConfig(cfg), since, until, qs)
	if err != nil {
		return err
	}
	for i, s := range svcs {
		r := res[fmt.Sprintf("r%d", i)]
		add(aws.ToString(s.ServiceName), r.Timestamps, r.Values, aligned(r, res[fmt.Sprintf("e%d", i)]))
	}
	return nil
}

const costTTL = 6 * time.Hour

var (
	costMu    sync.Mutex
	costCache = map[string]struct {
		at time.Time
		c  *Cost
	}{}
)

// cost is the month's spend so far, by service.
func (a *AWS) cost(ctx context.Context) (*Cost, error) {
	acct, err := a.accountID(ctx)
	if err != nil {
		return nil, err
	}
	costMu.Lock()
	if hit, ok := costCache[acct]; ok && time.Since(hit.at) < costTTL {
		costMu.Unlock()
		return hit.c, nil
	}
	costMu.Unlock()
	cfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := now.AddDate(0, 0, 1)
	out, err := costexplorer.NewFromConfig(cfg).GetCostAndUsage(ctx, &costexplorer.GetCostAndUsageInput{
		TimePeriod:  &cetypes.DateInterval{Start: aws.String(start.Format("2006-01-02")), End: aws.String(end.Format("2006-01-02"))},
		Granularity: cetypes.GranularityMonthly,
		Metrics:     []string{"UnblendedCost"},
		GroupBy:     []cetypes.GroupDefinition{{Type: cetypes.GroupDefinitionTypeDimension, Key: aws.String("SERVICE")}},
	})
	if err != nil {
		return nil, err
	}
	c := &Cost{Currency: "USD", Period: start.Format("2006-01-02") + " → " + now.Format("2006-01-02")}
	for _, r := range out.ResultsByTime {
		for _, g := range r.Groups {
			mv, ok := g.Metrics["UnblendedCost"]
			if !ok {
				continue
			}
			amt, _ := strconv.ParseFloat(aws.ToString(mv.Amount), 64)
			if u := aws.ToString(mv.Unit); u != "" {
				c.Currency = u
			}
			c.Amount += amt
			if amt >= 0.005 && len(g.Keys) > 0 {
				c.ByService = append(c.ByService, CostService{Name: g.Keys[0], Amount: amt})
			}
		}
	}
	sort.Slice(c.ByService, func(i, j int) bool { return c.ByService[i].Amount > c.ByService[j].Amount })
	costMu.Lock()
	costCache[acct] = struct {
		at time.Time
		c  *Cost
	}{time.Now(), c}
	costMu.Unlock()
	return c, nil
}
