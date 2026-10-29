package tests

import (
	"context"
	"fmt"

	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/promquery"
)

const (
	// sliRateInterval replaces $__rate_interval. It must span several scrape intervals
	// (20s in the smoke values) so short runs still produce a rate.
	sliRateInterval = "5m"

	// sliPercentMax bounds percentage panels. Short bursts above 100% are possible, so
	// the bound only catches a panel that lost its normalization.
	sliPercentMax = 200

	sliJobInsert  = ".*vminsert.*"
	sliJobSelect  = ".*vmselect.*"
	sliJobStorage = ".*vmstorage.*"
	sliJobCluster = ".*vm(insert|select|storage).*"
	sliJobSingle  = ".*vmsingle.*"
	sliJobAgent   = ".*vmagent.*"
)

// SLI is service level indicator. Max is an upper sanity
// bound; 0 leaves the panel unbounded and only asserts that it returns data.
type SLI struct {
	Name  string
	Query string
	Max   float64
}

// CheckSLI evaluates every SLI query. The checker fails when a query returns
// no series.
func CheckSLI(check func(purpose, query string) ScannedMetric, indicators []SLI) {
	for _, indicator := range indicators {
		metric := check("SLI: "+indicator.Name, indicator.Query)
		metric.GreaterOrEqual(0)

		if indicator.Max > 0 {
			metric.Less(indicator.Max)
		}
	}
}

// WaitForSLI blocks until every SLI query returns data, i.e. until overwatch
// scraped the component often enough to fill the rate() windows. Short-lived suites
// must call it before CheckSLI; load suites already wait for k6 metrics.
func WaitForSLI(ctx context.Context, t terratesting.TestingT, overwatch promquery.PrometheusClient, indicators []SLI) {
	require.Eventually(t, func() bool {
		for _, indicator := range indicators {
			if _, _, err := overwatch.VectorScan(ctx, indicator.Query); err != nil {
				return false
			}
		}

		return true
	}, consts.PollingTimeout, consts.PollingInterval, "SLI queries returned no data")
}

func sliSelector(namespace, job string) string {
	return fmt.Sprintf(`job=~%q, namespace=%q`, job, namespace)
}

// sliErrorRate is the RED error panel shared by all three dashboards.
func sliErrorRate(selector string) string {
	return fmt.Sprintf(`100 * (sum(rate(vm_http_request_errors_total{%[1]s}[%[2]s])) or vector(0)) / clamp_min(sum(rate(vm_http_requests_total{%[1]s}[%[2]s])), 1)`,
		selector, sliRateInterval)
}

// sliCPUUtilization and sliMemoryUtilization are the USE panels shared by all three dashboards.
func sliCPUUtilization(selector string) string {
	return fmt.Sprintf(`100 * max(rate(process_cpu_seconds_total{%[1]s}[%[2]s]) / process_cpu_cores_available{%[1]s})`,
		selector, sliRateInterval)
}

func sliMemoryUtilization(selector string) string {
	return fmt.Sprintf(`100 * max(max_over_time(process_resident_memory_bytes{%[1]s}[%[2]s]) / vm_available_memory_bytes{%[1]s})`,
		selector, sliRateInterval)
}

// sliSlowInsertRatio is the ingestion duration panel shared by cluster and single-node.
func sliSlowInsertRatio(selector string) string {
	return fmt.Sprintf(`sum(100 * ((rate(vm_slow_row_inserts_total{%[1]s}[%[2]s]) or 0) / clamp_min(rate(vm_rows_added_to_storage_total{%[1]s}[%[2]s]), 1)))`,
		selector, sliRateInterval)
}

// VMClusterSLIPanels returns the Stats row of the VMCluster SLI dashboard.
func VMClusterSLIPanels(namespace string) []SLI {
	insert := sliSelector(namespace, sliJobInsert)
	query := sliSelector(namespace, sliJobSelect)
	storage := sliSelector(namespace, sliJobStorage)
	cluster := sliSelector(namespace, sliJobCluster)

	return []SLI{
		{
			Name:  "vmcluster ingestion request rate",
			Query: fmt.Sprintf(`sum(rate(vm_rows_inserted_total{%s}[%s]))`, insert, sliRateInterval),
		},
		{
			Name:  "vmcluster ingestion error rate",
			Query: sliErrorRate(insert),
			Max:   sliPercentMax,
		},
		{
			// Summed per vmstorage, so the value is unbounded by design.
			Name:  "vmcluster slow insert ratio",
			Query: sliSlowInsertRatio(cluster),
		},
		{
			Name:  "vmcluster ingestion concurrency saturation",
			Query: fmt.Sprintf(`100 * max(vm_concurrent_insert_current{%[1]s}) / clamp_min(min(vm_concurrent_insert_capacity{%[1]s}), 1)`, insert),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmcluster query request rate",
			Query: fmt.Sprintf(`sum(rate(vm_http_requests_total{%s}[%s]))`, query, sliRateInterval),
		},
		{
			Name:  "vmcluster query error rate",
			Query: sliErrorRate(query),
			Max:   sliPercentMax,
		},
		{
			Name: "vmcluster slow query ratio",
			Query: fmt.Sprintf(`clamp_min(100 * ((sum(rate(vm_slow_queries_total{%[1]s}[%[2]s])) or vector(0)) / clamp_min(sum(rate(vm_http_requests_total{%[1]s}[%[2]s])), 1)), 0)`,
				query, sliRateInterval),
			Max: sliPercentMax,
		},
		{
			Name:  "vmcluster query concurrency saturation",
			Query: fmt.Sprintf(`100 * max(vm_concurrent_select_current{%[1]s}) / clamp_min(min(vm_concurrent_select_capacity{%[1]s}), 1)`, query),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmcluster CPU utilization",
			Query: sliCPUUtilization(cluster),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmcluster memory utilization",
			Query: sliMemoryUtilization(cluster),
			Max:   sliPercentMax,
		},
		{
			// The ETA needs a day of data to produce a value, hence the fallback. The
			// companion panel below keeps the check from being vacuous.
			Name: "vmcluster storage full ETA",
			Query: fmt.Sprintf(`min((vm_free_disk_space_bytes{%[1]s} - vm_free_disk_space_limit_bytes{%[1]s}) / ignoring(path) ( `+
				`(rate(vm_rows_added_to_storage_total{%[1]s}[1d]) - sum(rate(vm_deduplicated_samples_total{%[1]s}[1d])) without(type)) * `+
				`(sum(vm_data_size_bytes{%[1]s, type!~"indexdb.*"}) without(type) / sum(vm_rows{%[1]s, type!~"indexdb.*"}) without(type)) + `+
				`rate(vm_new_timeseries_created_total{%[1]s}[1d]) * `+
				`(sum(vm_data_size_bytes{%[1]s, type="indexdb/file"}) without(type) / sum(vm_rows{%[1]s, type="indexdb/file"}) without(type)) ) > 0) or vector(0)`,
				storage),
		},
		{
			// count() of a missing metric returns no series, so this fails as soon as any
			// input of the ETA panel is renamed or dropped.
			Name: "vmcluster storage full ETA inputs",
			Query: fmt.Sprintf(`count(vm_free_disk_space_bytes{%[1]s}) + count(vm_free_disk_space_limit_bytes{%[1]s}) + `+
				`count(vm_new_timeseries_created_total{%[1]s}) + count(vm_deduplicated_samples_total{%[1]s}) + `+
				`count(vm_data_size_bytes{%[1]s}) + count(vm_rows{%[1]s})`, storage),
		},
		{
			Name:  "vmcluster storage connection saturation",
			Query: fmt.Sprintf(`max(rate(vm_rpc_send_duration_seconds_total{%s}[%s])) * 100`, cluster, sliRateInterval),
		},
	}
}

// VMSingleSLI returns the SLIs for the VMSingle.
func VMSingleSLI(namespace string) []SLI {
	single := sliSelector(namespace, sliJobSingle)
	write := single + `, path=~".*write"`
	read := single + `, path!~".*(/write|/metrics)"`

	return []SLI{
		{
			Name:  "vmsingle ingestion request rate",
			Query: fmt.Sprintf(`sum(rate(vm_rows_inserted_total{%s}[%s]))`, single, sliRateInterval),
		},
		{
			Name:  "vmsingle ingestion error rate",
			Query: sliErrorRate(write),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmsingle slow insert ratio",
			Query: sliSlowInsertRatio(single),
		},
		{
			Name:  "vmsingle query request rate",
			Query: fmt.Sprintf(`sum(rate(vm_http_requests_total{%s}[%s]))`, read, sliRateInterval),
		},
		{
			Name:  "vmsingle query error rate",
			Query: sliErrorRate(read),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmsingle query request duration",
			Query: fmt.Sprintf(`max(vm_request_duration_seconds{%s, quantile="0.99"})`, read),
		},
		{
			Name:  "vmsingle CPU utilization",
			Query: sliCPUUtilization(single),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmsingle memory utilization",
			Query: sliMemoryUtilization(single),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmsingle concurrent insert saturation",
			Query: fmt.Sprintf(`100 * sum(vm_concurrent_insert_current{%[1]s}) / clamp_min(sum(vm_concurrent_insert_capacity{%[1]s}), 1)`, single),
			Max:   sliPercentMax,
		},
	}
}

// VMAgentSLI returns SLIs for the VMAgent.
func VMAgentSLI(namespace string) []SLI {
	agent := sliSelector(namespace, sliJobAgent)

	return []SLI{
		{
			Name:  "vmagent push rate",
			Query: fmt.Sprintf(`sum(rate(vmagent_remotewrite_requests_total{%s}[%s]))`, agent, sliRateInterval),
		},
		{
			Name: "vmagent push error rate",
			Query: fmt.Sprintf(`100 * (((sum(rate(vmagent_remotewrite_requests_total{%[1]s, status_code!~"2.."}[%[2]s])) + `+
				`sum(rate(vmagent_remotewrite_retries_count_total{%[1]s}[%[2]s]))) or vector(0)) / `+
				`clamp_min(sum(rate(vmagent_remotewrite_requests_total{%[1]s}[%[2]s])), 1))`, agent, sliRateInterval),
			Max: sliPercentMax,
		},
		{
			Name: "vmagent push duration",
			Query: fmt.Sprintf(`sum(rate(vmagent_remotewrite_send_duration_seconds_total{%[1]s}[%[2]s])) / `+
				`clamp_min(sum(rate(vmagent_remotewrite_requests_total{%[1]s}[%[2]s])), 1)`, agent, sliRateInterval),
		},
		{
			Name:  "vmagent persistent queue size",
			Query: fmt.Sprintf(`sum(vmagent_remotewrite_pending_data_bytes{%s})`, agent),
		},
		{
			// vmagent may have no scrape targets in a test namespace.
			Name: "vmagent scrape duration p99",
			Query: fmt.Sprintf(`histogram_quantile(0.99, sum(rate(vm_promscrape_scrape_duration_seconds_bucket{%s}[%s])) by (vmrange)) or vector(0)`,
				agent, sliRateInterval),
		},
		{
			Name:  "vmagent CPU utilization",
			Query: sliCPUUtilization(agent),
			Max:   sliPercentMax,
		},
		{
			Name:  "vmagent memory utilization",
			Query: sliMemoryUtilization(agent),
			Max:   sliPercentMax,
		},
		{
			// Only exported once a row is actually dropped.
			Name:  "vmagent rows ignored rate",
			Query: fmt.Sprintf(`sum(rate(vm_rows_ignored_total{%s}[%s])) or vector(0)`, agent, sliRateInterval),
		},
		{
			Name:  "vmagent concurrent insert saturation",
			Query: fmt.Sprintf(`100 * max(vm_concurrent_insert_current{%[1]s}) / max(vm_concurrent_insert_capacity{%[1]s})`, agent),
			Max:   sliPercentMax,
		},
		{
			Name: "vmagent remote write connection saturation",
			Query: fmt.Sprintf(`100 * max(rate(vmagent_remotewrite_send_duration_seconds_total{%[1]s}[%[2]s]) / vmagent_remotewrite_queues{%[1]s})`,
				agent, sliRateInterval),
			Max: sliPercentMax,
		},
	}
}
