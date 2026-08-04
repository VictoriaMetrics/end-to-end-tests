package gather

import (
	"errors"
	"testing"
)

func TestIsRetriableVMGatherExportErrorMatchesExportFailure(t *testing.T) {
	if !isRetriableVMGatherExportError(errVMGatherExportFailed) {
		t.Fatal("expected errVMGatherExportFailed to be retriable")
	}
}

func TestIsRetriableVMGatherExportErrorRejectsOtherErrors(t *testing.T) {
	if isRetriableVMGatherExportError(errors.New("boom")) {
		t.Fatal("expected unrelated error to not be retriable")
	}
}
