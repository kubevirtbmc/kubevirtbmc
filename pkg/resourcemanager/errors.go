package resourcemanager

import (
	"fmt"

	goipmihandlers "github.com/bougou/go-ipmi/pkg/handlers"
)

// ErrRetryable marks a KubeVirt operation that failed only due to a
// transitional VM state (e.g. VMI still cleaning up) and may succeed on
// retry. Protocol layers detect it via errors.As: go-ipmi extracts
// CodeNodeBusy (0xC0) through As, Redfish maps it to an HTTP iLO response.
type ErrRetryable struct{ Err error }

func (e *ErrRetryable) Error() string {
	return fmt.Sprintf("retryable: %v", e.Err)
}

func (e *ErrRetryable) Unwrap() error {
	return e.Err
}

// As exposes [goipmihandlers.CodeNodeBusy] (0xC0) to errors.As so go-ipmi's
// chassis handler reports "node busy, retry later" (IPMI spec §5.2 Table 5-2).
func (e *ErrRetryable) As(target any) bool {
	if cc, ok := target.(*goipmihandlers.CompletionCode); ok {
		*cc = goipmihandlers.CodeNodeBusy
		return true
	}
	return false
}
