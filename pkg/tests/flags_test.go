package tests

import (
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

func TestVMSingleDefaultImageFlag(t *testing.T) {
	// Save original env var and restore after test
	originalEnv := os.Getenv("VM_VMSINGLEDEFAULT_IMAGE")
	defer os.Setenv("VM_VMSINGLEDEFAULT_IMAGE", originalEnv)

	// Set env var to test default value from environment
	testImage := "victoriametrics/victoria-metrics"
	os.Setenv("VM_VMSINGLEDEFAULT_IMAGE", testImage)

	// Create a new flag set to test defaults
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	var testVMSingleImage string
	testFlagSet.StringVar(&testVMSingleImage, "vm-vmsingledefault-image", os.Getenv("VM_VMSINGLEDEFAULT_IMAGE"), "Default image for VMSingle")

	// Parse empty args to get defaults
	err := testFlagSet.Parse([]string{})
	require.NoError(t, err)

	assert.Equal(t, testImage, testVMSingleImage, "Expected flag to pick up value from VM_VMSINGLEDEFAULT_IMAGE env var")

	// Test overriding via flag
	overrideImage := "my-custom/vmsingle"
	err = testFlagSet.Parse([]string{"-vm-vmsingledefault-image", overrideImage})
	require.NoError(t, err)

	assert.Equal(t, overrideImage, testVMSingleImage, "Expected flag to override environment variable value")
}

func TestVMSingleDefaultVersionFlag(t *testing.T) {
	// Save original env var and restore after test
	originalEnv := os.Getenv("VM_VMSINGLEDEFAULT_VERSION")
	defer os.Setenv("VM_VMSINGLEDEFAULT_VERSION", originalEnv)

	// Set env var to test default value from environment
	testVersion := "v1.134.0"
	os.Setenv("VM_VMSINGLEDEFAULT_VERSION", testVersion)

	// Create a new flag set to test defaults
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	var testVMSingleVersion string
	testFlagSet.StringVar(&testVMSingleVersion, "vm-vmsingledefault-version", os.Getenv("VM_VMSINGLEDEFAULT_VERSION"), "Default version for VMSingle")

	// Parse empty args to get defaults
	err := testFlagSet.Parse([]string{})
	require.NoError(t, err)

	assert.Equal(t, testVersion, testVMSingleVersion, "Expected flag to pick up value from VM_VMSINGLEDEFAULT_VERSION env var")

	// Test overriding via flag
	overrideVersion := "v1.99.9"
	err = testFlagSet.Parse([]string{"-vm-vmsingledefault-version", overrideVersion})
	require.NoError(t, err)

	assert.Equal(t, overrideVersion, testVMSingleVersion, "Expected flag to override environment variable value")
}

func TestVMSingleDefaultInitIntegration(t *testing.T) {
	// Save original values
	origImg := vmSingleDefaultImage
	origVer := vmSingleDefaultVersion
	defer func() {
		vmSingleDefaultImage = origImg
		vmSingleDefaultVersion = origVer
	}()

	testImg := "repo/image"
	testVer := "v1.2.3"

	// Mock the flag variables
	vmSingleDefaultImage = testImg
	vmSingleDefaultVersion = testVer

	// Call Init to sync flags to consts
	Init()

	assert.Equal(t, testImg, consts.VMSingleDefaultImage())
	assert.Equal(t, testVer, consts.VMSingleDefaultVersion())
}

func TestReportLocationFlag(t *testing.T) {
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	var testReport string
	testFlagSet.StringVar(&testReport, "report", "/tmp/allure-results", "Report location")

	err := testFlagSet.Parse([]string{"-report", "/custom/path"})
	require.NoError(t, err)
	assert.Equal(t, "/custom/path", testReport)
}

func TestEnvK8SDistroFlag(t *testing.T) {
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	var testDistro string
	testFlagSet.StringVar(&testDistro, "env-k8s-distro", "kind", "Kube distro name")

	err := testFlagSet.Parse([]string{"-env-k8s-distro", "gke"})
	require.NoError(t, err)
	assert.Equal(t, "gke", testDistro)
}

func TestOperatorFlags(t *testing.T) {
	testFlagSet := flag.NewFlagSet("test", flag.ContinueOnError)
	var testRegistry, testRepository, testTag string
	testFlagSet.StringVar(&testRegistry, "operator-registry", "", "Operator image registry")
	testFlagSet.StringVar(&testRepository, "operator-repository", "", "Operator image repository")
	testFlagSet.StringVar(&testTag, "operator-tag", "", "Operator image tag")

	err := testFlagSet.Parse([]string{
		"-operator-registry", "reg",
		"-operator-repository", "repo",
		"-operator-tag", "tag",
	})
	require.NoError(t, err)
	assert.Equal(t, "reg", testRegistry)
	assert.Equal(t, "repo", testRepository)
	assert.Equal(t, "tag", testTag)
}

// TestMakefileFlagCombosInitIntegration verifies that each Makefile flag combination
// (VM_RC, VM_ENTERPRISE, VM_LTS_VERSION=current/previous) flows correctly from flag
// variables through Init() into the consts package for VMSingle and VMCluster components.
// Keep version values in sync with Makefile lines 20-68.
func TestMakefileFlagCombosInitIntegration(t *testing.T) {
	scenarios := []struct {
		name           string
		singleVersion  string
		clusterVersion string
	}{
		{
			name:           "default (no flags)",
			singleVersion:  "v1.144.0",
			clusterVersion: "v1.144.0-cluster",
		},
		{
			name:           "VM_ENTERPRISE=1",
			singleVersion:  "v1.128.0-enterprise",
			clusterVersion: "v1.128.0-enterprise-cluster",
		},
		{
			name:           "VM_RC=1",
			singleVersion:  "v1.143.0-enterprise-cluster-rc0",
			clusterVersion: "v1.143.0-cluster-rc0",
		},
		{
			name:           "VM_LTS_VERSION=current",
			singleVersion:  "v1.136.9-enterprise",
			clusterVersion: "v1.136.9-enterprise-cluster",
		},
		{
			name:           "VM_LTS_VERSION=previous",
			singleVersion:  "v1.122.22-enterprise",
			clusterVersion: "v1.122.22-enterprise-cluster",
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			origSingle := vmSingleDefaultVersion
			origSelect := vmClusterVMSelectDefaultVersion
			origStorage := vmClusterVMStorageDefaultVersion
			origInsert := vmClusterVMInsertDefaultVersion
			defer func() {
				vmSingleDefaultVersion = origSingle
				vmClusterVMSelectDefaultVersion = origSelect
				vmClusterVMStorageDefaultVersion = origStorage
				vmClusterVMInsertDefaultVersion = origInsert
			}()

			vmSingleDefaultVersion = s.singleVersion
			vmClusterVMSelectDefaultVersion = s.clusterVersion
			vmClusterVMStorageDefaultVersion = s.clusterVersion
			vmClusterVMInsertDefaultVersion = s.clusterVersion

			Init()

			assert.Equal(t, s.singleVersion, consts.VMSingleDefaultVersion(), "VMSingle version")
			assert.Equal(t, s.clusterVersion, consts.VMClusterVMSelectDefaultVersion(), "VMSelect version")
			assert.Equal(t, s.clusterVersion, consts.VMClusterVMStorageDefaultVersion(), "VMStorage version")
			assert.Equal(t, s.clusterVersion, consts.VMClusterVMInsertDefaultVersion(), "VMInsert version")
		})
	}
}

func TestOperatorInitIntegration(t *testing.T) {
	// Save original values
	origReg := operatorRegistry
	origRepo := operatorRepository
	origTag := operatorTag
	defer func() {
		operatorRegistry = origReg
		operatorRepository = origRepo
		operatorTag = origTag
	}()

	testReg := "my-registry"
	testRepo := "my-repo"
	testTag := "v1.0.0"

	// Mock the flag variables
	operatorRegistry = testReg
	operatorRepository = testRepo
	operatorTag = testTag

	// Call Init to sync flags to consts
	Init()

	assert.Equal(t, testReg, consts.OperatorImageRegistry())
	assert.Equal(t, testRepo, consts.OperatorImageRepository())
	assert.Equal(t, testTag, consts.OperatorImageTag())
}
