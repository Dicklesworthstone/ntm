// Package components provides shared TUI building blocks.
package components

import (
	"math"

	"github.com/charmbracelet/harmonica"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
)

const (
	defaultSpringStiffness = 6.0
	defaultSpringDamping   = 0.4
	springSettledEpsilon   = 0.001
)

// AnimatedFloat stores the state for a single spring-animated value.
type AnimatedFloat struct {
	Value, Velocity, Target float64
	Spring                  harmonica.Spring
	DeadZone                float64
	animating               bool
	stiffness, damping      float64 // creation params, used to avoid unnecessary recreation
}

// SpringManager tracks keyed animated values for Bubble Tea components.
// Bubble Tea updates run on a single goroutine, so no mutex is required.
type SpringManager struct {
	springs map[string]*AnimatedFloat
}

// NewSpringManager creates an empty spring manager.
func NewSpringManager() *SpringManager {
	return &SpringManager{
		springs: make(map[string]*AnimatedFloat),
	}
}

// Has reports whether the manager has state for the given spring ID.
func (sm *SpringManager) Has(id string) bool {
	if sm == nil || id == "" {
		return false
	}
	_, ok := sm.springs[id]
	return ok
}

// SetWithParams updates a spring target with explicit spring tuning.
func (sm *SpringManager) SetWithParams(id string, target, stiffness, damping float64) {
	sm.SetWithDeadZone(id, target, stiffness, damping, 0)
}

// SetWithDeadZone updates a spring target and suppresses tiny changes.
func (sm *SpringManager) SetWithDeadZone(id string, target, stiffness, damping, deadZone float64) {
	if sm == nil || id == "" {
		return
	}

	spring := sm.ensure(id, stiffness, damping)
	if deadZone < 0 {
		deadZone = 0
	}
	spring.DeadZone = deadZone

	if styles.ReducedMotionEnabled() {
		sm.SetImmediate(id, target)
		return
	}

	if math.Abs(target-spring.Value) <= spring.DeadZone {
		spring.Value = target
		spring.Target = target
		spring.Velocity = 0
		spring.animating = false
		return
	}

	spring.Target = target
	spring.animating = true
}

// SetImmediate snaps a spring to a value without animation.
func (sm *SpringManager) SetImmediate(id string, value float64) {
	if sm == nil || id == "" {
		return
	}

	spring := sm.ensure(id, defaultSpringStiffness, defaultSpringDamping)
	spring.Value = value
	spring.Target = value
	spring.Velocity = 0
	spring.animating = false
}

// Tick advances all active springs by one frame.
func (sm *SpringManager) Tick() {
	if sm == nil || styles.ReducedMotionEnabled() {
		return
	}

	for _, spring := range sm.springs {
		if spring == nil || !spring.animating {
			continue
		}

		spring.Value, spring.Velocity = spring.Spring.Update(spring.Value, spring.Velocity, spring.Target)
		if isSpringSettled(spring) {
			spring.Value = spring.Target
			spring.Velocity = 0
			spring.animating = false
		}
	}
}

// Get returns the current value for the given spring.
func (sm *SpringManager) Get(id string) float64 {
	if sm == nil || id == "" {
		return 0
	}
	if spring, ok := sm.springs[id]; ok && spring != nil {
		return spring.Value
	}
	return 0
}

// IsAnimating reports whether any spring is currently moving.
func (sm *SpringManager) IsAnimating() bool {
	if sm == nil || styles.ReducedMotionEnabled() {
		return false
	}
	for _, spring := range sm.springs {
		if spring != nil && spring.animating {
			return true
		}
	}
	return false
}

func (sm *SpringManager) ensure(id string, stiffness, damping float64) *AnimatedFloat {
	if spring, ok := sm.springs[id]; ok && spring != nil {
		// Only recreate the spring physics if params actually changed
		if stiffness > 0 && damping > 0 && (stiffness != spring.stiffness || damping != spring.damping) {
			spring.Spring = harmonica.NewSpring(harmonica.FPS(60), stiffness, damping)
			spring.stiffness = stiffness
			spring.damping = damping
		}
		return spring
	}

	if stiffness <= 0 {
		stiffness = defaultSpringStiffness
	}
	if damping <= 0 {
		damping = defaultSpringDamping
	}

	spring := &AnimatedFloat{
		Spring:    harmonica.NewSpring(harmonica.FPS(60), stiffness, damping),
		stiffness: stiffness,
		damping:   damping,
	}
	sm.springs[id] = spring
	return spring
}

func isSpringSettled(spring *AnimatedFloat) bool {
	if spring == nil {
		return true
	}
	deadZone := spring.DeadZone * 0.1
	if deadZone < springSettledEpsilon {
		deadZone = springSettledEpsilon
	}
	return math.Abs(spring.Target-spring.Value) <= deadZone && math.Abs(spring.Velocity) <= springSettledEpsilon
}
