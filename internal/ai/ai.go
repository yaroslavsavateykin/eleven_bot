package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"group411/internal/category"
	"group411/internal/schedule"
)

type Service struct {
	BaseURL, Key, Model string
	Client              *http.Client
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type parsedEvent struct {
	Operation         string   `json:"operation"`
	Kind              string   `json:"kind"`
	Category          string   `json:"category"`
	Title             string   `json:"title"`
	Description       *string  `json:"description"`
	Location          *string  `json:"location"`
	StartLocal        string   `json:"start_local"`
	EndLocal          *string  `json:"end_local"`
	Timezone          string   `json:"timezone"`
	AllDay            bool     `json:"all_day"`
	RRule             *string  `json:"rrule"`
	Tags              []string `json:"tags"`
	Confidence        float64  `json:"confidence"`
	NeedsConfirmation bool     `json:"needs_confirmation"`
}

func (s Service) Complete(ctx context.Context, system, prompt string) (string, error) {
	return s.complete(ctx, system, prompt, 6000, 300, false)
}

func (s Service) complete(ctx context.Context, system, prompt string, maxPrompt, tokens int, structured bool) (string, error) {
	if s.Key == "" || s.Model == "" {
		return "", fmt.Errorf("AI is not configured")
	}
	if len(prompt) > maxPrompt {
		return "", fmt.Errorf("AI prompt too long")
	}
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	payload := map[string]any{"model": s.Model, "messages": []message{{Role: "system", Content: system}, {Role: "user", Content: prompt}}, "temperature": 0.4, "max_tokens": tokens}
	if structured {
		payload["response_format"] = map[string]string{"type": "json_object"}
		payload["temperature"] = 0
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("AI request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("AI returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(data, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("invalid AI response")
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("empty AI response")
	}
	return text, nil
}

// ParseEvent asks the model for structured data, then validates every field before persistence.
func (s Service) ParseEvent(ctx context.Context, text, groupName string, now time.Time, loc *time.Location) (schedule.Event, bool, error) {
	system := `Ты парсер событий для учебной группы. Верни только JSON без Markdown: {"operation":"create|none","kind":"lesson|deadline|event|note|other","title":"...","description":null,"location":null,"start_local":"YYYY-MM-DDTHH:MM:SS|null","end_local":null,"timezone":"Europe/Moscow","all_day":false,"rrule":null,"tags":["..."],"confidence":0.0,"needs_confirmation":false}. Понимай русский естественный язык: завтра, послезавтра, следующий вторник, каждый вторник, раз в две недели, до даты. Если время или дата отсутствуют, неоднозначны или выражены как «вторая пара» без известного расписания пар, верни needs_confirmation=true. Не выдумывай даты, время, аудитории и длительность. Для повторов укажи RFC5545 RRULE.`
	system += ` Добавь поле category: lesson (обычная пара), event (необычное мероприятие), test (КР, контрольная), quiz (проверочная, квиз), exam (экзамен), deadline (срок сдачи), other (прочее). Категория независима от kind: контрольная на паре имеет kind=lesson, category=test. Не добавляй priority.`
	raw, err := s.Complete(ctx, system, fmt.Sprintf("Группа: %s. Сейчас: %s. Timezone: %s. Сообщение: %s", groupName, now.In(loc).Format(time.RFC3339), loc, text))
	if err != nil {
		return schedule.Event{}, false, err
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	var p parsedEvent
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return schedule.Event{}, false, fmt.Errorf("AI returned invalid event JSON: %w", err)
	}
	if p.Operation != "create" {
		return schedule.Event{}, false, fmt.Errorf("событие в сообщении не найдено")
	}
	if p.NeedsConfirmation || p.Confidence < 0.75 {
		return schedule.Event{}, true, fmt.Errorf("нужны точные дата и время")
	}
	if p.Title == "" || p.StartLocal == "" {
		return schedule.Event{}, false, fmt.Errorf("AI не указал название или время")
	}
	if p.Timezone != "" && p.Timezone != loc.String() {
		return schedule.Event{}, false, fmt.Errorf("AI вернул неподдерживаемую timezone")
	}
	start, err := time.ParseInLocation("2006-01-02T15:04:05", p.StartLocal, loc)
	if err != nil {
		return schedule.Event{}, false, fmt.Errorf("некорректное время события: %w", err)
	}
	if start.Before(now.In(loc).Add(-5 * time.Minute)) {
		return schedule.Event{}, false, fmt.Errorf("время события уже прошло")
	}
	e := schedule.Event{Kind: p.Kind, Title: strings.TrimSpace(p.Title), Description: p.Description, Location: p.Location, StartsAt: start, Timezone: loc.String(), AllDay: p.AllDay, RRule: p.RRule, Tags: p.Tags}
	if e.Kind == "" {
		e.Kind = "event"
	}
	e.Category, err = category.Resolve(p.Category, e.Kind, e.Title)
	if err != nil {
		return e, false, err
	}
	if p.EndLocal != nil {
		end, err := time.ParseInLocation("2006-01-02T15:04:05", *p.EndLocal, loc)
		if err != nil {
			return e, false, fmt.Errorf("некорректное время окончания: %w", err)
		}
		if !end.After(start) {
			return e, false, fmt.Errorf("окончание должно быть после начала")
		}
		e.EndsAt = &end
	}
	return e, false, nil
}
