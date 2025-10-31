// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package retry

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
)

// When adjusting backoffs in retry.go, this can help calculate the steps,
// duration and exponential backoff factor required to get within a desired
// retry duration.
func TestBackoff(t *testing.T) {
	tests := []struct {
		name            string
		backoff         wait.Backoff
		wantCnt         int
		wantMinDuration time.Duration
		wantMaxDuration time.Duration
	}{
		{
			name:            "update",
			backoff:         WriteBackoff,
			wantCnt:         10,
			wantMinDuration: 95 * time.Millisecond,
			wantMaxDuration: 180 * time.Millisecond,
		},
		// Leaving this disabled as it will never fail and just adds to test
		// time.
		//
		// {
		// 	name:            "read",
		// 	backoff:         ReadRetry,
		// 	wantCnt:         20,
		// 	wantMinDuration: 15 * time.Second,
		// 	wantMaxDuration: 20 * time.Second,
		// },
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := 0
			now := time.Now()
			f := func() error {
				i++
				fmt.Printf("i: %s: %s\n", time.Now().String(), tt.name)
				return errors.New("test")
			}

			_ = retry.OnError(tt.backoff, IsRetriableError, f)

			if i != tt.wantCnt {
				t.Errorf("got: %d, want: %d", i, tt.wantCnt)
			}

			if time.Since(now) < tt.wantMinDuration {
				t.Errorf("got: %v, want: %v", time.Since(now), tt.wantMinDuration)
			}

			if time.Since(now) > tt.wantMaxDuration {
				t.Errorf("got: %v, want: %v", time.Since(now), tt.wantMaxDuration)
			}
		})
	}
}
