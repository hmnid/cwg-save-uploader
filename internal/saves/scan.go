package saves

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrSettling = errors.New("waiting for saves to settle")

// Scanner reads only direct children. Nested account layouts require explicit support.
type Scanner struct {
	Folder   string
	Quiet    time.Duration
	previous []metadata
	since    time.Time
}

type File struct {
	Text  string
	Mtime int64
}

type Snapshot struct {
	Files       map[string]File
	Fingerprint string
}

// Scan waits for stable metadata, then verifies it again after reading the snapshot.
func (s *Scanner) Scan(now time.Time) (Snapshot, error) {
	before, err := inventory(s.Folder)
	if err != nil {
		s.previous = nil
		return Snapshot{}, err
	}
	if !reflect.DeepEqual(before, s.previous) {
		s.previous, s.since = before, now
		return Snapshot{}, ErrSettling
	}
	if now.Sub(s.since) < s.Quiet {
		return Snapshot{}, ErrSettling
	}
	files := make(map[string]File, len(before))
	hero, accounts := false, 0
	var total int64
	for _, m := range before {
		total += m.Size
		if total > 32<<20 {
			return Snapshot{}, errors.New("save snapshot exceeds 32 MiB safety limit")
		}
		text, err := readFile(filepath.Join(s.Folder, m.Name), m)
		if err != nil {
			s.previous = nil
			return Snapshot{}, err
		}
		files[m.Name] = File{Text: text, Mtime: m.Modified.UnixMilli()}
		upper := strings.ToUpper(m.Name)
		hero = hero || strings.HasPrefix(upper, "HERO_")
		if strings.HasPrefix(upper, "ACCOUNT_") {
			accounts++
		}
	}
	after, err := inventory(s.Folder)
	if err != nil || !reflect.DeepEqual(before, after) {
		s.previous = nil
		return Snapshot{}, ErrSettling
	}
	if accounts > 1 {
		// As for now, i'm not sure the leaderboard can handle an upload with multiplie accounts
		return Snapshot{}, errors.New("multiple ACCOUNT_ files found; select a folder containing one account")
	}
	if !hero || accounts == 0 {
		return Snapshot{}, errors.New("waiting for HERO_ and ACCOUNT_ .pld files")
	}
	// encoding/json sorts map keys. Fingerprint only filenames and content hashes,
	// so timestamp-only changes do not trigger uploads.
	entries := make(map[string]string, len(files))
	for name, file := range files {
		hash := sha256.Sum256([]byte(file.Text))
		entries[name] = hex.EncodeToString(hash[:])
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return Snapshot{}, err
	}
	hash := sha256.Sum256(encoded)
	return Snapshot{Files: files, Fingerprint: hex.EncodeToString(hash[:])}, nil
}

type metadata struct {
	Name     string
	Size     int64
	Modified time.Time
}

func inventory(folder string) ([]metadata, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, fmt.Errorf("read save folder: %w", err)
	}
	result := make([]metadata, 0)
	for _, entry := range entries {
		if !eligible(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > 8<<20 {
			return nil, fmt.Errorf("save %s exceeds 8 MiB safety limit", entry.Name())
		}
		result = append(result, metadata{entry.Name(), info.Size(), info.ModTime()})
	}
	return result, nil
}

func eligible(name string) bool {
	upper := strings.ToUpper(name)
	if !strings.HasSuffix(upper, ".PLD") || strings.HasPrefix(upper, "ACCOUNT_VERIFY_") {
		return false
	}
	for _, prefix := range []string{"HERO_", "ACCOUNT_", "ENTITLEMENTS_", "LEGACY_", "SETTINGS_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

func readFile(path string, expected metadata) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", ErrSettling
	}
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read save: %w", err)
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(info, opened) || opened.Size() != expected.Size || !opened.ModTime().Equal(expected.Modified) {
		return "", ErrSettling
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) != expected.Size {
		return "", ErrSettling
	}
	if !utf8.Valid(data) {
		return "", errors.New("save is not valid UTF-8")
	}
	// Browser File.text() ignores a UTF-8 BOM.
	return strings.TrimPrefix(string(data), "\xef\xbb\xbf"), nil
}
