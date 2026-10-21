package dalgo2logs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
)

// Format is an explicitly configured file parser. There is no heuristic fallback.
type Format string

const (
	JSONL Format = "jsonl"
	Text  Format = "timestamped-text"
)

// Source names one collection and one file below Config.Root.
type Source struct {
	Collection string
	Path       string
	Format     Format
	// TimestampField selects one trusted top-level JSONL key. Empty preserves
	// the legacy timestamp/time selection; explicit values never fall back.
	TimestampField string
}

// Config is trusted server-side configuration. Never derive Root or Sources
// from an HTTP request. Every limit has a safe default and a fixed ceiling.
type Config struct {
	ID           string
	Root         string
	Sources      []Source
	MaxFileBytes int64
	MaxLineBytes int
	MaxEntries   int
	MaxResults   int
}

type source struct {
	Source
	path string
}

var timestampFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func NewDB(cfg Config) (dal.DB, error) {
	if cfg.ID == "" || cfg.Root == "" || len(cfg.Sources) == 0 {
		return nil, errors.New("dalgo2logs: ID, Root, and Sources are required")
	}
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = 8 << 20
	}
	if cfg.MaxLineBytes == 0 {
		cfg.MaxLineBytes = 256 << 10
	}
	if cfg.MaxEntries == 0 {
		cfg.MaxEntries = 20000
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 200
	}
	if cfg.MaxFileBytes < 1 || cfg.MaxFileBytes > 64<<20 || cfg.MaxLineBytes < 1 || cfg.MaxLineBytes > 1<<20 || cfg.MaxEntries < 1 || cfg.MaxEntries > 100000 || cfg.MaxResults < 1 || cfg.MaxResults > 1000 {
		return nil, errors.New("dalgo2logs: limits exceed supported bounds")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("dalgo2logs: root: %w", err)
	}
	// Canonicalize the trusted configured root (macOS commonly aliases /var
	// to /private/var). Source components below it must still be symlink-free.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("dalgo2logs: root: %w", err)
	}
	if err := noSymlink(root); err != nil {
		return nil, fmt.Errorf("dalgo2logs: root: %w", err)
	}
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, errors.New("dalgo2logs: root must be a readable directory")
	}
	cfg.Root = root
	idx := make(map[string]source, len(cfg.Sources))
	for _, s := range cfg.Sources {
		if s.Collection == "" || strings.ContainsAny(s.Collection, "/\\. ") || !filepath.IsLocal(s.Path) || s.Path != filepath.Clean(s.Path) || (s.Format != JSONL && s.Format != Text) {
			return nil, errors.New("dalgo2logs: invalid source configuration")
		}
		if s.TimestampField != "" && (s.Format != JSONL || !timestampFieldName.MatchString(s.TimestampField)) {
			return nil, errors.New("dalgo2logs: timestamp field requires JSONL and a top-level ASCII identifier of at most 128 characters")
		}
		if _, exists := idx[s.Collection]; exists {
			return nil, errors.New("dalgo2logs: duplicate collection")
		}
		path := filepath.Join(root, s.Path)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("dalgo2logs: source path leaves allowed root")
		}
		if err := noSymlink(path); err != nil {
			return nil, fmt.Errorf("dalgo2logs: source: %w", err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("dalgo2logs: source must be a regular file")
		}
		idx[s.Collection] = source{Source: s, path: path}
	}
	// Keep the trusted directory open: Root.Open resolves every future source
	// beneath this handle even if a path component is swapped after validation.
	openedRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, errors.New("dalgo2logs: allowed root is unavailable")
	}
	backend := &database{cfg: cfg, sources: idx, root: openedRoot, closeSource: (*os.File).Close}
	return &fileHandle{DB: dal.NewDB(backend), SchemaReader: dbschema.SchemaReader(backend), root: openedRoot}, nil
}

// noSymlink checks every existing component, including Root and a source file.
// The scan repeats this check at query time to catch replacement after setup.
func noSymlink(path string) error {
	clean := filepath.Clean(path)
	for p := clean; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink is not allowed")
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	return nil
}
