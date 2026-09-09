package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Token                                                                                          string
	GroupChatID                                                                                    int64
	AdminTelegramUserID                                                                            int64
	DiscoveryMode                                                                                  bool
	GroupName, Timezone, DBPath, BaseURL, ExternalToken, AdminPassword, HTTPAddr, GitHubRepository string
	Retention                                                                                      time.Duration
	AIBaseURL, AIKey, AITextModel, AIVisionModel, AISTTModel                                       string
	AcademicReferenceWeekStart, AcademicReferenceWeekParity                                        string
	Semester                                                                                       Semester
}

// Semester is a validated, timezone-aware configured academic period.
type Semester struct {
	Start time.Time
	End   time.Time
}

func Load() (Config, error) {
	c := Config{Token: os.Getenv("TELEGRAM_BOT_TOKEN"), DiscoveryMode: strings.EqualFold(os.Getenv("TELEGRAM_DISCOVERY_MODE"), "true"), GroupName: value("GROUP_NAME", "411 группа"), Timezone: value("GROUP_TIMEZONE", "Europe/Moscow"), DBPath: value("DATABASE_PATH", "data/app.db"), BaseURL: strings.TrimRight(value("BASE_URL", "http://localhost:6767"), "/"), ExternalToken: os.Getenv("EXTERNAL_API_TOKEN"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), HTTPAddr: value("HTTP_ADDR", ":6767"), GitHubRepository: value("GITHUB_REPOSITORY", "yaroslavsavateykin/eleven_bot"), Retention: 48 * time.Hour, AIBaseURL: os.Getenv("AI_BASE_URL"), AIKey: os.Getenv("AI_API_KEY"), AITextModel: os.Getenv("AI_TEXT_MODEL"), AIVisionModel: os.Getenv("AI_VISION_MODEL"), AISTTModel: os.Getenv("AI_STT_MODEL"), AcademicReferenceWeekStart: os.Getenv("ACADEMIC_REFERENCE_WEEK_START"), AcademicReferenceWeekParity: os.Getenv("ACADEMIC_REFERENCE_WEEK_PARITY")}
	var err error
	if v := os.Getenv("ADMIN_TELEGRAM_USER_ID"); v != "" {
		c.AdminTelegramUserID, err = strconv.ParseInt(v, 10, 64)
		if err != nil || c.AdminTelegramUserID <= 0 {
			return c, fmt.Errorf("invalid ADMIN_TELEGRAM_USER_ID")
		}
	}
	if s := os.Getenv("TELEGRAM_GROUP_CHAT_ID"); s != "" {
		c.GroupChatID, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			return c, fmt.Errorf("TELEGRAM_GROUP_CHAT_ID: %w", err)
		}
	}
	if s := os.Getenv("RAW_MESSAGE_RETENTION_HOURS"); s != "" {
		h, e := strconv.Atoi(s)
		if e != nil || h < 1 {
			return c, fmt.Errorf("invalid RAW_MESSAGE_RETENTION_HOURS")
		}
		c.Retention = time.Duration(h) * time.Hour
	}
	if _, e := time.LoadLocation(c.Timezone); e != nil {
		return c, fmt.Errorf("GROUP_TIMEZONE: %w", e)
	}
	if (c.AcademicReferenceWeekStart == "") != (c.AcademicReferenceWeekParity == "") {
		return c, fmt.Errorf("ACADEMIC_REFERENCE_WEEK_START and ACADEMIC_REFERENCE_WEEK_PARITY must be set together")
	}
	semesterStart, semesterEnd := os.Getenv("SEMESTER_START"), os.Getenv("SEMESTER_END")
	if (semesterStart == "") != (semesterEnd == "") {
		return c, fmt.Errorf("SEMESTER_START and SEMESTER_END must be configured together")
	}
	if semesterStart != "" {
		loc, _ := time.LoadLocation(c.Timezone)
		start, startErr := time.ParseInLocation("2006-01-02", semesterStart, loc)
		end, endErr := time.ParseInLocation("2006-01-02", semesterEnd, loc)
		if startErr != nil || endErr != nil || end.Before(start) {
			return c, fmt.Errorf("SEMESTER_START and SEMESTER_END must be ISO dates with end not before start")
		}
		c.Semester = Semester{Start: start, End: end}
	}
	if c.AcademicReferenceWeekStart != "" {
		start, e := time.ParseInLocation("2006-01-02", c.AcademicReferenceWeekStart, time.UTC)
		if e != nil || start.Weekday() != time.Monday {
			return c, fmt.Errorf("ACADEMIC_REFERENCE_WEEK_START must be a Monday in YYYY-MM-DD format")
		}
		if c.AcademicReferenceWeekParity != "even" && c.AcademicReferenceWeekParity != "odd" {
			return c, fmt.Errorf("ACADEMIC_REFERENCE_WEEK_PARITY must be even or odd")
		}
	}
	return c, nil
}
func value(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
