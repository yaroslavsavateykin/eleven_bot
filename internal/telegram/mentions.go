package telegram

import (
	"strings"
	"unicode/utf16"

	"github.com/go-telegram/bot/models"
)

const maxMentionMessageUTF16 = 3900
const maxMentionNameUTF16 = 256

type MentionMessage struct {
	Text     string
	Entities []models.MessageEntity
}

func buildMentionBatches(prefix string, members []TelegramMember, botID int64) []MentionMessage {
	prefix = strings.TrimSpace(limit(prefix, 1000))
	seen := make(map[int64]struct{}, len(members))
	var batches []MentionMessage
	current := MentionMessage{Text: prefix}
	if current.Text != "" {
		current.Text += "\n\n"
	}
	for _, member := range members {
		if member.ID == 0 || member.ID == botID || member.Bot || member.Deleted {
			continue
		}
		if _, duplicate := seen[member.ID]; duplicate {
			continue
		}
		seen[member.ID] = struct{}{}
		name := mentionName(member)
		separator := ""
		if current.Text != "" && !strings.HasSuffix(current.Text, "\n\n") {
			separator = " "
		}
		if utf16Len(current.Text)+utf16Len(separator)+utf16Len(name) > maxMentionMessageUTF16 && len(current.Entities) > 0 {
			batches = append(batches, current)
			current = MentionMessage{}
			separator = ""
		}
		offset := utf16Len(current.Text) + utf16Len(separator)
		current.Text += separator + name
		current.Entities = append(current.Entities, models.MessageEntity{
			Type:   models.MessageEntityTypeTextMention,
			Offset: offset,
			Length: utf16Len(name),
			User:   &models.User{ID: member.ID, Username: member.Username, FirstName: member.FirstName, LastName: member.LastName, IsBot: member.Bot},
		})
	}
	if len(current.Entities) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func mentionName(member TelegramMember) string {
	name := strings.TrimSpace(strings.Join([]string{member.FirstName, member.LastName}, " "))
	if name == "" {
		return "участник"
	}
	return truncateUTF16(name, maxMentionNameUTF16)
}

func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
}

func truncateUTF16(s string, max int) string {
	if utf16Len(s) <= max {
		return s
	}
	var out strings.Builder
	used := 0
	for _, r := range s {
		runeLen := utf16Len(string(r))
		if used+runeLen > max {
			break
		}
		out.WriteRune(r)
		used += runeLen
	}
	return out.String()
}
