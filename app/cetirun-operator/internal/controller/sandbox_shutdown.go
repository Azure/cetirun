// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controller

// shutdownPendingError marks an observed, ordinary shutdown wait. Missing
// evidence, ownership conflicts and failed API or revocation operations must
// remain ordinary errors.
type shutdownPendingError string

func (e shutdownPendingError) Error() string { return string(e) }

// All causes must be progress waits. errors.As alone would also match a wait
// joined with a real failure, hiding the failed cleanup or status write.
func onlyShutdownPending(err error) bool {
	switch e := err.(type) {
	case shutdownPendingError:
		return true
	case interface{ Unwrap() []error }:
		causes := e.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyShutdownPending(cause) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return onlyShutdownPending(e.Unwrap())
	default:
		return false
	}
}
