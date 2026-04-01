package remotewrite

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/prompb"
	"github.com/klauspost/compress/snappy"
)

// GenTimeSeries generates a slice of Prometheus time series with generated labels and sample values.
// The name_prefix is used to generate the metric name, and size determines the number of series.
// All series will have the same sample value and current timestamp.
func GenTimeSeries(name_prefix string, size int, value float64) []prompb.TimeSeries {
	ts := []prompb.TimeSeries{}
	for i := 0; i < size; i++ {
		ts = append(ts, prompb.TimeSeries{
			Labels: []prompb.Label{
				{Name: "__name__", Value: fmt.Sprintf(`%s_%d`, name_prefix, i)},
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
	r := &prompb.WriteRequest{}
	r.Timeseries = timeseries
	payload := r.MarshalProtobuf(nil)
	return snappy.Encode(nil, payload)
}

// RemoteWrite sends the time series to the specified remote write URL using the provided HTTP client.
// It constructs the payload using GenPayload and sets the appropriate headers.
func RemoteWrite(c *http.Client, ts []prompb.TimeSeries, url string) error {
	payload := GenPayload(ts)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "aUserAgent")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(payload)))

	resp, err := c.Do(req)
	if err != nil {
		log.Println("http: do: ", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 204 {
		log.Println("http: do: ", resp.Status)
	}
	return nil
}
