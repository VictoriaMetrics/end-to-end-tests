FROM quay.io/vrutkovs/e2e-runner:golang-1.26.3 AS builder

RUN go install go.k6.io/xk6@v1.4.1 && \
    xk6 build v2.0.0 \
    --with github.com/grafana/xk6-client-prometheus-remote@v0.5.0 \
    --output /k6

FROM alpine:3.21

RUN adduser -D -u 12345 k6
COPY --from=builder /k6 /usr/local/bin/k6

USER k6
WORKDIR /home/k6

ENTRYPOINT ["k6"]
