package ai

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/digkill/gamedev/backend/internal/projects"
)

var (
	ErrUnavailable = projects.ErrAIUnavailable
	ErrFailed      = projects.ErrAIFailed
	ErrProvider    = projects.ErrAIProvider
)

type Request struct {
	Prompt   string
	Mode     string
	Context  json.RawMessage
	Manifest json.RawMessage
}

type Result struct {
	Summary    string
	Operations []projects.Command
	Model      string
}

type Provider interface {
	Configured() bool
	Generate(ctx context.Context, req Request) (Result, error)
}

// Generator turns one prompt into a validated command set. It is provider
// agnostic: the model is reached through a Chat, so the same edit path works
// with OpenAI directly or with kie.ai in front of Claude.
type Generator struct{ chat Chat }

// New keeps the direct OpenAI path for the single-shot edit endpoint.
func New(apiKey, model string) Provider {
	if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(model) == "" {
		return Unavailable{}
	}
	return Generator{chat: newOpenAIChat(apiKey, model)}
}

// FromChat wraps any configured Chat, which is how the kie.ai client becomes
// the provider for the edit endpoint.
func FromChat(chat Chat) Provider {
	if chat == nil || !chat.Configured() {
		return Unavailable{}
	}
	return Generator{chat: chat}
}

type Unavailable struct{}

func (Unavailable) Configured() bool { return false }

func (Unavailable) Generate(context.Context, Request) (Result, error) {
	return Result{}, ErrUnavailable
}

func (g Generator) Configured() bool { return g.chat != nil && g.chat.Configured() }

func (g Generator) Generate(ctx context.Context, req Request) (Result, error) {
	if !g.Configured() {
		return Result{}, ErrUnavailable
	}
	if utf8.RuneCountInString(strings.TrimSpace(req.Prompt)) < 1 {
		return Result{}, projects.ErrInvalid
	}
	sceneID, _ := sceneState(req.Manifest)
	result, err := g.call(ctx, req, "")
	if err == nil {
		result.Operations = sanitizeOps(req.Mode, req.Manifest, result.Operations)
		if applyError(req.Mode, req.Manifest, result.Operations) == "" && len(result.Operations) > 0 {
			return result, nil
		}
		repaired, retryErr := g.call(ctx, req, "Команды отклонены. Верни только CreateEntity/SetProperty/SetProjectSetting без лишних полей. scene_id="+sceneID+". Цвет #RRGGBBAA. У CreateEntity нет entity_id.")
		if retryErr == nil {
			repaired.Operations = sanitizeOps(req.Mode, req.Manifest, repaired.Operations)
			if applyError(req.Mode, req.Manifest, repaired.Operations) == "" && len(repaired.Operations) > 0 {
				return repaired, nil
			}
		}
	}
	fallback := fallbackOps(req.Mode, sceneID, req.Prompt)
	if applyError(req.Mode, req.Manifest, fallback) == "" && len(fallback) > 0 {
		return Result{Summary: "Собрана базовая сцена", Operations: fallback, Model: g.model()}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return Result{}, ErrFailed
}

func (g Generator) model() string {
	if named, ok := g.chat.(interface{ PrimaryModel() string }); ok {
		return named.PrimaryModel()
	}
	return ""
}

func (g Generator) call(ctx context.Context, req Request, previousError string) (Result, error) {
	response, err := g.chat.Chat(ctx, ChatRequest{
		System: systemPrompt,
		User:   userInput(req, previousError),
		JSON:   true,
	})
	if err != nil {
		return Result{}, err
	}
	ops, summary, err := parseProposal(response.Text)
	if err != nil {
		return Result{}, err
	}
	return Result{Summary: summary, Operations: ops, Model: response.Model}, nil
}

func userInput(req Request, previousError string) string {
	var b strings.Builder
	b.WriteString("Ответь JSON объектом с полями summary и operations.\n")
	b.WriteString("Режим проекта: ")
	b.WriteString(req.Mode)
	b.WriteString("\nСцена:\n")
	b.Write(req.Context)
	b.WriteString("\n\nЗапрос пользователя:\n")
	b.WriteString(req.Prompt)
	if previousError != "" {
		b.WriteString("\n\nПредыдущий набор команд отклонён валидатором. Исправь только команды. Ошибка: ")
		b.WriteString(previousError)
	}
	return b.String()
}

func parseProposal(text string) ([]projects.Command, string, error) {
	raw, ok := ExtractJSON(text)
	if !ok {
		return nil, "", ErrFailed
	}
	var payload struct {
		Summary    string             `json:"summary"`
		Operations []projects.Command `json:"operations"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil, "", ErrFailed
	}
	if len(payload.Operations) < 1 || len(payload.Operations) > 100 {
		return nil, "", projects.ErrInvalid
	}
	ops := make([]projects.Command, 0, len(payload.Operations))
	for _, op := range payload.Operations {
		op = projects.NormalizeCommand(op)
		if !projects.IsUUID(op.OperationID) {
			id, err := projects.NewUUID()
			if err != nil {
				return nil, "", ErrFailed
			}
			op.OperationID = id
		}
		ops = append(ops, op)
	}
	summary := strings.TrimSpace(payload.Summary)
	if summary == "" {
		summary = "Изменения сцены"
	}
	if utf8.RuneCountInString(summary) > 400 {
		summary = string([]rune(summary)[:400])
	}
	return ops, summary, nil
}

const systemPrompt = `Ты серверный конструктор AI Sandbox. Отвечай только JSON {"summary":"...","operations":[...]}.
Разрешены type: CreateEntity, SetProperty, SetProjectSetting, SetComponent, DeleteEntity.
CreateEntity содержит только operation_id, type, scene_id и entity. Поля entity_id, scene, component, property, value, setting в CreateEntity быть не должно.
scene_id бери из scene.id контекста, не выдумывай.
entity: {"id":"<uuid>","name":"Hero","parent_id":null,"components":[{"type":"Transform","space":"2d","position":[0,1],"rotation":0,"scale":[1,1]},{"type":"Sprite","shape":"rectangle","tint":"#FFDD55FF","layer":0}]}.
2D Transform.space="2d", rotation число, position/scale длины 2. 3D Transform.space="3d", rotation [x,y,z], Mesh primitive box|sphere|capsule|plane, material_color #RRGGBBAA.
Цвет строго 9 символов #RRGGBBAA. Без скриптов, ассетов, графов, префабов и лишних ключей JSON. Не больше 100 операций.`
