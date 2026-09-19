package gamespec

import "math"

// Template builds a complete, hand-tuned game without a model. The pipeline
// falls back to it when the developer agent cannot produce a usable spec, so a
// provider outage degrades the result instead of failing the request. The
// geometry is derived from the player's own jump, which means the levels are
// crossable by construction.
func Template(title, mode, genre string, levels int) Spec {
	spec := Normalize(Spec{Title: title, Mode: mode, Genre: genre})
	if levels < 1 {
		levels = 2
	}
	if levels > 4 {
		levels = 4
	}
	spec.Levels = make([]Level, 0, levels)
	for i := 0; i < levels; i++ {
		spec.Levels = append(spec.Levels, templateLevel(spec, i, levels))
	}
	return Repair(Normalize(spec))
}

func templateLevel(spec Spec, index, total int) Level {
	dims := dimensions(spec.Mode)
	level := Level{
		Name:  "Уровень " + itoa(index+1),
		Spawn: defaultSpawn(spec.Mode),
		Win:   "reach_goal",
		Lose:  "health_zero",
	}
	switch {
	case index == 0:
		level.Hint = "Двигайся вправо, собирай монеты и дойди до финиша"
	case index == total-1:
		level.Hint = "Последний рывок: не задень противников"
	default:
		level.Hint = "Дальше опаснее — рассчитывай прыжок"
	}
	if flatGravity(spec.Genre) {
		return flatTemplateLevel(spec, level, index, dims)
	}

	// Steps get longer and higher on later levels, but never beyond the jump.
	height, distance := jumpReach(spec, level)
	if math.IsInf(distance, 1) {
		height, distance = 3, 6
	}
	step := math.Min(distance*0.55, 5)
	rise := math.Min(height*0.5, 1.6)
	difficulty := float64(index)

	x := 0.0
	top := 0.0
	level.Platforms = append(level.Platforms, Box{
		Name: "Старт", Position: []float64{0, -1}, Size: []float64{8, 1},
	})
	segments := 4 + index
	for segment := 1; segment <= segments; segment++ {
		x += step + float64(segment%2)*0.5
		if segment%2 == 0 {
			top += rise
		}
		width := math.Max(2.5, 5-difficulty*0.6)
		level.Platforms = append(level.Platforms, Box{
			Name:     "Уступ " + itoa(segment),
			Position: []float64{round(x, 2), round(top-0.5, 2)},
			Size:     []float64{round(width, 2), 0.8},
		})
		level.Collectibles = append(level.Collectibles, Pickup{
			Name: "Монета " + itoa(segment), Kind: "score", Value: 1,
			Position: []float64{round(x, 2), round(top+1.2, 2)},
		})
		if segment%2 == 1 && segment > 1 {
			level.Hazards = append(level.Hazards, Box{
				Name:     "Шипы " + itoa(segment),
				Position: []float64{round(x-step/2, 2), round(top-1.6, 2)},
				Size:     []float64{round(step*0.5, 2), 0.5},
				Damage:   1,
			})
		}
	}
	patrolCentre := x * 0.6
	level.Enemies = append(level.Enemies, Enemy{
		Name: "Патрульный", Behavior: "patrol", Speed: 2 + difficulty*0.5, Damage: 1, Health: 1,
		Position: []float64{round(patrolCentre, 2), round(groundHeight(level, patrolCentre)+1, 2)},
		Patrol: [][]float64{
			{round(patrolCentre-2.5, 2), round(groundHeight(level, patrolCentre)+1, 2)},
			{round(patrolCentre+2.5, 2), round(groundHeight(level, patrolCentre)+1, 2)},
		},
	})
	if index > 0 {
		chaseX := x * 0.85
		level.Enemies = append(level.Enemies, Enemy{
			Name: "Преследователь", Behavior: "chase", Speed: 2.5, Damage: 1, Health: 1,
			Position: []float64{round(chaseX, 2), round(groundHeight(level, chaseX)+1, 2)},
		})
	}
	goalX := x + step*0.8
	level.Platforms = append(level.Platforms, Box{
		Name: "Площадка финиша", Position: []float64{round(goalX, 2), round(top-0.5, 2)}, Size: []float64{4, 0.8},
	})
	level.Goal = &Box{
		Name: "Финиш", Position: []float64{round(goalX, 2), round(top+1.2, 2)}, Size: []float64{1.2, 2.4},
	}
	return level
}

// flatTemplateLevel lays out an arena or top-down room: a floor, walls of
// obstacles, pickups spread around, and enemies that come to the player.
func flatTemplateLevel(spec Spec, level Level, index, dims int) Level {
	size := 18.0 + float64(index)*4
	level.Platforms = append(level.Platforms, Box{
		Name: "Пол", Position: zeroes(dims), Size: sizeFor(dims, size, 1),
	})
	for i := 0; i < 4+index; i++ {
		angle := float64(i) * 2 * math.Pi / float64(4+index)
		radius := size / 3
		position := zeroes(dims)
		position[0] = round(math.Cos(angle)*radius, 2)
		position[1] = round(math.Sin(angle)*radius, 2)
		if dims == 3 {
			position[1] = 1
			position[2] = round(math.Sin(angle)*radius, 2)
		}
		level.Platforms = append(level.Platforms, Box{
			Name: "Укрытие " + itoa(i+1), Position: position, Size: sizeFor(dims, 2, 2),
		})
		pickup := append([]float64(nil), position...)
		pickup[0] += 2
		level.Collectibles = append(level.Collectibles, Pickup{
			Name: "Кристалл " + itoa(i+1), Position: pickup, Kind: "score", Value: 1,
		})
	}
	if spec.Genre == "arena" {
		level.Win, level.Lose, level.Seconds = "survive_time", "health_zero", 45+float64(index)*15
		level.Enemies = append(level.Enemies, Enemy{
			Name: "Волна", Behavior: "spawned", Speed: 2.5, Damage: 1, Health: 1,
			Count: 8 + index*4, IntervalSeconds: 3,
			Position: sizeFor(dims, 0, 0),
		})
		level.Hint = "Держись " + itoa(int(level.Seconds)) + " секунд"
		return level
	}
	level.Win, level.Lose = "collect_all", "health_zero"
	level.Enemies = append(level.Enemies, Enemy{
		Name: "Страж", Behavior: "chase", Speed: 2, Damage: 1, Health: 1,
		Position: sizeFor(dims, 4, 4),
	})
	level.Hint = "Собери все кристаллы и не попадись стражу"
	return level
}
