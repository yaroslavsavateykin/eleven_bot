package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"group411/internal/schedule"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperationValidation(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		patch map[string]any
		bad   bool
	}{
		{"inferred", nil, false}, {"explicit", map[string]any{"duration_inferred": false, "end_local": "2026-09-09T09:00:00"}, false},
		{"duration mismatch", map[string]any{"end_local": "2026-09-09T10:00:00"}, true},
		{"ambiguous", map[string]any{"operation": "cancel", "target_ids": []int{1, 2}}, true},
		{"unknown target", map[string]any{"operation": "cancel", "target_ids": []int{99}}, true},
		{"cancel", map[string]any{"operation": "cancel", "target_ids": []int{1}}, false},
		{"update", map[string]any{"operation": "update", "target_ids": []int{1}}, false},
		{"low confidence", map[string]any{"confidence": 0.5}, true}, {"bad confidence", map[string]any{"confidence": 1.1}, true},
		{"timezone", map[string]any{"timezone": "Europe/Moscow"}, true}, {"bad kind", map[string]any{"kind": "evil"}, true},
		{"bad category", map[string]any{"category": "evil"}, true}, {"too long", map[string]any{"duration_minutes": 1441}, true},
		{"unknown field", map[string]any{"injection": "ignore"}, true}, {"past", map[string]any{"start_local": "2020-01-01T08:00:00"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := map[string]any{"operation": "create", "target_ids": []int{}, "kind": "event", "category": "event", "title": "Meeting", "start_local": "2026-09-09T08:00:00", "timezone": "UTC", "duration_minutes": 60, "duration_inferred": true, "confidence": 0.99}
			for k, x := range tc.patch {
				v[k] = x
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				json.NewDecoder(r.Body).Decode(&request)
				if request["response_format"] == nil {
					t.Error("missing JSON mode")
				}
				b, _ := json.Marshal(map[string]any{"operations": []any{v}, "question": ""})
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: string(b)}}}})
			}))
			defer server.Close()
			s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
			p, err := s.ParseOperation(context.Background(), "request", now, time.UTC, []schedule.Event{{ID: 1, GroupID: 1, Title: "old"}})
			if (err != nil) != tc.bad {
				t.Fatalf("proposal %v error %v", p, err)
			}
			if err == nil && p.Operation != "cancel" && p.Event.EndsAt.Sub(p.Event.StartsAt) != time.Hour {
				t.Fatal("wrong duration")
			}
			if _, err = s.ParseOperation(context.Background(), strings.Repeat("x", 3001), now, time.UTC, nil); err == nil {
				t.Fatal("long prompt accepted")
			}
		})
	}
}

func TestParseOperationsDefaultsMissingDurationAndParsesMultipleCreates(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(request.Messages[0].Content, "Не спрашивай место или длительность") || !strings.Contains(request.Messages[0].Content, "Создавай все независимо однозначные события") {
			t.Fatal("missing create-with-defaults instruction")
		}
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Английский","location":null,"start_local":"2026-09-09T14:00:00","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":0,"confidence":0.99},{"operation":"create","target_ids":[],"kind":"event","category":"event","title":"Встреча","location":null,"start_local":"2026-09-10T10:00:00","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":0,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}
	proposals, question, err := s.ParseOperations(context.Background(), "завтра английский в 14:00, послезавтра встреча в 10:00", now, time.UTC, nil)
	if err != nil || question != "" || len(proposals) != 2 {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
	if got := proposals[0].Event.EndsAt.Sub(proposals[0].Event.StartsAt); got != 95*time.Minute || !proposals[0].Inferred {
		t.Fatalf("lesson default=%v inferred=%v", got, proposals[0].Inferred)
	}
	if got := proposals[1].Event.EndsAt.Sub(proposals[1].Event.StartsAt); got != time.Hour || !proposals[1].Inferred {
		t.Fatalf("event default=%v inferred=%v", got, proposals[1].Inferred)
	}
}

func TestParseOperationsUsesGroupPairContext(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, input, start, end, category string
	}{
		{"first lesson", "завтра на первой паре квантовая", "2026-09-09T09:00:00", "2026-09-09T10:35:00", "lesson"},
		{"second lesson", "в пятницу на второй паре органика", "2026-09-11T10:50:00", "2026-09-11T12:25:00", "lesson"},
		{"third test", "на третьей паре контрольная", "2026-09-09T12:25:00", "2026-09-09T14:15:00", "test"},
		{"fifth seminar", "на пятой паре семинар", "2026-09-09T16:35:00", "2026-09-09T18:20:00", "event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"%s","category":"%s","title":"x","location":null,"start_local":"%s","end_local":"%s","timezone":"UTC","all_day":false,"rrule":null,"tags":[],"duration_minutes":%d,"confidence":0.99}],"question":""}`, map[bool]string{true: "lesson", false: "event"}[tc.category == "lesson" || tc.category == "test"], tc.category, tc.start, tc.end, int(parseTime(t, tc.end).Sub(parseTime(t, tc.start)).Minutes()))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []message `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&request)
				if !strings.Contains(request.Messages[0].Content, "1 09:00–10:35") || !strings.Contains(request.Messages[0].Content, "Таблица имеет приоритет") {
					t.Fatal("group context was not included in event prompt")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
			}))
			defer server.Close()
			proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), tc.input, now, time.UTC, nil)
			if err != nil || question != "" || len(proposals) != 1 || proposals[0].Event.Category != tc.category || proposals[0].Event.StartsAt.Format("2006-01-02T15:04:05") != tc.start || proposals[0].Event.EndsAt.Format("2006-01-02T15:04:05") != tc.end {
				t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
			}
		})
	}
}

func TestParseOperationsValidatesRecurrenceFromGroupContext(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, input, start, rrule string
		allDay                    bool
	}{
		{"weekly pair", "каждый вторник второй парой семинар", "2026-09-15T10:50:00", "FREQ=WEEKLY;BYDAY=TU;COUNT=8", false},
		{"two weekly pair", "каждые две недели по четвергам первой парой квантовая", "2026-09-10T09:00:00", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH;COUNT=8", false},
		{"count", "каждый вторник семинар, 6 раз", "2026-09-15T10:50:00", "FREQ=WEEKLY;BYDAY=TU;COUNT=6", false},
		{"until", "каждую пятницу консультация до 20 декабря", "2026-09-11T15:00:00", "FREQ=WEEKLY;BYDAY=FR;UNTIL=20261220T000000Z", false},
		{"all-day deadline", "каждую пятницу дедлайн отчёта", "2026-09-11", "FREQ=WEEKLY;BYDAY=FR;COUNT=8", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			duration, end := 95, "2026-09-15T12:25:00"
			kind, category := "lesson", "lesson"
			if tc.allDay {
				duration, end = 0, ""
				kind, category = "deadline", "deadline"
			}
			response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"%s","category":"%s","title":"Семинар","location":null,"start_local":"%s","end_local":null,"timezone":"UTC","all_day":%t,"rrule":"%s","tags":[],"duration_minutes":%d,"confidence":0.99}],"question":""}`, kind, category, tc.start, tc.allDay, tc.rrule, duration)
			if !tc.allDay && end == "" {
				t.Fatal("timed response needs an end")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Messages []message `json:"messages"`
				}
				_ = json.NewDecoder(r.Body).Decode(&request)
				if !strings.Contains(request.Messages[0].Content, "Раз в две недели") || !strings.Contains(request.Messages[0].Content, "Дедлайн без времени") || !strings.Contains(request.Messages[0].Content, "1 09:00–10:35") {
					t.Fatal("recurrence, deadline, or pair context missing from event prompt")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
			}))
			defer server.Close()
			proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), tc.input, now, time.UTC, nil)
			if err != nil || question != "" || len(proposals) != 1 || proposals[0].Event.RRule == nil || *proposals[0].Event.RRule != tc.rrule || proposals[0].Event.AllDay != tc.allDay {
				t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
			}
		})
	}
}

func TestParseOperationsAllowsRecurringSeriesWithoutExplicitEnd(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, input, start, end, rule string
	}{
		{"weekly pair", "каждый вторник второй парой физхимия", "2026-09-15T10:50:00", "2026-09-15T12:25:00", "FREQ=WEEKLY;BYDAY=TU"},
		{"two-week pair", "каждые две недели в четверг четвёртой парой квантовая", "2026-09-10T15:00:00", "2026-09-10T16:35:00", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := fmt.Sprintf(`{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"x","location":null,"start_local":"%s","end_local":"%s","timezone":"UTC","all_day":false,"rrule":"%s","week_parity":"","tags":[],"duration_minutes":95,"confidence":0.99}],"question":""}`, tc.start, tc.end, tc.rule)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
			}))
			defer server.Close()
			proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), tc.input, now, time.UTC, nil)
			if err != nil || question != "" || len(proposals) != 1 || proposals[0].Event.RRule == nil || *proposals[0].Event.RRule != tc.rule {
				t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
			}
		})
	}
}

func TestParseOperationsAllowsOneTimeEventAfterOneWeek(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"event","category":"event","title":"Консультация","location":null,"start_local":"2026-09-15T14:00:00","end_local":null,"timezone":"UTC","all_day":false,"rrule":null,"week_parity":"","tags":[],"duration_minutes":60,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), "Через неделю в 14:00 консультация", now, time.UTC, nil)
	if err != nil || question != "" || len(proposals) != 1 || proposals[0].Event.RRule != nil {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
}

func TestParseOperationsPassesAcademicWeekParityContext(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		system := request.Messages[0].Content
		if !strings.Contains(system, "reference_week_start: 2026-09-07") || !strings.Contains(system, "reference_week_parity: odd") || !strings.Contains(system, `"week_parity":null`) {
			t.Fatal("configured academic parity context missing from AI request")
		}
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Квантовая химия","location":null,"start_local":"2026-09-08T10:50:00","end_local":"2026-09-08T12:25:00","timezone":"UTC","all_day":false,"rrule":"FREQ=WEEKLY;INTERVAL=2;BYDAY=TU;COUNT=6","week_parity":"even","tags":[],"duration_minutes":95,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client(), WeekParity: schedule.WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "odd"}}
	proposals, question, err := s.ParseOperations(context.Background(), "по чётным неделям во вторник второй парой квантовая химия", now, time.UTC, nil)
	if err != nil || question != "" || len(proposals) != 1 || proposals[0].WeekParity != "even" || proposals[0].Event.RRule == nil {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
}

func TestParseScheduleInputKeepsDefiniteRecordsAndSkipsUncertainOnes(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !strings.Contains(request.Messages[0].Content, "Режим admin import") || !strings.Contains(request.Messages[1].Content, strings.Repeat("материалы ", 600)) {
			t.Fatal("admin import prompt was not used for the full paste")
		}
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Физхимия","location":null,"start_local":"2026-09-09T10:50:00","end_local":"2026-09-09T12:25:00","timezone":"UTC","all_day":false,"rrule":null,"week_parity":"","tags":[],"duration_minutes":95,"confidence":0.99},{"operation":"create","target_ids":[],"kind":"deadline","category":"deadline","title":"Документы","location":null,"start_local":"2026-09-11","end_local":null,"timezone":"UTC","all_day":true,"rrule":null,"week_parity":"","tags":[],"duration_minutes":0,"confidence":0.99}],"skipped":[{"source":"Семинар инструментальных методов","reason":"нет точного слота"}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	input := "Среда: вторая пара физхимия\nДедлайн документов до пятницы\nСеминар инструментальных методов со следующей недели, слот пока неизвестен\n" + strings.Repeat("материалы ", 600)
	result, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseScheduleInput(context.Background(), input, now, time.UTC, nil)
	if err != nil || len(result.Operations) != 2 || len(result.Skipped) != 1 || result.Operations[0].Event.StartsAt.Format("15:04") != "10:50" || !result.Operations[1].Event.AllDay {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestParseScheduleInputAcceptsOddWeekRecurringPair(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"Физхимия","location":null,"start_local":"2026-09-15T10:50:00","end_local":"2026-09-15T12:25:00","timezone":"UTC","all_day":false,"rrule":"FREQ=WEEKLY;INTERVAL=2;BYDAY=TU;COUNT=8","week_parity":"odd","tags":[],"duration_minutes":95,"confidence":0.99}],"skipped":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client(), WeekParity: schedule.WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "odd"}}
	result, err := s.ParseScheduleInput(context.Background(), "Каждый вторник второй парой физхимия, по нечётным неделям", now, time.UTC, nil)
	if err != nil || len(result.Operations) != 1 || result.Operations[0].WeekParity != "odd" || result.Operations[0].Event.RRule == nil || *result.Operations[0].Event.RRule != "FREQ=WEEKLY;INTERVAL=2;BYDAY=TU;COUNT=8" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestParseScheduleInputBuildsStructuredRecurrence(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"operations":[{"operation":"create","target_ids":[],"kind":"lesson","category":"lesson","title":"География","location":null,"start_local":"2026-09-11T09:00:00","end_local":"2026-09-11T10:35:00","timezone":"UTC","all_day":false,"rrule":null,"recurrence":{"frequency":"weekly","interval":2,"weekday":["FR"],"count":0,"until_local":""},"week_parity":"even","source_index":1,"source_text":"по чётным неделям в пятницу первой парой география","tags":[],"duration_minutes":95,"confidence":0.99}],"skipped":[]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	s := Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client(), WeekParity: schedule.WeekParityConfig{ReferenceWeekStart: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), ReferenceParity: "even"}}
	result, err := s.ParseScheduleInput(context.Background(), "по чётным неделям в пятницу первой парой география", now, time.UTC, nil)
	if err != nil || len(result.Operations) != 1 || result.Operations[0].Event.RRule == nil || *result.Operations[0].Event.RRule != "FREQ=WEEKLY;INTERVAL=2;BYDAY=FR" || result.Operations[0].SourceIndex != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestParseOperationsPassesMissingAcademicParityInstruction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if !strings.Contains(request.Messages[0].Content, "Не настроено, какая учебная неделя считается чётной") {
			t.Fatal("missing-parity instruction absent from AI request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: `{"operations":[],"question":"Не настроено, какая учебная неделя считается чётной. Укажите одну известную чётную или нечётную неделю."}`}}}})
	}))
	defer server.Close()
	_, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), "по чётным неделям во вторник семинар", time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC), time.UTC, nil)
	if err != nil || !strings.Contains(question, "Не настроено") {
		t.Fatalf("question=%q err=%v", question, err)
	}
}

func TestParseOperationsAllDayAndTimedDeadlines(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, input, response string
		allDay                bool
		start, end            string
	}{
		{"all-day deadline", "дедлайн подачи документов до 25 сентября", `{"kind":"deadline","category":"deadline","start_local":"2026-09-25","all_day":true,"duration_minutes":0}`, true, "2026-09-25T00:00:00", "2026-09-26T00:00:00"},
		{"timed deadline", "дедлайн подачи документов 25 сентября до 18:00", `{"kind":"deadline","category":"deadline","start_local":"2026-09-25T18:00:00","all_day":false,"duration_minutes":60}`, false, "2026-09-25T18:00:00", "2026-09-25T19:00:00"},
		{"all-day event", "20 сентября День химика", `{"kind":"event","category":"event","start_local":"2026-09-20","all_day":true,"duration_minutes":0}`, true, "2026-09-20T00:00:00", "2026-09-21T00:00:00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := `{"operations":[{"operation":"create","target_ids":[],"title":"Подача документов","location":null,"timezone":"UTC","end_local":null,"rrule":null,"tags":[],"confidence":0.99,` + strings.Trim(tc.response, "{}") + `}],"question":""}`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
			}))
			defer server.Close()
			proposals, _, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), tc.input, now, time.UTC, nil)
			if err != nil || len(proposals) != 1 || proposals[0].Event.AllDay != tc.allDay || proposals[0].Event.StartsAt.Format("2006-01-02T15:04:05") != tc.start || proposals[0].Event.EndsAt.Format("2006-01-02T15:04:05") != tc.end {
				t.Fatalf("proposals=%#v err=%v", proposals, err)
			}
		})
	}
}

func TestExamDefaultDurationIsThreeAndHalfHours(t *testing.T) {
	v := operation{Operation: "create", Kind: "event", Category: "exam", Title: "Экзамен", Start: "2026-09-09T10:00:00", Timezone: "UTC", Duration: 0, Confidence: .99}
	p, err := proposal(v, time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), time.UTC, nil)
	if err != nil || p.Event.EndsAt.Sub(p.Event.StartsAt) != 210*time.Minute {
		t.Fatalf("proposal=%#v err=%v", p, err)
	}
}

func TestParseOperationsExpandsBulkCancelAtomically(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	candidates := []schedule.Event{{ID: 2, GroupID: 1, Kind: "lesson", Category: "test", Title: "КР по математике"}, {ID: 6, GroupID: 1, Kind: "lesson", Category: "test", Title: "КР по математике"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"operations":[{"operation":"cancel","target_ids":[2,6],"kind":"","category":"","title":"","start_local":"","timezone":"","all_day":false,"duration_minutes":0,"confidence":0.99}],"question":""}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: response}}}})
	}))
	defer server.Close()
	proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), "убери все пары по математике", now, time.UTC, candidates)
	if err != nil || question != "" || len(proposals) != 2 || proposals[0].Event.ID != 2 || proposals[1].Event.ID != 6 || proposals[0].Operation != "cancel" || proposals[1].Operation != "cancel" {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
}

func TestNormalLessonWithoutTimeReturnsQuestion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Content: `{"operations":[],"question":"Во сколько будет занятие?"}`}}}})
	}))
	defer server.Close()
	proposals, question, err := (Service{BaseURL: server.URL, Key: "test", Model: "test", Client: server.Client()}).ParseOperations(context.Background(), "20 сентября квантовая химия", time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC), time.UTC, nil)
	if err != nil || len(proposals) != 0 || question != "Во сколько будет занятие?" {
		t.Fatalf("proposals=%#v question=%q err=%v", proposals, question, err)
	}
}

func parseTime(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02T15:04:05", value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
