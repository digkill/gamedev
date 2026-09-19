package agents

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/digkill/gamedev/backend/internal/ai"
	"github.com/digkill/gamedev/backend/internal/gamespec"
	"github.com/digkill/gamedev/backend/internal/projects"
)

func section(title, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return "\n=== " + title + " ===\n" + body + "\n"
}

func formatDefects(defects []gamespec.Defect) string {
	if len(defects) == 0 {
		return "Дефектов не найдено."
	}
	var b strings.Builder
	for _, defect := range defects {
		mark := "  "
		if defect.Fatal {
			mark = "! "
		}
		fmt.Fprintf(&b, "%s[%s] уровень %d: %s\n", mark, defect.Code, defect.Level, defect.Message)
	}
	return b.String()
}

func nonFatal(defects []gamespec.Defect) []gamespec.Defect {
	var out []gamespec.Defect
	for _, defect := range defects {
		if !defect.Fatal {
			out = append(out, defect)
		}
	}
	return out
}

func verdictFor(defects []gamespec.Defect) string {
	if len(gamespec.Fatal(defects)) > 0 {
		return "fail"
	}
	return "pass"
}

// compactSpec gives a reviewer the shape of the level without the full
// coordinate dump, which would crowd out everything else in the prompt.
func compactSpec(spec gamespec.Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s/%s, уровней: %d\n", spec.Title, spec.Mode, spec.Genre, len(spec.Levels))
	fmt.Fprintf(&b, "игрок: скорость %.1f, прыжок %.1f, здоровье %d\n",
		spec.Player.MoveSpeed, spec.Player.JumpSpeed, spec.Player.Health)
	if spec.Summary != "" {
		b.WriteString(spec.Summary + "\n")
	}
	for i, level := range spec.Levels {
		fmt.Fprintf(&b, "уровень %d «%s»: платформ %d, опасностей %d, предметов %d, противников %d, финиш %v, победа %s, поражение %s",
			i+1, level.Name, len(level.Platforms), len(level.Hazards), len(level.Collectibles),
			len(level.Enemies), level.Goal != nil, level.Win, level.Lose)
		if level.Seconds > 0 {
			fmt.Fprintf(&b, ", время %.0f с", level.Seconds)
		}
		b.WriteString("\n")
		for _, enemy := range level.Enemies {
			fmt.Fprintf(&b, "    противник «%s»: %s, скорость %.1f, урон %d\n",
				enemy.Name, enemy.Behavior, enemy.Speed, enemy.Damage)
		}
	}
	// The full spec follows so the tester can check actual coordinates.
	if raw, err := json.Marshal(spec); err == nil && len(raw) < 24000 {
		b.WriteString("\nПолная спецификация:\n")
		b.Write(raw)
		b.WriteString("\n")
	}
	return b.String()
}

func statsLine(compiled gamespec.Compiled) string {
	s := compiled.Stats
	return fmt.Sprintf("сцен %d, объектов %d, платформ %d, опасностей %d, предметов %d, противников %d, финишей %d, элементов HUD %d",
		s.Scenes, s.Entities, s.Platforms, s.Hazards, s.Collectibles, s.Enemies, s.Goals, s.UIWidgets)
}

// bannedWords is the deterministic half of the audit. The model is asked about
// policy too, but a match here rejects the job regardless of what it says.
var bannedWords = []string{
	"porn", "порно", "nsfw", "хентай", "hentai", "эротик", "erotic",
	"обнаж", "nude", "изнасил", "rape", "педофил", "pedophil",
}

func policyViolations(spec gamespec.Spec) []string {
	var found []string
	check := func(where, text string) {
		lower := strings.ToLower(text)
		for _, word := range bannedWords {
			if strings.Contains(lower, word) {
				found = append(found, where+": запрещённое содержимое")
				return
			}
		}
	}
	check("название", spec.Title)
	check("описание", spec.Summary)
	check("игрок", spec.Player.Name)
	for _, level := range spec.Levels {
		check("уровень «"+level.Name+"»", level.Name+" "+level.Hint)
		for _, enemy := range level.Enemies {
			check("противник", enemy.Name)
		}
		for _, pickup := range level.Collectibles {
			check("предмет", pickup.Name)
		}
	}
	return found
}

type policyError struct{ reasons []string }

func (e *policyError) Error() string {
	return "policy rejected: " + strings.Join(e.reasons, "; ")
}

// classify maps an internal error to the code and the Russian message the API
// returns. Provider detail never reaches the client.
func classify(err error) (string, string) {
	var policy *policyError
	switch {
	case errors.Is(err, ErrCancelled):
		return "CANCELLED", "Генерация отменена"
	case errors.As(err, &policy):
		return "POLICY_REJECTED", "Запрос нарушает правила платформы: " + strings.Join(policy.reasons, "; ")
	case errors.Is(err, ai.ErrUnavailable):
		return "AI_UNAVAILABLE", "Модель не настроена на сервере"
	case errors.Is(err, ai.ErrProvider):
		return "AI_PROVIDER", "Провайдер модели недоступен, попробуйте позже"
	case errors.Is(err, ai.ErrFailed):
		return "AI_FAILED", "Модель вернула неприменимый результат"
	case errors.Is(err, projects.ErrInvalid):
		return "INVALID_PROJECT", "Собранный проект не прошёл проверку"
	case errors.Is(err, projects.ErrConflict):
		return "REVISION_CONFLICT", "Проект изменился во время генерации"
	default:
		return "INTERNAL", "Внутренняя ошибка генерации"
	}
}

func requestHash(value string) [32]byte { return sha256.Sum256([]byte(value)) }

func firstWords(text string, n int) string {
	fields := strings.Fields(text)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

func pickSummary(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
