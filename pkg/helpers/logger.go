package helpers

import (
	"fmt"
	"os"
	"strings"

	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

var filteredSubstrings = []string{
	"Configuring Kubernetes client using config file",
	"is not available. Sleeping for",
	"Warning: deleting cluster-scoped resources",
	"Waiting for ingress-nginx-controller service to have LoadBalancer",
	"Wait for ingress ",
}

// FilterLogger drops high-frequency Terratest log messages without signal.
type FilterLogger struct{}

func (FilterLogger) Logf(t terratesting.TestingT, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	for _, substring := range filteredSubstrings {
		if strings.Contains(msg, substring) {
			return
		}
	}
	logger.DoLog(t, 3, os.Stdout, msg)
	AddAllureLog(msg)
}
