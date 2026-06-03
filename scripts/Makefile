POD_NAME=bench-press
VM_CONTAINER=victoria-metrics
GRAFANA_CONTAINER=grafana
VMANOMALY_CONTAINER=vmanomaly
PROVISIONING_DIR=$(shell pwd)/provisioning
CUSTOM_DASHBOARDS_DIR=$(shell pwd)/custom_dashboards
VMANOMALY_DIR=$(shell pwd)/vmanomaly
LICENSE_FILE ?=
DASHBOARD_URLS= \
	https://github.com/VictoriaMetrics/VictoriaMetrics/raw/refs/heads/master/dashboards/victoriametrics-cluster.json \
	https://github.com/VictoriaMetrics/VictoriaMetrics/raw/refs/heads/master/dashboards/vmagent.json \
	https://github.com/VictoriaMetrics/VictoriaMetrics/raw/refs/heads/master/dashboards/vmauth.json

.PHONY: up down status logs clean dashboards run help

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@echo "  up          Create and start the Podman pod (VictoriaMetrics + Grafana)"
	@echo "  down        Stop and remove the Podman pod"
	@echo "  status      Show current pod and container status"
	@echo "  logs        Show logs for all containers in the pod"
	@echo "  dashboards  Download Grafana dashboard JSON files"
	@echo "  clean       Remove the pod and all persistent data"
	@echo "  run         Show usage for bench_press.py"

run:
	@python3 bench_press.py --help

dashboards:
	@mkdir -p $(PROVISIONING_DIR)/dashboards
	@for url in $(DASHBOARD_URLS); do \
		name=$$(basename $$url); \
		if [ ! -f $(PROVISIONING_DIR)/dashboards/$$name ]; then \
			echo "Downloading dashboard $$name..."; \
			curl -sL $$url > $(PROVISIONING_DIR)/dashboards/$$name; \
		fi \
	done

up: dashboards
	@if ! podman pod exists $(POD_NAME); then \
		echo "Creating pod $(POD_NAME)..."; \
		podman pod create --name $(POD_NAME) \
			-p 3000:3000 \
			-p 8428:8428 \
			-p 8490:8490; \
	fi
	@if ! podman container exists $(VM_CONTAINER); then \
		echo "Starting VictoriaMetrics..."; \
		podman run -d --pod $(POD_NAME) \
			--name $(VM_CONTAINER) \
			docker.io/victoriametrics/victoria-metrics:latest \
			-maxLabelsPerTimeseries=200; \
	fi
	@if [ -n "$(LICENSE_FILE)" ] && ! podman container exists $(VMANOMALY_CONTAINER); then \
		echo "Starting VMAnomaly..."; \
		podman run -d --pod $(POD_NAME) \
			--name $(VMANOMALY_CONTAINER) \
			-v $(VMANOMALY_DIR)/config.yml:/etc/vmanomaly/config.yml:Z \
			-v $(LICENSE_FILE):/etc/vmanomaly/license:Z \
			docker.io/victoriametrics/vmanomaly:latest \
			/etc/vmanomaly/config.yml \
			--license-file=/etc/vmanomaly/license; \
	elif [ -z "$(LICENSE_FILE)" ]; then \
		echo "Skipping VMAnomaly: LICENSE_FILE not set"; \
	fi
	@if ! podman container exists $(GRAFANA_CONTAINER); then \
		echo "Starting Grafana..."; \
		podman run -d --pod $(POD_NAME) \
			--name $(GRAFANA_CONTAINER) \
			-v $(PROVISIONING_DIR)/datasources:/etc/grafana/provisioning/datasources:Z \
			-v $(PROVISIONING_DIR)/dashboards:/etc/grafana/provisioning/dashboards:Z \
			-v $(CUSTOM_DASHBOARDS_DIR):/etc/grafana/provisioning/custom_dashboards:Z \
			docker.io/grafana/grafana:latest; \
	fi
	@echo "Services started:"
	@echo " - Grafana:          http://localhost:3000"
	@echo " - VictoriaMetrics:  http://localhost:8428"
	@if [ -n "$(LICENSE_FILE)" ]; then echo " - VMAnomaly:        http://localhost:8490"; fi

down:
	@echo "Stopping and removing pod $(POD_NAME)..."
	@podman pod rm -f $(POD_NAME) || true

status:
	@podman pod ps --filter name=$(POD_NAME)
	@podman ps --filter pod=$(POD_NAME)

logs:
	@podman pod logs -f $(POD_NAME)

clean: down
	@echo "Cleaning up..."
	@rm -rf provisioning/dashboards
