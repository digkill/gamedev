package gamespec

import (
	"fmt"
	"math"
	"sort"
)

// Reachability turns the player's jump into an actual constraint on the
// geometry. A gap wider than the jump, or a ledge higher than the jump, makes
// the level unfinishable, and neither is visible to a schema check — it only
// falls out of the physics the runtime will use.

type span struct {
	left  float64
	right float64
	top   float64
}

// jumpReach returns the height the player can climb and the distance the
// player can clear, from the standard projectile equations for a jump that
// starts and lands at the same height.
func jumpReach(spec Spec, level Level) (height, distance float64) {
	gravity := defaultGravity(spec)
	if level.Gravity != nil {
		gravity = *level.Gravity
	}
	if gravity >= 0 {
		return math.Inf(1), math.Inf(1)
	}
	g := math.Abs(gravity)
	v := spec.Player.JumpSpeed
	if spec.Player.DoubleJump {
		// A second jump at the apex adds most of another arc.
		v *= 1.35
	}
	height = v * v / (2 * g)
	airtime := 2 * v / g
	distance = spec.Player.MoveSpeed * airtime
	return height, distance
}

func platformSpans(level Level) []span {
	spans := make([]span, 0, len(level.Platforms))
	for _, box := range level.Platforms {
		if len(box.Position) < 2 || len(box.Size) < 2 {
			continue
		}
		spans = append(spans, span{
			left:  box.Position[0] - box.Size[0]/2,
			right: box.Position[0] + box.Size[0]/2,
			top:   box.Position[1] + box.Size[1]/2,
		})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].left < spans[j].left })
	return spans
}

// reachabilityDefects walks the platforms left to right and reports the first
// gap or ledge the player cannot pass.
func reachabilityDefects(spec Spec, level Level, number int) []Defect {
	if spec.Mode != "2d" || flatGravity(spec.Genre) {
		return nil
	}
	spans := platformSpans(level)
	if len(spans) < 2 {
		return nil
	}
	height, distance := jumpReach(spec, level)
	if math.IsInf(distance, 1) {
		return nil
	}
	var defects []Defect
	for i := 1; i < len(spans); i++ {
		previous, current := spans[i-1], spans[i]
		if current.left <= previous.right {
			// The platforms overlap horizontally; only the step matters.
			if current.top-previous.top > height-0.3 {
				defects = append(defects, Defect{
					Code: "LEDGE_TOO_HIGH", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: подъём %.1f выше прыжка %.1f", number, current.top-previous.top, height),
				})
			}
			continue
		}
		gap := current.left - previous.right
		if gap > distance*0.9 {
			defects = append(defects, Defect{
				Code: "GAP_TOO_WIDE", Level: number, Fatal: true,
				Message: fmt.Sprintf("Уровень %d: разрыв %.1f шире прыжка %.1f", number, gap, distance*0.9),
			})
		}
		if current.top-previous.top > height-0.3 {
			defects = append(defects, Defect{
				Code: "LEDGE_TOO_HIGH", Level: number, Fatal: true,
				Message: fmt.Sprintf("Уровень %d: подъём %.1f выше прыжка %.1f", number, current.top-previous.top, height),
			})
		}
	}
	return defects
}

// repairReachability drops stepping platforms into gaps the player cannot
// clear, and under ledges that are too high.
func repairReachability(spec Spec, level *Level) {
	if spec.Mode != "2d" || flatGravity(spec.Genre) {
		return
	}
	height, distance := jumpReach(spec, *level)
	if math.IsInf(distance, 1) {
		return
	}
	for guard := 0; guard < 12; guard++ {
		spans := platformSpans(*level)
		if len(spans) < 2 {
			return
		}
		added := false
		for i := 1; i < len(spans); i++ {
			previous, current := spans[i-1], spans[i]
			gap := current.left - previous.right
			step := current.top - previous.top
			switch {
			case gap > distance*0.9:
				width := math.Min(3, math.Max(1.2, gap/3))
				centre := (previous.right + current.left) / 2
				top := math.Min(previous.top, current.top)
				level.Platforms = append(level.Platforms, Box{
					Name:     "Ступень",
					Position: []float64{round(centre, 3), round(top-0.4, 3)},
					Size:     []float64{round(width, 3), 0.8},
					Color:    spec.Palette.Platform,
					Shape:    "rectangle",
				})
				added = true
			case step > height-0.3:
				centre := current.left - 1.5
				if gap > 0 {
					centre = (previous.right + current.left) / 2
				}
				level.Platforms = append(level.Platforms, Box{
					Name:     "Ступень",
					Position: []float64{round(centre, 3), round(previous.top+(height-0.6)/2, 3)},
					Size:     []float64{2.4, 0.6},
					Color:    spec.Palette.Platform,
					Shape:    "rectangle",
				})
				added = true
			}
			if added {
				break
			}
		}
		if !added {
			return
		}
	}
}
