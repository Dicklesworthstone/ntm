package ratelimit

import "sync"

var (
	defaultAdmissionOnce sync.Once
	defaultAdmission     *AdmissionController
)

// DefaultAdmissionController is the process-wide provider admission boundary.
// State remains isolated by the complete provider identity tuple; callers must
// not create a fresh controller per request because that would reset tokens,
// backoff, and circuit state.
func DefaultAdmissionController() *AdmissionController {
	defaultAdmissionOnce.Do(func() {
		controller, err := NewAdmissionController(DefaultAdmissionConfig(), nil, nil)
		if err != nil {
			panic(err)
		}
		defaultAdmission = controller
	})
	return defaultAdmission
}
