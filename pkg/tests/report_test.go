package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvironmentPropertiesContent(t *testing.T) {
	tests := []struct {
		name     string
		props    map[string]string
		expected []string
	}{
		{
			name:     "empty properties",
			props:    map[string]string{},
			expected: []string{},
		},
		{
			name: "single property",
			props: map[string]string{
				"key1": "value1",
			},
			expected: []string{"key1=value1"},
		},
		{
			name: "multiple properties",
			props: map[string]string{
				"kube-distro":      "kind",
				"helm-chart":       "v1.2.3",
				"operator-version": "v0.47.0",
			},
			expected: []string{
				"helm-chart=v1.2.3",
				"kube-distro=kind",
				"operator-version=v0.47.0",
			},
		},
		{
			name: "properties with special characters",
			props: map[string]string{
				"key-with-dashes":    "value-with-dashes",
				"key_with_undercore": "value_with_underscore",
				"key.with.dots":      "value.with.dots",
			},
			expected: []string{
				"key-with-dashes=value-with-dashes",
				"key.with.dots=value.with.dots",
				"key_with_undercore=value_with_underscore",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := environmentPropertiesContent(tt.props)
			resultStr := string(result)

			// Split by newlines and filter out empty lines
			lines := strings.Split(strings.TrimSpace(resultStr), "\n")
			if len(tt.expected) == 0 {
				if resultStr != "" {
					assert.Empty(t, resultStr, "Expected empty content for empty props")
				}
				return
			}

			assert.Equal(t, len(tt.expected), len(lines), "Expected %d lines", len(tt.expected))

			// Check that all expected lines are present (order should be alphabetical)
			for i, expectedLine := range tt.expected {
				if i < len(lines) {
					assert.Equal(t, expectedLine, lines[i], "Expected line %d to match", i)
				}
			}
		})
	}
}

func TestEnvironmentPropertiesContentOrdering(t *testing.T) {
	// Test that properties are sorted alphabetically
	props := map[string]string{
		"zebra": "last",
		"alpha": "first",
		"beta":  "second",
	}

	result := environmentPropertiesContent(props)
	resultStr := string(result)

	expectedOrder := []string{
		"alpha=first",
		"beta=second",
		"zebra=last",
	}

	lines := strings.Split(strings.TrimSpace(resultStr), "\n")

	for i, expectedLine := range expectedOrder {
		if i < len(lines) {
			assert.Equal(t, expectedLine, lines[i], "Expected line %d to match", i)
		} else {
			t.Errorf("Missing line %d: %s", i, expectedLine)
		}
	}
}

func TestWriteEnvironmentProperties(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "test-env-props")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tempDir)

	// Set some test values in consts
	consts.SetEnvK8SDistro("test-distro")
	consts.SetHelmChartVersion("test-chart-v1.0.0")
	consts.SetOperatorVersion("test-op-v2.0.0")
	consts.SetVMVersion("test-vm-v3.0.0")

	// Test writeEnvironmentProperties
	err = writeEnvironmentProperties(tempDir)
	require.NoError(t, err, "writeEnvironmentProperties failed")

	// Verify the file was created
	envFilePath := filepath.Join(tempDir, "environment.properties")
	_, err = os.Stat(envFilePath)
	assert.False(t, os.IsNotExist(err), "environment.properties file was not created")

	// Read and verify the content
	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read environment.properties")

	contentStr := string(content)
	expectedLines := []string{
		"helm-chart=test-chart-v1.0.0",
		"kube-distro=test-distro",
		"operator-version=test-op-v2.0.0",
		"vm-version=test-vm-v3.0.0",
	}

	lines := strings.Split(strings.TrimSpace(contentStr), "\n")

	assert.Equal(t, len(expectedLines), len(lines), "Expected line count match")

	for i, expectedLine := range expectedLines {
		if i < len(lines) {
			assert.Equal(t, expectedLine, lines[i], "Expected line %d to match", i)
		}
	}
}

func TestWriteEnvironmentPropertiesCreatesDirectory(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "test-env-props-dir")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tempDir)

	// Use a nested path that doesn't exist
	nestedPath := filepath.Join(tempDir, "nested", "path")

	// Test writeEnvironmentProperties with nested path
	err = writeEnvironmentProperties(nestedPath)
	require.NoError(t, err, "writeEnvironmentProperties failed")

	// Verify the nested directory was created
	_, err = os.Stat(nestedPath)
	assert.False(t, os.IsNotExist(err), "Nested directory was not created")

	// Verify the file was created in the nested directory
	envFilePath := filepath.Join(nestedPath, "environment.properties")
	_, err = os.Stat(envFilePath)
	assert.False(t, os.IsNotExist(err), "environment.properties file was not created in nested directory")
}

func TestWriteEnvironmentPropertiesWithEmptyValues(t *testing.T) {
	// Create a temporary directory
	tempDir, err := os.MkdirTemp("", "test-env-props-empty")
	require.NoError(t, err, "Failed to create temp directory")
	defer os.RemoveAll(tempDir)

	// Set empty values in consts
	consts.SetEnvK8SDistro("")
	consts.SetHelmChartVersion("")
	consts.SetOperatorVersion("")
	consts.SetVMVersion("")

	// Test writeEnvironmentProperties
	err = writeEnvironmentProperties(tempDir)
	require.NoError(t, err, "writeEnvironmentProperties failed")

	// Read and verify the content
	envFilePath := filepath.Join(tempDir, "environment.properties")
	content, err := os.ReadFile(envFilePath)
	require.NoError(t, err, "Failed to read environment.properties")

	contentStr := string(content)
	expectedLines := []string{
		"helm-chart=",
		"kube-distro=",
		"operator-version=",
		"vm-version=",
	}

	lines := strings.Split(strings.TrimSpace(contentStr), "\n")

	assert.Equal(t, len(expectedLines), len(lines), "Expected line count match")

	for i, expectedLine := range expectedLines {
		if i < len(lines) {
			assert.Equal(t, expectedLine, lines[i], "Expected line %d to match", i)
		}
	}
}

func TestConstsIntegration(t *testing.T) {
	// Test that the functions can interact with consts package properly
	originalDistro := consts.EnvK8SDistro()
	originalChart := consts.HelmChartVersion()
	originalOperator := consts.OperatorVersion()

	// Clean up after test
	defer func() {
		consts.SetEnvK8SDistro(originalDistro)
		consts.SetHelmChartVersion(originalChart)
		consts.SetOperatorVersion(originalOperator)
	}()

	// Test setting and getting values
	testDistro := "test-k8s-distro"
	testChart := "test-helm-chart"
	testOperator := "test-operator"

	consts.SetEnvK8SDistro(testDistro)
	consts.SetHelmChartVersion(testChart)
	consts.SetOperatorVersion(testOperator)

	assert.Equal(t, testDistro, consts.EnvK8SDistro())
	assert.Equal(t, testChart, consts.HelmChartVersion())
	assert.Equal(t, testOperator, consts.OperatorVersion())
}
