# VictoriaMetrics End-to-End Tests

End-to-end test suite for VictoriaMetrics deployments on Kubernetes. Tests run against real clusters (kind locally, GKE in CI) using the [VictoriaMetrics Operator](https://github.com/VictoriaMetrics/operator).

Main focus of the tests is to simulate topoliogies of real customer deployments, using similar approaches (helm / operator) and published binaries only.

---

## Test Suites

### Functional (`tests/functional_test/`)

Validates correctness of VMSingle and VMCluster deployments:

- Data isolation between tenants
- Ingestion protocols: InfluxDB, Datadog, OpenTelemetry
- Relabeling and streaming aggregation
- Enterprise features: downsampling, retention filters
- Alert rules and recording rules

Tests are tagged with Ginkgo labels (`vmcluster`, `vmsingle`, `enterprise`, `kind`) and a unique `id=<UUID>` for traceability.

### Load (`tests/load_test/`)

Performance and scalability tests using [k6](https://k6.io/) via the k6 Operator:

- High-throughput insert/query under sustained load (50 VUs, 10 minutes)
- Behavior under rolling replica scaling
- Request load balancer cycling

Verifies k6 metrics: rows inserted, request counts, error rates, p95 latency.

### Chaos (`tests/chaos_test/`)

Resilience tests using [Chaos Mesh](https://chaos-mesh.org/):

- Pod restarts and failures
- CPU, memory, and I/O resource stress
- Network failures: packet loss, corruption, delays
- HTTP chaos: response aborts, request delays

Each scenario verifies that expected alerts fire and data integrity is maintained.

### Distributed (`tests/distributed_test/`)

Validates multi-region/multi-zone deployments using the `victoria-metrics-distributed` Helm chart. Tests global and per-zone endpoint behavior.

---

## Frameworks

### Ginkgo

Tests use [Ginkgo v2](https://onsi.github.io/ginkgo/) as the BDD test framework.

**Structure:** `Describe` → `Context` → `It`, with `BeforeEach`/`AfterEach` hooks. Nested `Describe` blocks group related scenarios.

**Labels:** Every `Describe` carries a suite label (`vmcluster`, `vmsingle`, `kind`, etc.) and every `It` carries a unique `id=<UUID>` label for reports (see below). Filter at runtime with `--label-filter`.

**Parameterized tests:** Use `DescribeTable` + `Entry` for scenario variants (load test scenarios, chaos scenarios). Each `Entry` gets its own `id=` label.

**Parallel safety:** `SynchronizedBeforeSuite` runs cluster setup once on process 1, then per-process namespace setup on all processes. Each test gets an isolated namespace via `tests.RandomNamespace()`. Goroutines inside tests must `defer GinkgoRecover()`.

**Pending tests:** Prefix with `P` (`PDescribe`, `PDescribeTable`) to skip without deleting.

**Focus on test:** Prefix with `F` (`FDescribeTable`, `FEntry`) to run just these tests.

**Steps:** Use `By("description")` to annotate progress within a test — these appear as named steps in Allure reports.

```go
var _ = Describe("VMCluster test", Label("vmcluster"), func() {
    BeforeEach(func(ctx context.Context) { /* per-test setup */ })
    AfterEach(func(ctx context.Context) {
        gather.VMAfterAll(ctx, t, kubeOpts, namespace)  // collect diagnostics on failure
    })

    It("should isolate tenant data", Label("id=66618081-b150-4b48-8180-ae1f53512117"), func(ctx context.Context) {
        By("Inserting data into tenant 0")
        // ...
        By("Verifying tenant 0 cannot see tenant 1 data")
        // ...
    })
})

// Parameterized load scenarios
DescribeTable("prw2 load test",
    runLoadScenario,
    Entry("baseline", Label("id=a1b2c3d4-..."), LoadScenario{ScenarioName: "baseline"}),
    Entry("with VMInsert cycling", Label("id=6bbeb19c-..."), LoadScenario{
        ScenarioName:   "vminsert-cycling",
        BackgroundFunc: vmInsertCyclingBackgroundFunc,
    }),
)
```

---

### Gomega / require

Assertions use the standard `testify/require` package (not Gomega matchers directly), with one exception: `gomega.Expect` appears in internal Allure attachment helpers.

Common patterns:

```go
require.NoError(t, err)
require.Equal(t, value, model.SampleValue(1))
require.EqualError(t, err, consts.ErrNoDataReturned)
require.Contains(t, labels, model.LabelName("cluster"))
require.Greater(t, float64(value), 0.0, "query: %s", query)
```

Load tests wrap metric assertions in a helper for better error messages:

```go
checkMetric("rows inserted", fmt.Sprintf(`sum(vm_rows_inserted_total{namespace="%s"})`, namespace)).Greater(0)
```

That would create a message:
```
Error:      	"0" is not greater than "0"
Test:       	
Messages:   	rows inserted
            	query: sum(vm_rows_inserted_total{namespace="foobar"})

```

---

### Allure

Test results are converted to Allure format and uploaded to GCS for HTML report generation.

**How it works:**
- `ReportAfterSuite` hook in `pkg/tests/report.go` converts the Ginkgo report via `allure.FromGinkgoReport(report)`
- Output directory: `-report` flag (default `/tmp/allure-results`), overridable via `ALLURE_RESULTS_PATH` env var
- `By()` blocks inside tests become named Allure steps
- Ginkgo states map to Allure statuses: passed → passed, failed → failed, panicked → broken, skipped → skipped

**Attachments:** Attach arbitrary data to the current test's Allure result:

```go
allure.AddAttachment("query response", allure.MimeTypeJSON, responseBytes)
```

Usually tests collect two artifacts on failure:
* VMGather snapshot of the namespace
* crust-gather archive - this is a snapshot of all cluster manifests, including pod logs, generated configuration and so on.

**Environment metadata:** The suite writes `environment.properties` alongside results (operator version, VM versions, k8s distro) so the Allure report shows the exact build under test.

In CI, results from all parallel suite runs are merged and published as a single HTML report via Buildkite artifacts.

---

### Overwatch

Overwatch is a dedicated monitoring stack deployed in the `overwatch` namespace that observes the test infrastructure itself. It scrapes metrics from VMCluster / VMSingle deployments created during tests.

**Components:**
- `vmsingle-overwatch` — VMSingle instance storing all collected metrics (`manifests/overwatch/vmsingle.yaml`)
- `vmks` — VMAgent scraping the main test cluster, forwarding to overwatch VMSingle (`manifests/overwatch/vmagent.yaml`)
- `vmsingle-ingress` — nginx Ingress exposing VMSingle at `vmsingle.example.com` (`manifests/overwatch/vmsingle-ingress.yaml`)

Installed by `InstallOverwatch` (`pkg/install/helm.go`): applies the VMSingle manifest, waits for the deployment, then reconfigures VMAlert to remote-write into overwatch.

**Role in tests:** Tests query overwatch via the Prometheus API to assert on real scraped metrics from the system under test. `pkg/gather/vm.go` uses it for health checks; `RestartOverwatchInstance` can restart it as part of resilience scenarios.

---

## Writing New Tests

### Package overview

| Package | Purpose |
|---|---|
| `pkg/tests/` | Fluent builders and test utilities |
| `pkg/install/` | Deploy/configure Kubernetes resources (Helm, CRDs) |
| `pkg/promquery/` | Prometheus API client wrapper |
| `pkg/consts/` | Shared constants, timeouts, namespace names |
| `pkg/gather/` | Diagnostic log/event collection on failure |

### Key builders (`pkg/tests/`)

```go
// Build test time series
ts := tests.NewTimeSeriesBuilder("metric_name").
    WithCount(10).
    WithValue(42).
    Build()

// Write time series data via remote write
tests.NewRemoteWriteBuilder().
    WithHTTPClient(c).
    ForTenant(namespace, 0).
    Send(ts)

// Query via prometheus API
prom := tests.NewPromClientBuilder().
    WithNamespace(namespace).
    WithTenant(0).
    MustBuild()
value, err := tests.RetryVectorScan(ctx, t, namespace, prom, "metric_name", 5)

// JSON patch for manifests
patch := tests.NewJSONPatchBuilder().
    Add("/spec/remoteWrite/0/url", insertURL).
    MustBuild()

// ConfigMap with relabel/aggregation config
cm := tests.NewConfigMapBuilder("my-config").
    WithRelabelConfig(relabelYAML).
    Apply(t, kubeOpts)
```

### Install helpers (`pkg/install/`)

```go
install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, patches)
install.InstallVMSingle(ctx, t, kubeOpts, namespace, vmclient, patches)
install.InstallVMAgent(ctx, t, kubeOpts, namespace, vmclient, url)
install.InstallK6(ctx, t, kubeOpts, namespace)
install.RunK6Scenario(ctx, t, kubeOpts, namespace, scenarioPath)
install.RunChaosScenario(ctx, t, kubeOpts, namespace, scenarioPath)
```

### Test structure pattern

```go
var _ = SynchronizedBeforeSuite(func(ctx context.Context) {
    // Runs once: install operator, monitoring stack
    install.InstallVMK8StackWithHelm(ctx, t, kubeOpts)
}, func(ctx context.Context) {
    // Runs per parallel process: set up namespace
    namespace = tests.RandomNamespace("vm")
})

var _ = Describe("My feature", Label("vmcluster"), func() {
    BeforeEach(func(ctx context.Context) {
        install.InstallVMCluster(ctx, t, kubeOpts, namespace, vmclient, nil)
    })
    AfterEach(func(ctx context.Context) {
        gather.VMAfterAll(ctx, t, kubeOpts, namespace)  // collect diagnostics on failure
    })

    It("should do something", Label("id=<UUID>"), func(ctx context.Context) {
        By("Step description for reporting")
        // test body
    })
})
```

Each test gets an isolated namespace via `tests.RandomNamespace()`, enabling safe parallel execution.

### Manifests

- `manifests/kind.yaml` — kind cluster config
- `manifests/smoke.yaml` — default Helm values
- `manifests/distributed.yaml` — distributed chart values
- `manifests/load-tests/` — k6 scenario scripts
- `manifests/chaos-tests/` — Chaos Mesh scenario YAMLs (organized by type: pods/, cpu/, memory/, io/, network/, http/)

---

## Running Tests

### Prerequisites

```bash
make install-dependencies   # installs go, kubectl, helm, kind, ginkgo, etc.
```

### Locally (kind)

```bash
make test-kind
```
Creates a kind cluster, runs functional tests tagged `kind`, then deletes the cluster.

### On GKE

```bash
export PROJECT_ID=my-gcp-project
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/key.json
export MANIFESTS_DIR=$(pwd)/manifests
export PROCS=3 # parallelization
make test-gke TEST_SUITE=functional
```

Available `TEST_SUITE` values: `functional`, `load`, `chaos`, `distributed`.

### Manual ginkgo invocation

```bash
ginkgo -v \
  --label-filter='vmcluster && !enterprise' \
  -procs=2 -timeout=60m \
  ./tests/functional_test \
  -- \
  -env-k8s-distro=kind \
  -operator-tag=v0.68.3 \
  -vm-vmsingledefault-version=v1.140.0 \
  -vm-vmclusterdefault-vmselectdefault-version=v1.140.0-cluster \
  -report=/tmp/allure-results
```

### Enterprise and RC builds

```bash
make test-gke VM_ENTERPRISE=1   # use enterprise images, autoinjects VM license
make test-gke VM_RC=1           # helper for RC images
```

### Unit tests

```bash
make test-unit   # tests pkg/ without a cluster
```

---

## CI (Buildkite)

The pipeline is defined in `.buildkite/pipeline.yml` with dynamic generation via `.buildkite/generate_pipeline.py`.

**Flow:**
1. Extract PR labels from GitHub
2. Build the test runner Docker image (pre-compiles all test binaries)
3. Generate test steps based on labels — each suite runs in a separate GKE cluster
4. Each suite: provision cluster → run tests → upload Allure results to GCS → destroy cluster
5. Merge results and publish HTML Allure report

**PR labels control which suites run:**

| Label | Suite |
|---|---|
| `functional-test` | Functional |
| `load-test` | Load |
| `chaos-test` | Chaos |
| `distributed-test` | Distributed |
| `enterprise` | Use enterprise images |
| `rc` | Use RC images |

---

## Dependency Updates (Renovate)

[Renovate](https://docs.renovatebot.com/) manages dependency updates via `renovate.json`. It tracks:

- **Go modules** (`go.mod`) — runs `go mod tidy` after updates
- **Tool versions in `Makefile`**: Go, Kind, kubectl, Terraform, Ginkgo, crust-gather, vmgather, VictoriaMetrics Operator
- **VictoriaMetrics component versions** (in `Makefile`): grouped by release channel and labeled to trigger the appropriate test suites automatically

Renovate PRs are pre-labeled so CI runs only the relevant test suites:
- Updates to `vmstorage` docker registry (default) → labels `functional-test`, `load-test`, `chaos-test`, `distributed-test`
- Updates to `vmstorage` containing `-enterprise` in the tag → adds `enterprise` label
- Updates to `vmstorage` containing `-rc` in the tag → adds `rc` label
- All other dependency updates → `functional-test` only
