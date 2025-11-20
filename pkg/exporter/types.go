package exporter

import "time"

// Auth defines the authentication details for the connection.
type Auth struct {
	Type string `json:"type"`
}

// Connection defines the connection details for the VictoriaMetrics instance.
type Connection struct {
	URL           string `json:"url"`
	APIBasePath   string `json:"api_base_path"`
	TenantID      *int   `json:"tenant_id"` // Use *int for nullability
	IsMultitenant bool   `json:"is_multitenant"`
	FullAPIURL    string `json:"full_api_url"`
	Auth          Auth   `json:"auth"`
	SkipTLSVerify bool   `json:"skip_tls_verify"`
}

// TimeRange defines the start and end times for data export.
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Obfuscation defines the data obfuscation settings.
type Obfuscation struct {
	Enabled           bool     `json:"enabled"`
	ObfuscateInstance bool     `json:"obfuscate_instance"`
	ObfuscateJob      bool     `json:"obfuscate_job"`
	PreserveStructure bool     `json:"preserve_structure"`
	CustomLabels      []string `json:"custom_labels"`
}

// Batching defines the batching strategy for data export.
type Batching struct {
	Enabled            bool   `json:"enabled"`
	Strategy           string `json:"strategy"`
	CustomIntervalSecs int    `json:"custom_interval_secs"`
}

// RequestBody defines the top-level structure for the vmexporter /api/start request.
type RequestBody struct {
	Connection        Connection  `json:"connection"`
	TimeRange         TimeRange   `json:"time_range"`
	Components        []string    `json:"components"`
	Jobs              []string    `json:"jobs"`
	Obfuscation       Obfuscation `json:"obfuscation"`
	StagingDir        string      `json:"staging_dir"`
	MetricStepSeconds int         `json:"metric_step_seconds"`
	Batching          Batching    `json:"batching"`
}
