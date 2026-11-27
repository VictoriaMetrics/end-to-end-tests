package operator_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRestoreAndGatherOnFailure(t *testing.T) {
	restoreErr := errors.New("restore failed")
	for _, tc := range []struct {
		name       string
		restoreErr error
		wantGather []string
	}{
		{name: "restore failed", restoreErr: restoreErr, wantGather: []string{"VL", "crust-gather"}},
		{name: "restore succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gathered []string
			err := restoreAndGatherOnFailure(context.Background(), func() error {
				return tc.restoreErr
			}, func(context.Context) {
				gathered = append(gathered, "VL")
			}, func(context.Context) {
				gathered = append(gathered, "crust-gather")
			})
			if !errors.Is(err, tc.restoreErr) {
				t.Fatalf("restore error = %v, want %v", err, tc.restoreErr)
			}
			if !reflect.DeepEqual(gathered, tc.wantGather) {
				t.Fatalf("gathered = %v, want %v", gathered, tc.wantGather)
			}
		})
	}
}
