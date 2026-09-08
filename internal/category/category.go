package category

import (
	"fmt"
	"strings"
	"unicode"
)

// Resolve shares conservative defaults between migration, API and AI ingestion.
func Resolve(value, kind, title string) (string, error) {
	if value == "" {
		words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		for _, rule := range []struct{ word, category string }{
			{"экзамен", "exam"}, {"дедлайн", "deadline"}, {"кр", "test"},
			{"контрольная", "test"}, {"проверочная", "quiz"},
		} {
			for _, word := range words {
				if word == rule.word {
					return rule.category, nil
				}
			}
		}
		switch kind {
		case "lesson", "deadline", "event", "test", "quiz", "exam":
			return kind, nil
		case "":
			return "event", nil
		default:
			return "other", nil
		}
	}
	switch value {
	case "lesson", "event", "test", "quiz", "exam", "deadline", "other":
		return value, nil
	default:
		return "", fmt.Errorf("invalid category: expected lesson, event, test, quiz, exam, deadline or other")
	}
}
