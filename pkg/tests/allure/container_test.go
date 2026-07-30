package allure

import (
	"testing"

	"github.com/onsi/ginkgo/v2/types"
)

func TestCreateFromReportSurfacesFailedBeforeSuite(t *testing.T) {
	report := types.Report{
		SuitePath:        "/tests/vl-functional_test",
		SuiteDescription: "VictoriaLogs Functional test Suite",
		SpecReports: types.SpecReports{
			{
				LeafNodeType: types.NodeTypeSynchronizedBeforeSuite,
				State:        types.SpecStateFailed,
				Failure: types.Failure{
					Message: "timed out waiting for VMAgent in namespace vmgather to become operational",
				},
			},
		},
	}

	c := newTestContainer().createFromReport(report)

	if len(c.Befores) == 0 {
		t.Fatalf("expected at least one before-suite step, got none")
	}

	last := c.Befores[len(c.Befores)-1]
	if last.Status != failed {
		t.Fatalf("expected last before step status %q, got %q", failed, last.Status)
	}
	if last.StatusDetails == nil || last.StatusDetails.Message == "" {
		t.Fatalf("expected status details with a message, got %#v", last.StatusDetails)
	}
}

func TestCreateFromReportKeepsPassedBeforeSuiteSilent(t *testing.T) {
	report := types.Report{
		SpecReports: types.SpecReports{
			{
				LeafNodeType: types.NodeTypeSynchronizedBeforeSuite,
				State:        types.SpecStatePassed,
			},
		},
	}

	c := newTestContainer().createFromReport(report)

	for _, step := range c.Befores {
		if step.Status == failed || step.Status == broken {
			t.Fatalf("did not expect a failure step for a passed BeforeSuite, got %#v", step)
		}
	}
}
