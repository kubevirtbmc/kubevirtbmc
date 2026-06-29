package resourcemanager

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	goipmihandlers "github.com/bougou/go-ipmi/pkg/handlers"
)

// TestErrRetryableAsCompletionCode asserts errors.As extracts CodeNodeBusy
// (0xC0) from ErrRetryable, directly and through %w wrapping.
func TestErrRetryableAsCompletionCode(t *testing.T) {
	original := &ErrRetryable{Err: fmt.Errorf("VM is not running")}
	var cc goipmihandlers.CompletionCode
	assert.True(t, errors.As(original, &cc),
		"errors.As should extract CompletionCode from ErrRetryable")
	assert.Equal(t, goipmihandlers.CodeNodeBusy, cc,
		"ErrRetryable should expose CodeNodeBusy (0xC0)")

	wrapped := fmt.Errorf("chassis control failed: %w", original)
	var cc2 goipmihandlers.CompletionCode
	assert.True(t, errors.As(wrapped, &cc2),
		"errors.As should extract CompletionCode through error wrapping")
	assert.Equal(t, goipmihandlers.CodeNodeBusy, cc2)

	var retryable *ErrRetryable
	assert.True(t, errors.As(wrapped, &retryable))
	assert.Equal(t, "retryable: VM is not running", retryable.Error())

	assert.Equal(t, "VM is not running", original.Unwrap().Error())
}

// TestErrRetryableAsOnlyMatchesCompletionCode asserts that ErrRetryable.As
// declines non-CompletionCode targets so errors.As falls back to type matching.
func TestErrRetryableAsOnlyMatchesCompletionCode(t *testing.T) {
	original := &ErrRetryable{Err: fmt.Errorf("test")}

	var nonMatching interface{ Retryable() }
	assert.False(t, errors.As(original, &nonMatching),
		"errors.As should not match unrelated target types")
}
