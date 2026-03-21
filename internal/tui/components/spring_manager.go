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

// Set updates a spring target using the default spring parameters.
func (sm *SpringManager) Set(id string, target float64) {
	sm.SetWithDeadZone(id, target, defaultSpringStiffness, defaultSpringDamping, 0)
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

// IsSettled reports whether a specific spring has reached its target.
func (sm *SpringManager) IsSettled(id string) bool {
	if sm == nil || id == "" {
		return true
	}
	spring, ok := sm.springs[id]
	if !ok || spring == nil {
		return true
	}
	return !spring.animating && isSpringSettled(spring)
}

func (sm *SpringManager) ensure(id string, stiffness, damping float64) *AnimatedFloat {
	if spring, ok := sm.springs[id]; ok && spring != nil {
		if stiffness > 0 && damping > 0 {
			spring.Spring = harmonica.NewSpring(harmonica.FPS(60), stiffness, damping)
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
		Spring: harmonica.NewSpring(harmonica.FPS(60), stiffness, damping),
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

// SetDimension animates a 2D dimension (width, height) using two coordinated springs.
// [tui-upgrade: bd-3xm0o] - Panel dimension animation for focus transitions
func (sm *SpringManager) SetDimension(id string, width, height int) {
	sm.Set(id+":w", float64(width))
	sm.Set(id+":h", float64(height))
}

// SetDimensionImmediate sets a 2D dimension without animation.
func (sm *SpringManager) SetDimensionImmediate(id string, width, height int) {
	sm.SetImmediate(id+":w", float64(width))
	sm.SetImmediate(id+":h", float64(height))
}

// GetDimension returns the current animated width and height.
// [tui-upgrade: bd-3xm0o]
func (sm *SpringManager) GetDimension(id string) (width, height int) {
	if sm == nil || id == "" {
		return 0, 0
	}
	w := sm.Get(id + ":w")
	h := sm.Get(id + ":h")
	return int(math.Round(w)), int(math.Round(h))
}

// GetSmoothed returns the current value with optional smoothing applied.
// For values that jump around, this can provide visual stability.
// [tui-upgrade: bd-3xm0o]
func (sm *SpringManager) GetSmoothed(id string, smoothingFactor float64) float64 {
	if sm == nil || id == "" {
		return 0
	}
	value := sm.Get(id)
	if smoothingFactor <= 0 || smoothingFactor >= 1 {
		return value
	}
	// Exponential smoothing: smooth = smooth * factor + value * (1 - factor)
	// The spring already provides smoothing, so this just returns the current value
	// with an optional bias toward the target for more responsive feel.
	if spring, ok := sm.springs[id]; ok && spring != nil {
		// Blend current value toward target based on smoothing factor
		return value*(1-smoothingFactor) + spring.Target*smoothingFactor
	}
	return value
}

// SetScrollOffset animates a scroll position with easing.
// [tui-upgrade: bd-3xm0o] - Scroll easing for viewport panels
func (sm *SpringManager) SetScrollOffset(id string, offset int) {
	// Use softer spring parameters for scroll easing
	sm.SetWithParams(id+":scroll", float64(offset), 8.0, 0.5)
}

// GetScrollOffset returns the current animated scroll position.
func (sm *SpringManager) GetScrollOffset(id string) int {
	if sm == nil || id == "" {
		return 0
	}
	return int(math.Round(sm.Get(id + ":scroll")))
}
