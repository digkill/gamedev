package gamespec

import (
	"fmt"
	"math"
)

// Validate reports what keeps the spec from being a finished game. It runs on a
// normalised spec, so it only looks for missing substance, never for malformed
// numbers.
//
// Fatal defects are the ones that make the level unplayable or unwinnable. The
// pipeline sends them back to the developer agent; if the agent cannot fix
// them within the repair budget, Repair applies a deterministic fallback.
func Validate(spec Spec) []Defect {
	var defects []Defect
	if len(spec.Levels) == 0 {
		return []Defect{{Code: "NO_LEVELS", Message: "В игре нет ни одного уровня", Fatal: true}}
	}
	for i, level := range spec.Levels {
		number := i + 1
		solid := len(level.Platforms)
		if solid == 0 {
			defects = append(defects, Defect{
				Code: "FLOOR_MISSING", Level: number, Fatal: true,
				Message: fmt.Sprintf("Уровень %d: нет ни одной платформы, персонажу не на чем стоять", number),
			})
		}
		objects := solid + len(level.Hazards) + len(level.Collectibles) + len(level.Enemies)
		if level.Goal != nil {
			objects++
		}
		if objects < 4 {
			defects = append(defects, Defect{
				Code: "LEVEL_TOO_SPARSE", Level: number, Fatal: true,
				Message: fmt.Sprintf("Уровень %d почти пустой: %d объектов. Нужны препятствия, предметы или противники", number, objects),
			})
		}
		switch level.Win {
		case "reach_goal":
			if level.Goal == nil {
				defects = append(defects, Defect{
					Code: "GOAL_MISSING", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: условие победы reach_goal, но финиша нет", number),
				})
			}
		case "collect_all":
			if len(level.Collectibles) == 0 {
				defects = append(defects, Defect{
					Code: "COLLECTIBLES_MISSING", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: условие победы collect_all, но собирать нечего", number),
				})
			}
		case "defeat_all":
			if len(level.Enemies) == 0 {
				defects = append(defects, Defect{
					Code: "ENEMIES_MISSING", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: условие победы defeat_all, но противников нет", number),
				})
			}
			if !anyEnemyKillable(level.Enemies) {
				defects = append(defects, Defect{
					Code: "ENEMIES_IMMORTAL", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: у противников не задано здоровье, победить их нельзя", number),
				})
			}
		case "survive_time":
			if len(level.Enemies) == 0 && len(level.Hazards) == 0 {
				defects = append(defects, Defect{
					Code: "SURVIVAL_WITHOUT_THREAT", Level: number, Fatal: true,
					Message: fmt.Sprintf("Уровень %d: выживание без единой угрозы — игрок просто ждёт таймер", number),
				})
			}
		}
		if level.Lose == "health_zero" && len(level.Enemies) == 0 && len(level.Hazards) == 0 {
			defects = append(defects, Defect{
				Code: "LOSE_UNREACHABLE", Level: number,
				Message: fmt.Sprintf("Уровень %d: проиграть невозможно, нет ни противников, ни опасностей", number),
			})
		}
		if level.Goal != nil && distance(level.Spawn, level.Goal.Position) < 3 {
			defects = append(defects, Defect{
				Code: "GOAL_TOO_CLOSE", Level: number,
				Message: fmt.Sprintf("Уровень %d: финиш стоит вплотную к точке старта", number),
			})
		}
		if blocked, name := spawnBlocked(level); blocked {
			defects = append(defects, Defect{
				Code: "SPAWN_BLOCKED", Level: number,
				Message: fmt.Sprintf("Уровень %d: точка старта внутри объекта «%s»", number, name),
			})
		}
		if !flatGravity(spec.Genre) && !hasGroundBelow(level, level.Spawn) {
			defects = append(defects, Defect{
				Code: "SPAWN_IN_AIR", Level: number,
				Message: fmt.Sprintf("Уровень %d: под точкой старта нет платформы", number),
			})
		}
		defects = append(defects, reachabilityDefects(spec, level, number)...)
		for _, pickup := range level.Collectibles {
			if !flatGravity(spec.Genre) && !hasGroundBelow(level, pickup.Position) {
				defects = append(defects, Defect{
					Code: "PICKUP_UNREACHABLE", Level: number,
					Message: fmt.Sprintf("Уровень %d: предмет «%s» висит без платформы под ним", number, pickup.Name),
				})
				break
			}
		}
	}
	return defects
}

// Fatal filters the defects that block publication.
func Fatal(defects []Defect) []Defect {
	var out []Defect
	for _, defect := range defects {
		if defect.Fatal {
			out = append(out, defect)
		}
	}
	return out
}

// Repair is the deterministic fallback. When the developer agent has spent its
// attempts and fatal defects remain, this builds the missing pieces so the user
// still receives a game that starts, can be won, and can be lost.
func Repair(spec Spec) Spec {
	dims := dimensions(spec.Mode)
	for i := range spec.Levels {
		level := &spec.Levels[i]
		if len(level.Platforms) == 0 {
			level.Platforms = append(level.Platforms, groundBox(spec, level.Spawn, dims))
		}
		if !flatGravity(spec.Genre) && !hasGroundBelow(*level, level.Spawn) {
			level.Platforms = append(level.Platforms, groundBox(spec, level.Spawn, dims))
		}
		extent := levelExtent(*level, dims)
		if level.Win == "reach_goal" && level.Goal == nil {
			position := append([]float64(nil), level.Spawn...)
			position[0] = extent.max[0] + 2
			if dims == 2 {
				position[1] = groundHeight(*level, position[0]) + 1.5
			} else {
				position[1] = groundHeight(*level, position[0]) + 1.5
			}
			level.Goal = &Box{
				Name: "Финиш", Position: position,
				Size:  sizeFor(dims, 1.2, 2.4),
				Color: spec.Palette.Goal, Shape: normalizeShape("", spec.Mode),
			}
		}
		if level.Win == "collect_all" && len(level.Collectibles) == 0 {
			for step := 1; step <= 3; step++ {
				position := append([]float64(nil), level.Spawn...)
				position[0] = level.Spawn[0] + float64(step)*3
				position[1] = groundHeight(*level, position[0]) + 1.5
				level.Collectibles = append(level.Collectibles, Pickup{
					Name: "Монета " + itoa(step), Position: position, Kind: "score", Value: 1,
				})
			}
		}
		if level.Win == "defeat_all" || level.Win == "survive_time" {
			if len(level.Enemies) == 0 {
				level.Enemies = append(level.Enemies, patrolEnemy(spec, *level, dims))
			}
			if level.Win == "defeat_all" && !anyEnemyKillable(level.Enemies) {
				for j := range level.Enemies {
					level.Enemies[j].Health = 1
				}
			}
		}
		if level.Lose == "health_zero" && len(level.Enemies) == 0 && len(level.Hazards) == 0 {
			level.Enemies = append(level.Enemies, patrolEnemy(spec, *level, dims))
		}
		repairReachability(spec, level)
		// A level that is still thin gets filler platforms rather than being
		// shipped as an empty box.
		for attempts := 0; attempts < 4 && countObjects(*level) < 4; attempts++ {
			position := append([]float64(nil), level.Spawn...)
			position[0] = level.Spawn[0] + float64(attempts+1)*4
			position[1] = level.Spawn[1] + 1 + float64(attempts)
			level.Platforms = append(level.Platforms, Box{
				Name: "Уступ " + itoa(attempts+1), Position: position,
				Size:  sizeFor(dims, 3, 0.6),
				Color: spec.Palette.Platform, Shape: normalizeShape("", spec.Mode),
			})
		}
	}
	return Normalize(spec)
}

func countObjects(level Level) int {
	n := len(level.Platforms) + len(level.Hazards) + len(level.Collectibles) + len(level.Enemies)
	if level.Goal != nil {
		n++
	}
	return n
}

func anyEnemyKillable(enemies []Enemy) bool {
	for _, enemy := range enemies {
		if enemy.Health > 0 {
			return true
		}
	}
	return false
}

func groundBox(spec Spec, spawn []float64, dims int) Box {
	position := append([]float64(nil), spawn...)
	position[1] = spawn[1] - 2
	size := sizeFor(dims, 24, 1)
	if dims == 3 {
		size = []float64{24, 1, 24}
	}
	return Box{
		Name: "Земля", Position: position, Size: size,
		Color: spec.Palette.Platform, Shape: normalizeShape("", spec.Mode),
	}
}

func patrolEnemy(spec Spec, level Level, dims int) Enemy {
	position := append([]float64(nil), level.Spawn...)
	position[0] += 6
	position[1] = groundHeight(level, position[0]) + 1
	left := append([]float64(nil), position...)
	right := append([]float64(nil), position...)
	left[0] -= 3
	right[0] += 3
	return Enemy{
		Name: "Патрульный", Position: position, Size: defaultPlayerSize(spec.Mode),
		Color: spec.Palette.Enemy, Behavior: "patrol", Patrol: [][]float64{left, right},
		Speed: 2, Damage: 1, Health: 1,
	}
}

func sizeFor(dims int, width, height float64) []float64 {
	if dims == 3 {
		return []float64{width, height, width}
	}
	return []float64{width, height}
}

type extent struct {
	min []float64
	max []float64
}

// levelExtent is the bounding box of everything placed in the level, including
// the spawn point.
func levelExtent(level Level, dims int) extent {
	min := append([]float64(nil), level.Spawn...)
	max := append([]float64(nil), level.Spawn...)
	include := func(position, size []float64) {
		for i := 0; i < dims && i < len(position); i++ {
			half := 0.5
			if size != nil && i < len(size) {
				half = size[i] / 2
			}
			min[i] = math.Min(min[i], position[i]-half)
			max[i] = math.Max(max[i], position[i]+half)
		}
	}
	for _, box := range level.Platforms {
		include(box.Position, box.Size)
	}
	for _, box := range level.Hazards {
		include(box.Position, box.Size)
	}
	for _, box := range level.Decor {
		include(box.Position, box.Size)
	}
	for _, pickup := range level.Collectibles {
		include(pickup.Position, nil)
	}
	for _, enemy := range level.Enemies {
		include(enemy.Position, enemy.Size)
		for _, point := range enemy.Patrol {
			include(point, nil)
		}
	}
	if level.Goal != nil {
		include(level.Goal.Position, level.Goal.Size)
	}
	return extent{min: min, max: max}
}

// groundHeight returns the top of the highest platform under x, or the lowest
// point of the level when there is nothing there.
func groundHeight(level Level, x float64) float64 {
	best := math.Inf(-1)
	for _, box := range level.Platforms {
		if len(box.Position) < 2 || len(box.Size) < 2 {
			continue
		}
		half := box.Size[0] / 2
		if x < box.Position[0]-half || x > box.Position[0]+half {
			continue
		}
		top := box.Position[1] + box.Size[1]/2
		if top > best {
			best = top
		}
	}
	if math.IsInf(best, -1) {
		return 0
	}
	return best
}

func hasGroundBelow(level Level, position []float64) bool {
	if len(position) < 2 {
		return false
	}
	for _, box := range level.Platforms {
		if len(box.Position) < 2 || len(box.Size) < 2 {
			continue
		}
		half := box.Size[0] / 2
		if position[0] < box.Position[0]-half-0.5 || position[0] > box.Position[0]+half+0.5 {
			continue
		}
		top := box.Position[1] + box.Size[1]/2
		if top <= position[1]+0.5 {
			return true
		}
	}
	return false
}

func spawnBlocked(level Level) (bool, string) {
	for _, box := range level.Platforms {
		if inside(level.Spawn, box) {
			return true, box.Name
		}
	}
	for _, box := range level.Hazards {
		if inside(level.Spawn, box) {
			return true, box.Name
		}
	}
	return false, ""
}

func inside(point []float64, box Box) bool {
	if len(point) == 0 || len(box.Position) == 0 {
		return false
	}
	for i := range point {
		if i >= len(box.Position) || i >= len(box.Size) {
			return false
		}
		half := box.Size[i] / 2
		if point[i] < box.Position[i]-half || point[i] > box.Position[i]+half {
			return false
		}
	}
	return true
}

func distance(a, b []float64) float64 {
	sum := 0.0
	for i := range a {
		if i >= len(b) {
			break
		}
		delta := a[i] - b[i]
		sum += delta * delta
	}
	return math.Sqrt(sum)
}
