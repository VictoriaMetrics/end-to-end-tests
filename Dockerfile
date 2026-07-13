ARG GO_VERSION=1.26.5
FROM quay.io/vrutkovs/e2e-runner:golang-1.26.5

ARG OPENTOFU_VERSION=1.12.4

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl \
    unzip \
    gnupg \
    apt-transport-https \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install opentofu
RUN curl -fsSL https://github.com/opentofu/opentofu/releases/download/v${OPENTOFU_VERSION}/tofu_${OPENTOFU_VERSION}_linux_amd64.zip \
        -o /tmp/tofu.zip && \
    unzip /tmp/tofu.zip tofu -d /usr/local/bin/ && \
    rm /tmp/tofu.zip

# Install gcloud SDK
RUN curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
        | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
        | tee /etc/apt/sources.list.d/google-cloud-sdk.list && \
    apt-get update && apt-get install -y --no-install-recommends \
        google-cloud-sdk \
        google-cloud-sdk-gke-gcloud-auth-plugin \
    && curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

# Pre-install terraform providers
COPY terraform/ /terraform/
RUN tofu -chdir=/terraform/gke init -backend=false

# Install Ginkgo binary
RUN go install github.com/onsi/ginkgo/v2/ginkgo@latest

WORKDIR /app
COPY Makefile ./
# Install tools into /usr/local/bin so they survive git clean on workdir mount
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    make install-dependencies BIN_DIR=/usr/local/bin

# Pre-cache module dependencies as a separate layer
# Only invalidates when go.mod/go.sum change, not on every code change
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Precompile binaries in the runner
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /tests && \
    for test in load_test chaos_test distributed_test functional_test enterprise_test; do \
        go test -c -o /tests/${test}.test ./tests/${test} || exit 1; \
    done
