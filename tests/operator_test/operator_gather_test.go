package operator_test

import (
	"context"
	"errors"
	"testing"
)

func TestRestoreAndGatherOnFailure(t *testing.T) {
	restoreErr := errors.New("restore failed")
	for _, tc := range []struct {
		name       string
		restoreErr error
		wantGather bool
	}{
		{name: "restore failed", restoreErr: restoreErr, wantGather: true},
		{name: "restore succeeded", wantGather: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gathered := false
			err := restoreAndGatherOnFailure(context.Background(), func() error {
				return tc.restoreErr
			}, func(context.Context) {
				gathered = true
			})
			if !errors.Is(err, tc.restoreErr) {
				t.Fatalf("restore error = %v, want %v", err, tc.restoreErr)
			}
			if gathered != tc.wantGather {
				t.Fatalf("gathered = %t, want %t", gathered, tc.wantGather)
			}
		})
	}
}
