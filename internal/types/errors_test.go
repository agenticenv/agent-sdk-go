package types

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrTemporalDialTimeoutWrapping(t *testing.T) {
	err := fmt.Errorf("%w: detail", ErrTemporalDialTimeout)
	if !errors.Is(err, ErrTemporalDialTimeout) {
		t.Fatal("errors.Is should match wrapped dial timeout")
	}
}

func TestErrTemporalNamespaceCheckTimeoutWrapping(t *testing.T) {
	err := fmt.Errorf("%w: detail", ErrTemporalNamespaceCheckTimeout)
	if !errors.Is(err, ErrTemporalNamespaceCheckTimeout) {
		t.Fatal("errors.Is should match wrapped namespace timeout")
	}
}
