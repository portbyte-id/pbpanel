package store

import (
	"context"
	"testing"
)

func TestLoadSettingMissingIsNotAnError(t *testing.T) {
	s := openTemp(t)

	value, ok, err := s.LoadSetting(context.Background(), "panel.port")
	if err != nil {
		t.Fatalf("LoadSetting: %v", err)
	}
	if ok || value != "" {
		t.Fatalf("LoadSetting = %q, %v; want empty and not found", value, ok)
	}
}

func TestSaveSettingInsertsThenUpdates(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if err := s.SaveSetting(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("SaveSetting insert: %v", err)
	}
	value, ok, err := s.LoadSetting(ctx, "panel.port")
	if err != nil || !ok || value != "9443" {
		t.Fatalf("LoadSetting after insert = %q, %v, %v; want 9443", value, ok, err)
	}

	if err := s.SaveSetting(ctx, "panel.port", "10443"); err != nil {
		t.Fatalf("SaveSetting update: %v", err)
	}
	value, ok, err = s.LoadSetting(ctx, "panel.port")
	if err != nil || !ok || value != "10443" {
		t.Fatalf("LoadSetting after update = %q, %v, %v; want 10443", value, ok, err)
	}

	var rows int
	if err := s.DB().QueryRow(`SELECT count(*) FROM settings`).Scan(&rows); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if rows != 1 {
		t.Fatalf("settings has %d rows, want 1: the update inserted instead", rows)
	}
}

func TestSaveSettingStampsUpdatedAt(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if err := s.SaveSetting(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	var updated string
	if err := s.DB().QueryRow(`SELECT updated_at FROM settings WHERE name = ?`, "panel.port").Scan(&updated); err != nil {
		t.Fatalf("read updated_at: %v", err)
	}
	// The same shape as the migration ledger: 2026-11-16T22:41:07.123Z.
	if len(updated) != len("2006-01-02T15:04:05.000Z") || updated[len(updated)-1] != 'Z' {
		t.Fatalf("updated_at = %q, want an RFC3339 UTC timestamp with milliseconds", updated)
	}
}

func TestLoadSettingsReturnsEveryRow(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if err := s.SaveSetting(ctx, "panel.port", "9443"); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}
	if err := s.SaveSetting(ctx, "login.max_attempts", "10"); err != nil {
		t.Fatalf("SaveSetting: %v", err)
	}

	all, err := s.LoadSettings(ctx)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if len(all) != 2 || all["panel.port"] != "9443" || all["login.max_attempts"] != "10" {
		t.Fatalf("LoadSettings = %v, want both stored keys", all)
	}
}
