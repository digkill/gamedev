package gamespec

import (
	"math"
	"strconv"
	"strings"
)

func clampText(value string, max int, fallback string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return fallback
	}
	runes := []rune(value)
	if len(runes) > max {
		value = strings.TrimSpace(string(runes[:max]))
	}
	return value
}

// clampNumber treats zero as "not specified": every quantity here is a speed,
// a duration, or a size where zero is either meaningless or the caller applies
// it explicitly after normalisation.
func clampNumber(value, low, high, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value == 0 {
		value = fallback
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = low
	}
	return math.Max(low, math.Min(high, value))
}

func clampInt(value, low, high, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// clampVector returns a vector of exactly n finite components. A shorter vector
// is padded from the fallback, a longer one is cut.
func clampVector(values []float64, n int, low, high float64, fallback []float64) []float64 {
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		var value float64
		switch {
		case i < len(values):
			value = values[i]
		case i < len(fallback):
			value = fallback[i]
		}
		if math.IsNaN(value) || math.IsInf(value, 0) {
			if i < len(fallback) {
				value = fallback[i]
			} else {
				value = 0
			}
		}
		out[i] = math.Max(low, math.Min(high, value))
	}
	return out
}

func zeroes(n int) []float64 { return make([]float64, n) }

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

func sameVector(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

// normalizeColor accepts #RGB, #RRGGBB, and #RRGGBBAA and always returns the
// nine-character form the project validator requires.
func normalizeColor(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if !strings.HasPrefix(value, "#") {
		value = "#" + value
	}
	digits := strings.ToUpper(value[1:])
	for _, r := range digits {
		if !strings.ContainsRune("0123456789ABCDEF", r) {
			return fallback
		}
	}
	switch len(digits) {
	case 3:
		var b strings.Builder
		for _, r := range digits {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		return "#" + b.String() + "FF"
	case 6:
		return "#" + digits + "FF"
	case 8:
		return "#" + digits
	default:
		return fallback
	}
}

func normalizeShape(shape, mode string) string {
	shape = strings.ToLower(strings.TrimSpace(shape))
	if mode == "3d" {
		switch shape {
		case "box", "sphere", "capsule", "plane":
			return shape
		}
		return "box"
	}
	switch shape {
	case "rectangle", "circle":
		return shape
	}
	return "rectangle"
}

// colliderShape maps a visual shape to the collision shape, which has no plane.
func colliderShape(shape, mode string) string {
	if mode == "3d" {
		if shape == "sphere" || shape == "capsule" {
			return shape
		}
		return "box"
	}
	if shape == "circle" {
		return "circle"
	}
	return "rectangle"
}

func itoa(n int) string { return strconv.Itoa(n) }

func round(value float64, places int) float64 {
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}
