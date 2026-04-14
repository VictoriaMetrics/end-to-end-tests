package tests

import (
	"flag"
	"os"
	"path/filepath"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

var (
	manifestsDir           string
	reportLocation         string
	envK8SDistro           string
	operatorRegistry       string
	operatorRepository     string
	operatorTag            string
	vmSingleDefaultImage   string
	vmSingleDefaultVersion string

	vmClusterVMSelectDefaultImage   string
	vmClusterVMSelectDefaultVersion string

	vmClusterVMStorageDefaultImage   string
	vmClusterVMStorageDefaultVersion string

	vmClusterVMInsertDefaultImage   string
	vmClusterVMInsertDefaultVersion string

	vmAgentDefaultImage   string
	vmAgentDefaultVersion string

	vmAlertDefaultImage   string
	vmAlertDefaultVersion string

	vmAuthDefaultImage   string
	vmAuthDefaultVersion string

	vmBackupDefaultImage   string
	vmBackupDefaultVersion string

	vmRestoreDefaultImage   string
	vmRestoreDefaultVersion string
	licenseFile             string
	distributedRegion       string
	distributedZones        string
)

func init() {
	flag.StringVar(&manifestsDir, "manifests-dir", "", "Base directory for manifest files (overrides default relative path)")
	flag.StringVar(&reportLocation, "report", "/tmp/allure-results", "Report location")
	flag.StringVar(&envK8SDistro, "env-k8s-distro", "kind", "Kube distro name")
	flag.StringVar(&operatorRegistry, "operator-registry", "", "Operator image registry")
	flag.StringVar(&operatorRepository, "operator-repository", "", "Operator image repository")
	flag.StringVar(&operatorTag, "operator-tag", "", "Operator image tag")
	flag.StringVar(&vmSingleDefaultImage, "vm-vmsingledefault-image", os.Getenv("VM_VMSINGLEDEFAULT_IMAGE"), "Default image for VMSingle")
	flag.StringVar(&vmSingleDefaultVersion, "vm-vmsingledefault-version", os.Getenv("VM_VMSINGLEDEFAULT_VERSION"), "Default version for VMSingle")

	flag.StringVar(&vmClusterVMSelectDefaultImage, "vm-vmclusterdefault-vmselectdefault-image", os.Getenv("VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_IMAGE"), "Default image for VMCluster VMSelect")
	flag.StringVar(&vmClusterVMSelectDefaultVersion, "vm-vmclusterdefault-vmselectdefault-version", os.Getenv("VM_VMCLUSTERDEFAULT_VMSELECTDEFAULT_VERSION"), "Default version for VMCluster VMSelect")

	flag.StringVar(&vmClusterVMStorageDefaultImage, "vm-vmclusterdefault-vmstoragedefault-image", os.Getenv("VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_IMAGE"), "Default image for VMCluster VMStorage")
	flag.StringVar(&vmClusterVMStorageDefaultVersion, "vm-vmclusterdefault-vmstoragedefault-version", os.Getenv("VM_VMCLUSTERDEFAULT_VMSTORAGEDEFAULT_VERSION"), "Default version for VMCluster VMStorage")

	flag.StringVar(&vmClusterVMInsertDefaultImage, "vm-vmclusterdefault-vminsertdefault-image", os.Getenv("VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_IMAGE"), "Default image for VMCluster VMInsert")
	flag.StringVar(&vmClusterVMInsertDefaultVersion, "vm-vmclusterdefault-vminsertdefault-version", os.Getenv("VM_VMCLUSTERDEFAULT_VMINSERTDEFAULT_VERSION"), "Default version for VMCluster VMInsert")

	flag.StringVar(&vmAgentDefaultImage, "vm-vmagentdefault-image", os.Getenv("VM_VMAGENTDEFAULT_IMAGE"), "Default image for VMAgent")
	flag.StringVar(&vmAgentDefaultVersion, "vm-vmagentdefault-version", os.Getenv("VM_VMAGENTDEFAULT_VERSION"), "Default version for VMAgent")

	flag.StringVar(&vmAlertDefaultImage, "vm-vmalertdefault-image", os.Getenv("VM_VMALERTDEFAULT_IMAGE"), "Default image for VMAlert")
	flag.StringVar(&vmAlertDefaultVersion, "vm-vmalertdefault-version", os.Getenv("VM_VMALERTDEFAULT_VERSION"), "Default version for VMAlert")

	flag.StringVar(&vmAuthDefaultImage, "vm-vmauthdefault-image", os.Getenv("VM_VMAUTHDEFAULT_IMAGE"), "Default image for VMAuth")
	flag.StringVar(&vmAuthDefaultVersion, "vm-vmauthdefault-version", os.Getenv("VM_VMAUTHDEFAULT_VERSION"), "Default version for VMAuth")

	flag.StringVar(&vmBackupDefaultImage, "vm-vmbackupdefault-image", os.Getenv("VM_VMBACKUPDEFAULT_IMAGE"), "Default image for VMBackup")
	flag.StringVar(&vmBackupDefaultVersion, "vm-vmbackupdefault-version", os.Getenv("VM_VMBACKUPDEFAULT_VERSION"), "Default version for VMBackup")

	flag.StringVar(&vmRestoreDefaultImage, "vm-vmrestoredefault-image", os.Getenv("VM_VMRESTOREDEFAULT_IMAGE"), "Default image for VMRestore")
	flag.StringVar(&vmRestoreDefaultVersion, "vm-vmrestoredefault-version", os.Getenv("VM_VMRESTOREDEFAULT_VERSION"), "Default version for VMRestore")
	flag.StringVar(&licenseFile, "license-file", "", "Path to license file")
	flag.StringVar(&distributedRegion, "distributed-region", "europe-central2", "Region for distributed tests")
	flag.StringVar(&distributedZones, "distributed-zones", "europe-central2-a,europe-central2-b,europe-central2-c", "Zones for distributed tests")
}

// Init initializes test configuration by parsing flags and setting up constants.
func Init() {
	if !flag.Parsed() {
		flag.Parse()
	}
	if manifestsDir != "" {
		consts.SetManifestsDir(manifestsDir)
	}
	absReportLocation, err := filepath.Abs(reportLocation)
	if err != nil {
		panic(err)
	}
	consts.SetReportLocation(absReportLocation)
	if err := os.Setenv("ALLURE_RESULTS_PATH", absReportLocation); err != nil {
		panic(err)
	}

	consts.SetEnvK8SDistro(envK8SDistro)
	consts.SetOperatorImageRegistry(operatorRegistry)
	consts.SetOperatorImageRepository(operatorRepository)
	consts.SetOperatorImageTag(operatorTag)
	consts.SetVMSingleDefaultImage(vmSingleDefaultImage)
	consts.SetVMSingleDefaultVersion(vmSingleDefaultVersion)

	consts.SetVMClusterVMSelectDefaultImage(vmClusterVMSelectDefaultImage)
	consts.SetVMClusterVMSelectDefaultVersion(vmClusterVMSelectDefaultVersion)

	consts.SetVMClusterVMStorageDefaultImage(vmClusterVMStorageDefaultImage)
	consts.SetVMClusterVMStorageDefaultVersion(vmClusterVMStorageDefaultVersion)

	consts.SetVMClusterVMInsertDefaultImage(vmClusterVMInsertDefaultImage)
	consts.SetVMClusterVMInsertDefaultVersion(vmClusterVMInsertDefaultVersion)

	consts.SetVMAgentDefaultImage(vmAgentDefaultImage)
	consts.SetVMAgentDefaultVersion(vmAgentDefaultVersion)

	consts.SetVMAlertDefaultImage(vmAlertDefaultImage)
	consts.SetVMAlertDefaultVersion(vmAlertDefaultVersion)

	consts.SetVMAuthDefaultImage(vmAuthDefaultImage)
	consts.SetVMAuthDefaultVersion(vmAuthDefaultVersion)

	consts.SetVMBackupDefaultImage(vmBackupDefaultImage)
	consts.SetVMBackupDefaultVersion(vmBackupDefaultVersion)

	consts.SetVMRestoreDefaultImage(vmRestoreDefaultImage)
	consts.SetVMRestoreDefaultVersion(vmRestoreDefaultVersion)

	consts.SetLicenseFile(licenseFile)
	consts.SetDistributedRegion(distributedRegion)
	consts.SetDistributedZones(distributedZones)
}
