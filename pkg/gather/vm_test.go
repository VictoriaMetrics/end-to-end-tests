package gather

import (
	"errors"
	"testing"
	"time"
)

func TestRetryVMGatherExportRetriesFailedAttempt(t *testing.T) {
	attempts := 0
	err := retryVMGatherExport(3, time.Nanosecond, func() error {
		attempts++
		if attempts == 1 {
			return errVMGatherExportFailed
		}
		return nil
	})

	if err != nil {
		t.Fatalf("expected retry to succeed, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryVMGatherExportDoesNotRetryOtherErrors(t *testing.T) {
	wantErr := errors.New("boom")
	attempts := 0
	err := retryVMGatherExport(3, time.Nanosecond, func() error {
		attempts++
		return wantErr
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestMaxDurationReturnsLargestDuration(t *testing.T) {
	if got := maxDuration(5*time.Minute, 15*time.Minute); got != 15*time.Minute {
		t.Fatalf("expected 15m, got %s", got)
	}
	if got := maxDuration(20*time.Minute, 15*time.Minute); got != 20*time.Minute {
		t.Fatalf("expected 20m, got %s", got)
	}
}
