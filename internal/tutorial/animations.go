package tutorial

import (
	"fmt"
	"math"
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
)

// ParticleType defines different particle effects
type ParticleType int

const (
	ParticleSparkle ParticleType = iota
	ParticleStar
	ParticleConfetti
	ParticleFirework
	ParticleRain
	ParticleSnow
	ParticleGlow
)

// Particle represents an animated particle
type Particle struct {
	X, Y     float64
	VX, VY   float64
	Life     int
	MaxLife  int
	Type     ParticleType
	Char     string
	Color    string
	Size     int
	Gravity  float64
	Friction float64
}

func cyclicChoice(items []string, idx int) (string, bool) {
	if len(items) == 0 {
		return "", false
	}
	pos := idx % len(items)
	if pos < 0 {
		pos += len(items)
	}
	if pos < 0 || pos >= len(items) {
		return "", false
	}
	return items[pos], true
}

// Update advances the particle simulation
func (p *Particle) Update() {
	p.VY += p.Gravity
	p.VX *= (1 - p.Friction)
	p.X += p.VX
	p.Y += p.VY
	p.Life--
}

// Render returns the styled particle character
func (p Particle) Render() string {
	// Fade based on life
	alpha := float64(p.Life) / float64(p.MaxLife)
	if alpha < 0.3 {
		return ""
	}

	color := styles.ParseHex(p.Color)
	// Apply alpha to brightness
	color.R = int(float64(color.R) * alpha)
	color.G = int(float64(color.G) * alpha)
	color.B = int(float64(color.B) * alpha)

	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", color.R, color.G, color.B, p.Char)
}

// TypingAnimation creates a typing effect for text
type TypingAnimation struct {
	Lines       []string
	CurrentChar int
	Speed       int // ticks per character
	Cursor      string
	CursorBlink bool
	Done        bool
}

// RevealAnimation creates a line-by-line reveal effect
type RevealAnimation struct {
	Lines       []string
	CurrentLine int
	Speed       int
	RevealStyle string // "fade", "slide", "typewriter"
	Done        bool
}

// WaveText creates a wave animation effect on text
func WaveText(text string, tick int, amplitude float64, colors []string) string {
	runes := []rune(text)
	var result strings.Builder

	for i, r := range runes {
		if r == ' ' || r == '\n' {
			result.WriteRune(r)
			continue
		}

		// Calculate wave offset
		phase := float64(i)*0.3 + float64(tick)*0.15
		offset := math.Sin(phase) * amplitude

		// Calculate color based on position
		if len(colors) == 0 {
			continue
		}
		colorHex, ok := cyclicChoice(colors, i+tick/3)
		if !ok {
			continue
		}
		color := styles.ParseHex(colorHex)

		// Apply brightness based on wave
		brightness := 0.7 + 0.3*((offset+amplitude)/(amplitude*2))
		color.R = clamp(int(float64(color.R) * brightness))
		color.G = clamp(int(float64(color.G) * brightness))
		color.B = clamp(int(float64(color.B) * brightness))

		result.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c\x1b[0m", color.R, color.G, color.B, r))
	}

	return result.String()
}

// PulseText creates a pulsing brightness effect
func PulseText(text string, tick int, baseColor string) string {
	color := styles.ParseHex(baseColor)

	// Sine wave pulsing
	pulse := 0.6 + 0.4*math.Sin(float64(tick)*0.1)
	color.R = clamp(int(float64(color.R) * pulse))
	color.G = clamp(int(float64(color.G) * pulse))
	color.B = clamp(int(float64(color.B) * pulse))

	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%s\x1b[0m", color.R, color.G, color.B, text)
}

// ProgressDots creates animated progress dots
func ProgressDots(current, total int, tick int) string {
	var dots strings.Builder

	for i := 0; i < total; i++ {
		if i < current {
			// Completed dot with glow
			dots.WriteString(PulseText("●", tick+i*5, "#a6e3a1"))
		} else if i == current {
			// Current dot with animation
			dots.WriteString(PulseText("◉", tick, "#89b4fa"))
		} else {
			// Future dot
			dots.WriteString("\x1b[38;2;69;71;90m○\x1b[0m")
		}
		dots.WriteString(" ")
	}

	return dots.String()
}

// Helper functions

func clamp(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}

func visibleLength(s string) int {
	// Strip ANSI escape codes first
	var stripped strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		stripped.WriteRune(r)
	}
	// Use runewidth for proper emoji/wide character width calculation
	return runewidth.StringWidth(stripped.String())
}
