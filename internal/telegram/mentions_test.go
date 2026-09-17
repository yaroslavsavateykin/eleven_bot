package telegram

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestBuildMentionBatches(t *testing.T) {
	members := []TelegramMember{
		{ID: 1, FirstName: "Иван"},
		{ID: 2, FirstName: "Иван", LastName: "Иванов"},
		{ID: 3, FirstName: "🙂 Иван"},
		{ID: 4, FirstName: "Иван 🚀"},
		{ID: 5, FirstName: "Без username"},
		{ID: 5, FirstName: "Дубликат"},
		{ID: 6, FirstName: "Бот", Bot: true},
		{ID: 7, FirstName: "Eleven"},
		{ID: 8, FirstName: "Удалён", Deleted: true},
	}
	batches := buildMentionBatches("Важное объявление", members, 7)
	if len(batches) != 1 {
		t.Fatalf("batches=%d", len(batches))
	}
	batch := batches[0]
	if len(batch.Entities) != 5 {
		t.Fatalf("entities=%d", len(batch.Entities))
	}
	for _, entity := range batch.Entities {
		if entity.Type != "text_mention" || entity.User == nil {
			t.Fatalf("unexpected entity: %#v", entity)
		}
		start, end := utf16Slice(batch.Text, entity.Offset, entity.Length)
		if start == "" || end != "" {
			t.Fatalf("invalid UTF-16 bounds for %#v in %q", entity, batch.Text)
		}
	}
	if batch.Entities[2].Offset != utf16Len("Важное объявление\n\nИван Иван Иванов ") {
		t.Fatalf("emoji offset=%d", batch.Entities[2].Offset)
	}
}

func TestBuildMentionBatchesSplitsWithoutCuttingMentions(t *testing.T) {
	members := make([]TelegramMember, 0, 105)
	for i := range 105 {
		members = append(members, TelegramMember{ID: int64(i + 1), FirstName: strings.Repeat("🙂", 50), LastName: "Иван"})
	}
	batches := buildMentionBatches(strings.Repeat("объявление ", 200), members, 0)
	if len(batches) < 2 {
		t.Fatalf("batches=%d", len(batches))
	}
	count := 0
	for _, batch := range batches {
		if utf16Len(batch.Text) > maxMentionMessageUTF16 {
			t.Fatalf("message is too long: %d", utf16Len(batch.Text))
		}
		for _, entity := range batch.Entities {
			text, remainder := utf16Slice(batch.Text, entity.Offset, entity.Length)
			if text == "" || remainder != "" || entity.Length != utf16Len(text) {
				t.Fatalf("mention was cut: %#v in %q", entity, batch.Text)
			}
			count++
		}
	}
	if count != 105 {
		t.Fatalf("mentions=%d", count)
	}
}

func TestBuildMentionBatchesTruncatesVeryLongName(t *testing.T) {
	batches := buildMentionBatches(strings.Repeat("🙂", 1000), []TelegramMember{{ID: 1, FirstName: strings.Repeat("🚀", 1000)}}, 0)
	if len(batches) != 1 || len(batches[0].Entities) != 1 {
		t.Fatalf("unexpected batches: %#v", batches)
	}
	entity := batches[0].Entities[0]
	if entity.Length > maxMentionNameUTF16 || entity.Length != utf16Len(utf16SliceText(t, batches[0].Text, entity.Offset, entity.Length)) {
		t.Fatalf("invalid long-name entity: %#v", entity)
	}
}

// utf16Slice verifies that an entity's UTF-16 offset and length select complete runes.
func utf16Slice(s string, offset, length int) (string, string) {
	units := utf16.Encode([]rune(s))
	if offset < 0 || length < 0 || offset+length > len(units) {
		return "", "out of bounds"
	}
	selected := string(utf16.Decode(units[offset : offset+length]))
	if utf16Len(selected) != length {
		return selected, "split surrogate"
	}
	return selected, ""
}

func utf16SliceText(t *testing.T, s string, offset, length int) string {
	t.Helper()
	text, remainder := utf16Slice(s, offset, length)
	if remainder != "" {
		t.Fatalf("invalid UTF-16 bounds: %q", remainder)
	}
	return text
}
