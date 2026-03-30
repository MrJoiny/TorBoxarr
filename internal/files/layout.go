package files

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Layout struct {
	Root      string
	Staging   string
	Completed string
	Payloads  string
}

func NewLayout(root, staging, completed, payloads string) *Layout {
	return &Layout{
		Root:      root,
		Staging:   staging,
		Completed: completed,
		Payloads:  payloads,
	}
}

func (l *Layout) Ensure() error {
	for _, dir := range []string{l.Root, l.Staging, l.Completed, l.Payloads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("ensure directory %s: %w", dir, err)
		}
	}
	same, err := sameFilesystem(l.Staging, l.Completed)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("staging (%s) and completed (%s) must share the same filesystem", l.Staging, l.Completed)
	}
	return nil
}

func (l *Layout) StagingPathForJob(jobID string) string {
	return filepath.Join(l.Staging, jobID)
}

func (l *Layout) PayloadPathForJob(jobID, name string) string {
	return filepath.Join(l.Payloads, jobID, sanitize(name))
}

func (l *Layout) PayloadDirForJob(jobID string) string {
	return filepath.Join(l.Payloads, jobID)
}

func (l *Layout) CompletedPathForJob(category, displayName, jobID string) string {
	category = sanitize(strings.TrimSpace(category))
	// defensive: sanitize() currently never returns "" (returns "unnamed" for
	// empty input), but guard against future semantic changes.
	if category == "" {
		category = "uncategorized"
	}
	name := sanitize(strings.TrimSpace(displayName))
	if name == "" {
		name = jobID
	}
	return filepath.Join(l.Completed, category, fmt.Sprintf("%s-%s", name, jobID))
}

func (l *Layout) SavePayload(jobID, name string, reader io.Reader) (string, string, error) {
	path := l.PayloadPathForJob(jobID, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("ensure payload parent: %w", err)
	}
	file, err := os.Create(path)
	if err != nil {
		return "", "", fmt.Errorf("create payload file: %w", err)
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(file, h), reader); err != nil {
		return "", "", fmt.Errorf("write payload file: %w", err)
	}
	return path, hex.EncodeToString(h.Sum(nil)), nil
}

func (l *Layout) Promote(stagingPath, completedPath string) error {
	if stagingPath == "" || completedPath == "" {
		return fmt.Errorf("both stagingPath and completedPath are required")
	}
	if err := os.MkdirAll(filepath.Dir(completedPath), 0o755); err != nil {
		return fmt.Errorf("ensure completed parent: %w", err)
	}
	if err := os.Rename(stagingPath, completedPath); err != nil {
		return fmt.Errorf("rename staging to completed: %w", err)
	}
	return nil
}

func (l *Layout) RemovePath(path string) error {
	if path == "" {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("remove path %s: %w", path, err)
	}
	return nil
}

func (l *Layout) RemoveOrphanStagingDirs(validJobIDs map[string]struct{}) ([]string, error) {
	entries, err := os.ReadDir(l.Staging)
	if err != nil {
		return nil, fmt.Errorf("read staging root: %w", err)
	}

	var removed []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := validJobIDs[entry.Name()]; ok {
			continue
		}
		orphanPath := filepath.Join(l.Staging, entry.Name())
		if err := os.RemoveAll(orphanPath); err != nil {
			return removed, fmt.Errorf("remove orphan staging dir %s: %w", orphanPath, err)
		}
		removed = append(removed, orphanPath)
	}
	return removed, nil
}

var sanitizeReplacer = strings.NewReplacer(
	"/", "_",
	"\\", "_",
	":", "_",
	"*", "_",
	"?", "_",
	"\"", "_",
	"<", "_",
	">", "_",
	"|", "_",
)

func sanitize(v string) string {
	v = sanitizeReplacer.Replace(strings.TrimSpace(v))
	v = strings.Trim(v, ". ")
	if v == "" {
		return "unnamed"
	}
	return v
}
