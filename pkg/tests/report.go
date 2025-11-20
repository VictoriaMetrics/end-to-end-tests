package tests

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/tests/allure"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	. "github.com/onsi/ginkgo/v2" //nolint
	"github.com/onsi/ginkgo/v2/types"
)

// GetT returns a testing.T compatible object that can be used in terratesting.RunE2ETests
func GetT() terratesting.TestingT {
	return &myTestingT{
		GinkgoT(),
	}
}

// myTestingT wraps GinkgoTInterface to satisfy terratesting.TestingT interface.
type myTestingT struct {
	GinkgoTInterface
}

// Name returns the name of the test.
// It returns an empty string as Ginkgo tests don't map 1:1 to testing.T names in this context.
func (mt *myTestingT) Name() string {
	return ""
}

var _ = ReportAfterSuite("allure report", func(report types.Report) {
	reportPath, err := filepath.Abs(consts.ReportLocation())
	if err != nil {
		panic(err)
	}

	// Set ALLURE_RESULTS_PATH to reportPath so that allure package writes to the correct location
	if err := os.Setenv("ALLURE_RESULTS_PATH", reportPath); err != nil {
		panic(err)
	}

	if err := allure.FromGinkgoReport(report); err != nil {
		panic(err)
	}

	if err := writeEnvironmentProperties(reportPath); err != nil {
		panic(err)
	}
	GinkgoLogr.Info("Allure report generated", "path", reportPath)
})

// writeEnvironmentProperties writes environment information to environment.properties file in the report directory.
// This file is used by Allure to display environment information.
func writeEnvironmentProperties(reportPath string) error {
	envFilePath := filepath.Join(reportPath, "environment.properties")
	if err := os.MkdirAll(filepath.Dir(envFilePath), 0755); err != nil {
		return err
	}

	props := map[string]string{
		"kube-distro":      consts.EnvK8SDistro(),
		"helm-chart":       consts.HelmChartVersion(),
		"operator-version": consts.OperatorVersion(),
		"vm-version":       consts.VMVersion(),
	}
	GinkgoLogr.Info("Writing environment properties", "to", envFilePath, "properties", props)

	return os.WriteFile(envFilePath, environmentPropertiesContent(props), 0644)
}

// environmentPropertiesContent generates the content for environment.properties file
// from a map of properties. Keys are sorted alphabetically.
func environmentPropertiesContent(props map[string]string) []byte {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, k := range keys {
		builder.WriteString(k)
		builder.WriteString("=")
		builder.WriteString(props[k])
		builder.WriteString("\n")
	}

	return []byte(builder.String())
}
