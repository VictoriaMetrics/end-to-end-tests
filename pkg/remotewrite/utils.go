package remotewrite

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/prompb"
	"github.com/klauspost/compress/snappy"
)

// remoteWriteRetryDelay is the pause between failed RemoteWrite attempts.
const remoteWriteRetryDelay = 2 * time.Second

// GenTimeSeries generates a slice of Prometheus time series with generated labels and sample values.
// The namePrefix is used to generate the metric name, and size determines the number of series.
// All series will have the same sample value and current timestamp.
func GenTimeSeries(namePrefix string, size int, value float64) []prompb.TimeSeries {
	ts := []prompb.TimeSeries{}
	for i := 0; i < size; i++ {
		ts = append(ts, prompb.TimeSeries{
			Labels: []prompb.Label{
				{Name: "__name__", Value: fmt.Sprintf(`%s_%d`, namePrefix, i)},
				{Name: "foo", Value: fmt.Sprintf("fooVal_%d", i)},
				{Name: "bar", Value: fmt.Sprintf("barVal_%d", i)},
				{Name: "baz", Value: fmt.Sprintf("bazVal_%d", i)},
			},
			Samples: []prompb.Sample{
				{Value: value, Timestamp: time.Now().UnixMilli()},
			},
		})
	}
	return ts
}

// GenPayload marshals the time series into a WriteRequest protobuf and snappy encodes it.
// This matches the format expected by the Prometheus remote write API.
func GenPayload(timeseries []prompb.TimeSeries) []byte {
	r := &prompb.WriteRequest{Timeseries: timeseries}
	payload := r.MarshalProtobuf(nil)
	return snappy.Encode(nil, payload)
}

// RemoteWrite sends the time series to the specified remote write URL using the provided HTTP client.
// It constructs the payload using GenPayload and sets the appropriate headers.
func RemoteWrite(ctx context.Context, c *http.Client, ts []prompb.TimeSeries, url string) error {
	payload := GenPayload(ts)
	var lastErr error

	for attempt := 1; attempt <= 5; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(remoteWriteRetryDelay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("failed to build remote write request: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("User-Agent", "aUserAgent")
		req.Header.Set("Content-Encoding", "snappy")
		req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

		resp, err := c.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			return nil
		}
		lastErr = fmt.Errorf("remote write returned %s", resp.Status)
	}
	return lastErr
}
