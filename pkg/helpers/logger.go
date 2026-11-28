package helpers

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

var filteredSubstrings = []string{
	"Configuring Kubernetes client using config file",
	"is not available. Sleeping for",
	"Warning: deleting cluster-scoped resources",
	"Waiting for traefik service to have LoadBalancer",
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
	depth := 3
	// A nil Terratest logger forwards through Default, adding one wrapper.
	if pc, _, _, ok := runtime.Caller(2); ok && strings.HasSuffix(runtime.FuncForPC(pc).Name(), "/logger.(*Logger).Logf") {
		depth++
	}
	logger.DoLog(t, depth, os.Stdout, msg)
	AddAllureLog(msg)
}
