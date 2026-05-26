package install

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

func TestBuildVMK8StackValues(t *testing.T) {
	// Save original values and restore after test
	originalNginxHost := consts.NginxHost()
	defer func() {
		consts.SetNginxHost(originalNginxHost)
		consts.SetOperatorImageRegistry("")
		consts.SetOperatorImageRepository("")
		consts.SetOperatorImageTag("")
		consts.SetVMSingleDefaultImage("")
		consts.SetVMSingleDefaultVersion("")
		consts.SetVMClusterVMSelectDefaultImage("")
		consts.SetVMClusterVMSelectDefaultVersion("")
		consts.SetVMClusterVMStorageDefaultImage("")
		consts.SetVMClusterVMStorageDefaultVersion("")
		consts.SetVMClusterVMInsertDefaultImage("")
		consts.SetVMClusterVMInsertDefaultVersion("")
		consts.SetVMAgentDefaultImage("")
		consts.SetVMAgentDefaultVersion("")
		consts.SetVMAlertDefaultImage("")
		consts.SetVMAlertDefaultVersion("")
		consts.SetVMAuthDefaultImage("")
		consts.SetVMAuthDefaultVersion("")
	}()

	tests := []struct {
		name           string
		setup          func()
		namespace      string
		nginxHost      string
		expectedValues map[string]string
	}{
		{
			name: "Only ingress hosts are set by default",
			setup: func() {
				consts.SetNginxHost("1.2.3.4")
			},
			namespace: "prod",
			expectedValues: map[string]string{
				"vmcluster.ingress.select.hosts[0]": "vmselect-prod.1.2.3.4.nip.io",
				"vmcluster.ingress.insert.hosts[0]": "vminsert-prod.1.2.3.4.nip.io",
			},
		},
		{
			name: "VMSingle default image and version are passed to operator",
			setup: func() {
				consts.SetVMSingleDefaultImage("repo/vmsingle")
				consts.SetVMSingleDefaultVersion("v1.100.0")
			},
			namespace: "test",
			expectedValues: map[string]string{
				"victoria-metrics-operator.env[0].name":  "VM_VMSINGLEDEFAULT_IMAGE",
				"victoria-metrics-operator.env[0].value": "repo/vmsingle",
				"victoria-metrics-operator.env[1].name":  "VM_VMSINGLEDEFAULT_VERSION",
				"victoria-metrics-operator.env[1].value": "v1.100.0",
			},
		},
		{
			name: "VMCluster defaults are passed to operator",
			setup: func() {
				consts.SetVMClusterVMSelectDefaultImage("repo/vmselect")
				consts.SetVMClusterVMSelectDefaultVersion("v1.100.0-cluster")
				consts.SetVMClusterVMStorageDefaultImage("repo/vmstorage")
				consts.SetVMClusterVMStorageDefaultVersion("v1.100.0-cluster")
				consts.SetVMClusterVMInsertDefaultImage("repo/vminsert")
				consts.SetVMClusterVMInsertDefaultVersion("v1.100.0-cluster")
			},
			namespace: "test",
			expectedValues: map[string]string{
				"victoria-metrics-operator.env[0].name":  "VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE",
				"victoria-metrics-operator.env[0].value": "repo/vmselect",
				"victoria-metrics-operator.env[1].name":  "VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION",
				"victoria-metrics-operator.env[1].value": "v1.100.0-cluster",
				"victoria-metrics-operator.env[2].name":  "VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE",
				"victoria-metrics-operator.env[2].value": "repo/vmstorage",
				"victoria-metrics-operator.env[3].name":  "VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION",
				"victoria-metrics-operator.env[3].value": "v1.100.0-cluster",
				"victoria-metrics-operator.env[4].name":  "VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE",
				"victoria-metrics-operator.env[4].value": "repo/vminsert",
				"victoria-metrics-operator.env[5].name":  "VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION",
				"victoria-metrics-operator.env[5].value": "v1.100.0-cluster",
			},
		},
		{
			name: "Other components defaults are passed to operator",
			setup: func() {
				consts.SetVMAgentDefaultImage("repo/vmagent")
				consts.SetVMAgentDefaultVersion("v1.100.0")
				consts.SetVMAlertDefaultImage("repo/vmalert")
				consts.SetVMAlertDefaultVersion("v1.100.0")
				consts.SetVMAuthDefaultImage("repo/vmauth")
				consts.SetVMAuthDefaultVersion("v1.100.0")
			},
			namespace: "test",
			expectedValues: map[string]string{
				"victoria-metrics-operator.env[0].name":  "VM_VMAGENTDEFAULT_IMAGE",
				"victoria-metrics-operator.env[0].value": "repo/vmagent",
				"victoria-metrics-operator.env[1].name":  "VM_VMAGENTDEFAULT_VERSION",
				"victoria-metrics-operator.env[1].value": "v1.100.0",
				"victoria-metrics-operator.env[2].name":  "VM_VMALERTDEFAULT_IMAGE",
				"victoria-metrics-operator.env[2].value": "repo/vmalert",
				"victoria-metrics-operator.env[3].name":  "VM_VMALERTDEFAULT_VERSION",
				"victoria-metrics-operator.env[3].value": "v1.100.0",
				"victoria-metrics-operator.env[4].name":  "VM_VMAUTHDEFAULT_IMAGE",
				"victoria-metrics-operator.env[4].value": "repo/vmauth",
				"victoria-metrics-operator.env[5].name":  "VM_VMAUTHDEFAULT_VERSION",
				"victoria-metrics-operator.env[5].value": "v1.100.0",
			},
		},
		{
			name: "Operator image registry, repository and tag are passed to helm",
			setup: func() {
				consts.SetOperatorImageRegistry("my-registry")
				consts.SetOperatorImageRepository("my-repo")
				consts.SetOperatorImageTag("v1.2.3")
			},
			namespace: "test",
			expectedValues: map[string]string{
				"victoria-metrics-operator.image.registry":   "my-registry",
				"victoria-metrics-operator.image.repository": "my-repo",
				"victoria-metrics-operator.image.tag":        "v1.2.3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all defaults before each test case
			consts.SetOperatorImageRegistry("")
			consts.SetOperatorImageRepository("")
			consts.SetOperatorImageTag("")
			consts.SetVMSingleDefaultImage("")
			consts.SetVMSingleDefaultVersion("")
			consts.SetVMClusterVMSelectDefaultImage("")
			consts.SetVMClusterVMSelectDefaultVersion("")
			consts.SetVMClusterVMStorageDefaultImage("")
			consts.SetVMClusterVMStorageDefaultVersion("")
			consts.SetVMClusterVMInsertDefaultImage("")
			consts.SetVMClusterVMInsertDefaultVersion("")
			consts.SetVMAgentDefaultImage("")
			consts.SetVMAgentDefaultVersion("")
			consts.SetVMAlertDefaultImage("")
			consts.SetVMAlertDefaultVersion("")
			consts.SetVMAuthDefaultImage("")
			consts.SetVMAuthDefaultVersion("")

			tt.setup()
			setValues := buildVMK8StackValues(tt.namespace)

			for key, expectedValue := range tt.expectedValues {
				actualValue, exists := setValues[key]
				assert.True(t, exists, "Expected key '%s' to exist in setValues for test '%s'", key, tt.name)
				assert.Equal(t, expectedValue, actualValue, "Value mismatch for key '%s' in test '%s'", key, tt.name)
			}
		})
	}
}

// makefileVersionScenario mirrors the version selection logic in the Makefile.
// Each scenario corresponds to a flag combination (VM_RC, VM_ENTERPRISE, VM_LTS_VERSION)
// and the exact image+version values the Makefile would produce.
type makefileVersionScenario struct {
	name           string
	singleImage    string
	singleVersion  string
	clusterImage   string
	clusterVersion string
	// suffixSingle and suffixCluster are required substrings of the respective version.
	suffixSingle  string
	suffixCluster string
}

// makefileScenarios lists the exact image/version values produced by the Makefile for each
// supported flag combination. Keep in sync with Makefile lines 20-68.
var makefileScenarios = []makefileVersionScenario{
	{
		name:           "default (no flags)",
		singleImage:    "quay.io/victoriametrics/victoria-metrics",
		singleVersion:  "v1.144.0",
		clusterImage:   "quay.io/victoriametrics/vmselect",
		clusterVersion: "v1.144.0-cluster",
		suffixSingle:   "",
		suffixCluster:  "-cluster",
	},
	{
		name:           "VM_ENTERPRISE=1",
		singleImage:    "quay.io/victoriametrics/victoria-metrics",
		singleVersion:  "v1.128.0-enterprise",
		clusterImage:   "quay.io/victoriametrics/vmselect",
		clusterVersion: "v1.128.0-enterprise-cluster",
		suffixSingle:   "-enterprise",
		suffixCluster:  "-enterprise-cluster",
	},
	{
		name:           "VM_RC=1",
		singleImage:    "quay.io/victoriametrics/victoria-metrics",
		singleVersion:  "v1.143.0-enterprise-cluster-rc0",
		clusterImage:   "quay.io/victoriametrics/vmselect",
		clusterVersion: "v1.143.0-cluster-rc0",
		suffixSingle:   "-rc",
		suffixCluster:  "-rc",
	},
	{
		name:           "VM_LTS_VERSION=current",
		singleImage:    "quay.io/victoriametrics/victoria-metrics",
		singleVersion:  "v1.136.9-enterprise",
		clusterImage:   "quay.io/victoriametrics/vmselect",
		clusterVersion: "v1.136.9-cluster-enterprise",
		suffixSingle:   "-enterprise",
		suffixCluster:  "-enterprise",
	},
	{
		name:           "VM_LTS_VERSION=previous",
		singleImage:    "quay.io/victoriametrics/victoria-metrics",
		singleVersion:  "v1.122.22-enterprise",
		clusterImage:   "quay.io/victoriametrics/vmselect",
		clusterVersion: "v1.122.22-cluster-enterprise",
		suffixSingle:   "-enterprise",
		suffixCluster:  "-enterprise",
	},
}

// TestMakefileFlagCombosProduceValidImages verifies that each Makefile flag combination
// (VM_RC, VM_ENTERPRISE, VM_LTS_VERSION=current/previous) produces non-empty,
// correctly-suffixed image and version strings for VMSingle and VMCluster components
// when passed through buildVMK8StackValues.
func TestMakefileFlagCombosProduceValidImages(t *testing.T) {
	defer func() {
		consts.SetVMSingleDefaultImage("")
		consts.SetVMSingleDefaultVersion("")
		consts.SetVMClusterVMSelectDefaultImage("")
		consts.SetVMClusterVMSelectDefaultVersion("")
		consts.SetVMClusterVMStorageDefaultImage("")
		consts.SetVMClusterVMStorageDefaultVersion("")
		consts.SetVMClusterVMInsertDefaultImage("")
		consts.SetVMClusterVMInsertDefaultVersion("")
	}()

	for _, s := range makefileScenarios {
		t.Run(s.name, func(t *testing.T) {
			// Reset between subtests.
			consts.SetVMSingleDefaultImage("")
			consts.SetVMSingleDefaultVersion("")
			consts.SetVMClusterVMSelectDefaultImage("")
			consts.SetVMClusterVMSelectDefaultVersion("")
			consts.SetVMClusterVMStorageDefaultImage("")
			consts.SetVMClusterVMStorageDefaultVersion("")
			consts.SetVMClusterVMInsertDefaultImage("")
			consts.SetVMClusterVMInsertDefaultVersion("")

			// Simulate what the Makefile passes via -vm-*-image / -vm-*-version flags.
			consts.SetVMSingleDefaultImage(s.singleImage)
			consts.SetVMSingleDefaultVersion(s.singleVersion)
			consts.SetVMClusterVMSelectDefaultImage(s.clusterImage)
			consts.SetVMClusterVMSelectDefaultVersion(s.clusterVersion)
			consts.SetVMClusterVMStorageDefaultImage(s.clusterImage)
			consts.SetVMClusterVMStorageDefaultVersion(s.clusterVersion)
			consts.SetVMClusterVMInsertDefaultImage(s.clusterImage)
			consts.SetVMClusterVMInsertDefaultVersion(s.clusterVersion)

			setValues := buildVMK8StackValues("test")

			// env indices: 0=VMSingleImage, 1=VMSingleVersion,
			//              2=VMSelectImage, 3=VMSelectVersion,
			//              4=VMStorageImage, 5=VMStorageVersion,
			//              6=VMInsertImage,  7=VMInsertVersion
			singleImg := setValues["victoria-metrics-operator.env[0].value"]
			singleVer := setValues["victoria-metrics-operator.env[1].value"]
			assert.NotEmpty(t, singleImg, "VMSingle image must not be empty")
			assert.NotEmpty(t, singleVer, "VMSingle version must not be empty")
			assert.Equal(t, s.singleImage, singleImg, "VMSingle image")
			assert.Equal(t, s.singleVersion, singleVer, "VMSingle version")
			if s.suffixSingle != "" {
				assert.Contains(t, singleVer, s.suffixSingle, "VMSingle version must contain %q", s.suffixSingle)
			}

			for _, c := range []struct {
				component string
				imgKey    string
				verKey    string
			}{
				{"VMSelect", "victoria-metrics-operator.env[2].value", "victoria-metrics-operator.env[3].value"},
				{"VMStorage", "victoria-metrics-operator.env[4].value", "victoria-metrics-operator.env[5].value"},
				{"VMInsert", "victoria-metrics-operator.env[6].value", "victoria-metrics-operator.env[7].value"},
			} {
				clImg := setValues[c.imgKey]
				clVer := setValues[c.verKey]
				assert.NotEmpty(t, clImg, "%s image must not be empty", c.component)
				assert.NotEmpty(t, clVer, "%s version must not be empty", c.component)
				assert.Equal(t, s.clusterImage, clImg, "%s image", c.component)
				assert.Equal(t, s.clusterVersion, clVer, "%s version", c.component)
				if s.suffixCluster != "" {
					assert.Contains(t, clVer, s.suffixCluster, "%s version must contain %q", c.component, s.suffixCluster)
				}
			}
		})
	}
}

func TestBuildVMDistributedValues(t *testing.T) {
	originalNginxHost := consts.NginxHost()
	defer func() {
		consts.SetNginxHost(originalNginxHost)
	}()

	consts.SetNginxHost("cluster.local")
	namespace := "distributed-test"
	setValues := buildVMDistributedValues(namespace)

	assert.Equal(t, fmt.Sprintf("vmselect-%s.cluster.local.nip.io", namespace), setValues["read.global.vmauth.spec.ingress.host"])
	assert.Equal(t, fmt.Sprintf("vminsert-%s.cluster.local.nip.io", namespace), setValues["write.global.vmauth.spec.ingress.host"])
	assert.Equal(t, "vmselect-{{ (.zone).name }}.cluster.local.nip.io", setValues["zoneTpl.read.vmauth.spec.ingress.host"])
}

func TestBuildVMDistributedSetFiles(t *testing.T) {
	originalLicenseFile := consts.LicenseFile()
	defer consts.SetLicenseFile(originalLicenseFile)

	consts.SetLicenseFile("")
	assert.Empty(t, buildVMDistributedSetFiles())

	consts.SetLicenseFile("/tmp/vm-license")
	assert.Equal(t, map[string]string{
		"global.license.key":                "/tmp/vm-license",
		"common.vmcluster.spec.license.key": "/tmp/vm-license",
		"common.vmsingle.spec.license.key":  "/tmp/vm-license",
	}, buildVMDistributedSetFiles())
}
