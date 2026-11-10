package saves

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScannerWaitsForQuietPeriod(t *testing.T) {
	folder := fixtureFolder(t)
	scanner := Scanner{Folder: folder, Quiet: 5 * time.Second}
	now := time.Now()

	_, firstErr := scanner.Scan(now)
	_, earlyErr := scanner.Scan(now.Add(4 * time.Second))
	snapshot, settledErr := scanner.Scan(now.Add(5 * time.Second))

	if !errors.Is(firstErr, ErrSettling) || !errors.Is(earlyErr, ErrSettling) {
		t.Fatal("uploaded before quiet period")
	}
	if settledErr != nil {
		t.Fatal(settledErr)
	}
	if len(snapshot.Files) != 2 {
		t.Fatalf("got %d eligible files", len(snapshot.Files))
	}
	if snapshot.Files["HERO_01_123.pld"].Mtime != 1700000000123 {
		t.Fatal("mtime must use Unix milliseconds")
	}
}

func TestScannerDetectsContentChangesWithUnchangedMetadata(t *testing.T) {
	folder := fixtureFolder(t)
	scanner := Scanner{Folder: folder}
	now := time.Now()
	_, _ = scanner.Scan(now)
	first, _ := scanner.Scan(now)
	path := filepath.Join(folder, "HERO_01_123.pld")

	writeFixture(t, path, "different!!")
	second, err := scanner.Scan(now)

	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("same-size content change was missed")
	}
}

func TestScannerResetsQuietPeriodOnChange(t *testing.T) {
	folder := fixtureFolder(t)
	scanner := Scanner{Folder: folder, Quiet: 5 * time.Second}
	now := time.Now()
	_, _ = scanner.Scan(now)

	writeFixture(t, filepath.Join(folder, "HERO_01_123.pld"), "longer replacement save")
	_, changedErr := scanner.Scan(now.Add(5 * time.Second))
	_, settledErr := scanner.Scan(now.Add(10 * time.Second))

	if !errors.Is(changedErr, ErrSettling) {
		t.Fatal("change did not restart quiet period")
	}
	if settledErr != nil {
		t.Fatal(settledErr)
	}
}

func TestScannerRequiresAccount(t *testing.T) {
	folder := fixtureFolder(t)
	if err := os.Remove(filepath.Join(folder, "ACCOUNT_player_123.pld")); err != nil {
		t.Fatal(err)
	}
	scanner := Scanner{Folder: folder}

	_, _ = scanner.Scan(time.Now())
	_, err := scanner.Scan(time.Now())

	if err == nil || errors.Is(err, ErrSettling) {
		t.Fatal("missing account was not rejected")
	}
}

func TestScannerRejectsMultipleAccounts(t *testing.T) {
	folder := fixtureFolder(t)
	writeFixture(t, filepath.Join(folder, "ACCOUNT_other_456.pld"), "account")
	scanner := Scanner{Folder: folder}

	_, _ = scanner.Scan(time.Now())
	_, err := scanner.Scan(time.Now())

	if err == nil || errors.Is(err, ErrSettling) {
		t.Fatal("multiple accounts were not rejected")
	}
}

func TestScannerFingerprintIsDeterministic(t *testing.T) {
	folder := fixtureFolder(t)
	scanner := Scanner{Folder: folder}
	_, _ = scanner.Scan(time.Now())

	first, firstErr := scanner.Scan(time.Now())
	second, secondErr := scanner.Scan(time.Now())

	if firstErr != nil || secondErr != nil {
		t.Fatalf("scan errors: %v, %v", firstErr, secondErr)
	}
	if first.Fingerprint != second.Fingerprint || first.Fingerprint == "" {
		t.Fatal("unstable fingerprint")
	}
}

func TestScannerFingerprintIgnoresModificationTime(t *testing.T) {
	folder := fixtureFolder(t)
	scanner := Scanner{Folder: folder}
	now := time.Now()
	path := filepath.Join(folder, "HERO_01_123.pld")
	timestamp := time.UnixMilli(1800000000456)

	_, _ = scanner.Scan(now)
	first, firstErr := scanner.Scan(now)
	touchErr := os.Chtimes(path, timestamp, timestamp)
	_, changedErr := scanner.Scan(now)
	second, secondErr := scanner.Scan(now)

	if firstErr != nil || touchErr != nil || secondErr != nil {
		t.Fatalf("errors: %v, %v, %v", firstErr, touchErr, secondErr)
	}
	if !errors.Is(changedErr, ErrSettling) {
		t.Fatal("timestamp change must still trigger stability checks")
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("timestamp-only change altered the fingerprint")
	}
	if second.Files["HERO_01_123.pld"].Mtime != timestamp.UnixMilli() {
		t.Fatal("upload payload must retain the updated modification time")
	}
}

func TestEligibleFiltersFiles(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"HERO_01_123.pld", true}, {"account_player_123.PLD", true},
		{"ENTITLEMENTS_player_123_1.pld", true}, {"SETTINGS_123.pld", true}, {"LEGACY_player_123.pld", true},
		{"ACCOUNT_VERIFY_123.pld", false}, {"HERO_01_123.txt", false}, {"private.pld", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := eligible(tc.name)
			if got != tc.want {
				t.Fatalf("eligible = %v; want %v", got, tc.want)
			}
		})
	}
}

func fixtureFolder(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	writeFixture(t, filepath.Join(folder, "HERO_01_123.pld"), "hero data!!")
	writeFixture(t, filepath.Join(folder, "ACCOUNT_player_123.pld"), "account data")
	writeFixture(t, filepath.Join(folder, "ACCOUNT_VERIFY_123.pld"), "never upload")
	writeFixture(t, filepath.Join(folder, "notes.txt"), "never upload")
	return folder
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	timestamp := time.UnixMilli(1700000000123)
	if err := os.Chtimes(path, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
}
