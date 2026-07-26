package types

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrRunUnsupportedWrapping(t *testing.T) {
	err := fmt.Errorf("%w: detail", ErrRunNotFound)
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatal("errors.Is should match wrapped run not found")
	}
}
