package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
)

var version = "dev"

const defaultMaxSize int64 = 100 * 1024 * 1024

type options struct {
	Note       string
	Keep       int
	Yes        bool
	JSON       bool
	FullDiff   bool
	NoCompress bool
	NoDeref    bool
	All        bool
	Help       bool
	MaxSize    int64
}

type Index struct {
	Version int                   `json:"version"`
	Files   map[string]IndexEntry `json:"files"`
}

type IndexEntry struct {
	Path     string `json:"path"`
	Key      string `json:"key"`
	Versions int    `json:"versions"`
	LastTime string `json:"last_time"`
}

type Meta struct {
	Path      string    `json:"path"`
	Key       string    `json:"key"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Versions  []Version `json:"versions"`
}

type Version struct {
	V          int    `json:"v"`
	Time       string `json:"time"`
	Size       int64  `json:"size"`
	SHA        string `json:"sha"`
	Note       string `json:"note,omitempty"`
	Mode       uint32 `json:"mode"`
	Stored     string `json:"stored"`
	Compressed bool   `json:"compressed"`
	Kind       string `json:"kind"`
	LinkTarget string `json:"link_target,omitempty"`
}

type store struct {
	root      string
	objects   string
	indexPath string
}

type fileContent struct {
	data       []byte
	mode       uint32
	size       int64
	sha        string
	kind       string
	linkTarget string
}

type snapshotResult struct {
	Changed bool    `json:"changed"`
	Version Version `json:"version,omitempty"`
	Message string  `json:"message"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bak:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd, pos, opts, err := parseArgs(args)
	if err != nil {
		return err
	}
	if opts.Help || cmd == "help" {
		printUsage()
		return nil
	}
	if cmd == "version" {
		fmt.Println(version)
		return nil
	}

	switch cmd {
	case "snapshot":
		if len(pos) != 1 {
			return errors.New("usage: bak <file> [-m note]")
		}
		res, err := snapshotFile(pos[0], opts)
		if err != nil {
			return err
		}
		if opts.JSON {
			return printJSON(res)
		}
		fmt.Println(res.Message)
	case "list":
		if opts.All {
			return listAll(opts)
		}
		if len(pos) != 1 {
			return errors.New("usage: bak list <file> OR bak list --all")
		}
		return listFile(pos[0], opts)
	case "diff":
		if len(pos) < 1 || len(pos) > 3 {
			return errors.New("usage: bak diff <file> [vN] [vM]")
		}
		return diffFile(pos[0], pos[1:], opts)
	case "restore":
		if len(pos) < 1 || len(pos) > 2 {
			return errors.New("usage: bak restore <file> [vN]")
		}
		spec := ""
		if len(pos) == 2 {
			spec = pos[1]
		}
		return restoreFile(pos[0], spec, opts)
	case "show":
		if len(pos) != 2 {
			return errors.New("usage: bak show <file> vN")
		}
		return showVersion(pos[0], pos[1])
	case "rm":
		if len(pos) != 1 {
			return errors.New("usage: bak rm <file>")
		}
		return removeHistory(pos[0], opts)
	case "prune":
		if len(pos) != 1 || opts.Keep <= 0 {
			return errors.New("usage: bak prune <file> --keep N")
		}
		return pruneFile(pos[0], opts)
	case "gc":
		if len(pos) != 0 {
			return errors.New("usage: bak gc")
		}
		return gc(opts)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}

	return nil
}

func parseArgs(args []string) (string, []string, options, error) {
	opts := options{MaxSize: defaultMaxSize}
	if len(args) == 0 {
		return "help", nil, opts, nil
	}

	cmd := "snapshot"
	start := 0
	if isCommand(args[0]) {
		cmd = args[0]
		start = 1
	}

	var pos []string
	for i := start; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			opts.Help = true
		case arg == "--version":
			return "version", nil, opts, nil
		case arg == "-m" || arg == "--note":
			i++
			if i >= len(args) {
				return "", nil, opts, errors.New("--note requires a value")
			}
			opts.Note = args[i]
		case strings.HasPrefix(arg, "--note="):
			opts.Note = strings.TrimPrefix(arg, "--note=")
		case arg == "--keep":
			i++
			if i >= len(args) {
				return "", nil, opts, errors.New("--keep requires a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return "", nil, opts, errors.New("--keep must be a positive integer")
			}
			opts.Keep = n
		case strings.HasPrefix(arg, "--keep="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--keep="))
			if err != nil || n <= 0 {
				return "", nil, opts, errors.New("--keep must be a positive integer")
			}
			opts.Keep = n
		case arg == "--yes" || arg == "-y":
			opts.Yes = true
		case arg == "--json":
			opts.JSON = true
		case arg == "-f" || arg == "--full" || arg == "--full-diff":
			if cmd != "diff" {
				return "", nil, opts, errors.New("-f is only valid with bak diff")
			}
			opts.FullDiff = true
		case arg == "--no-compress":
			opts.NoCompress = true
		case arg == "--no-deref":
			opts.NoDeref = true
		case arg == "--all":
			opts.All = true
		case arg == "--max-size":
			i++
			if i >= len(args) {
				return "", nil, opts, errors.New("--max-size requires a value")
			}
			size, err := parseSize(args[i])
			if err != nil {
				return "", nil, opts, err
			}
			opts.MaxSize = size
		case strings.HasPrefix(arg, "--max-size="):
			size, err := parseSize(strings.TrimPrefix(arg, "--max-size="))
			if err != nil {
				return "", nil, opts, err
			}
			opts.MaxSize = size
		case strings.HasPrefix(arg, "-"):
			return "", nil, opts, fmt.Errorf("unknown flag %s", arg)
		default:
			pos = append(pos, arg)
		}
	}

	return cmd, pos, opts, nil
}

func isCommand(s string) bool {
	switch s {
	case "list", "diff", "restore", "show", "rm", "prune", "gc", "help", "version":
		return true
	default:
		return false
	}
}

func printUsage() {
	fmt.Print(`bak - per-file snapshots without cwd clutter

Usage:
  bak <file>                    snapshot now
  bak <file> -m "before tls"    snapshot with a note
  bak list <file>               list versions
  bak list --all                list tracked files
  bak diff [-f] <file> [vN] [vM] diff snapshot/current
  bak restore <file> [vN]       restore, saving current first when needed
  bak show <file> vN            print a version to stdout
  bak rm <file>                 drop history for a file
  bak prune <file> --keep N     keep newest N versions
  bak gc                        remove histories for deleted files

Flags:
  -m, --note TEXT       add a version note
      --keep N          prune after snapshot or with prune
      --yes             skip confirmation prompts
      --json            machine-readable output where supported
  -f, --full            show full-file diff instead of compact hunks
      --no-compress     store raw snapshots instead of zstd
      --no-deref        snapshot symlink itself instead of target content
      --max-size SIZE   max snapshot size before requiring --yes (default 100MB)

Storage:
  Uses $BAK_DIR when set, otherwise ~/.bak.
`)
}

func newStore() (*store, error) {
	root := os.Getenv("BAK_DIR")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(home, ".bak")
	}
	root = expandHome(root)
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &store{
		root:      abs,
		objects:   filepath.Join(abs, "objects"),
		indexPath: filepath.Join(abs, "index.json"),
	}, nil
}

func (s *store) ensure() error {
	return os.MkdirAll(s.objects, 0o755)
}

func (s *store) objectDir(key string) string {
	return filepath.Join(s.objects, key)
}

func (s *store) loadIndex() (Index, error) {
	idx := Index{Version: 1, Files: map[string]IndexEntry{}}
	b, err := os.ReadFile(s.indexPath)
	if errors.Is(err, os.ErrNotExist) {
		return idx, nil
	}
	if err != nil {
		return idx, err
	}
	if err := json.Unmarshal(b, &idx); err != nil {
		return idx, err
	}
	if idx.Files == nil {
		idx.Files = map[string]IndexEntry{}
	}
	if idx.Version == 0 {
		idx.Version = 1
	}
	return idx, nil
}

func (s *store) saveIndex(idx Index) error {
	if err := s.ensure(); err != nil {
		return err
	}
	return writeJSONAtomic(s.indexPath, idx, 0o644)
}

func (s *store) loadMeta(path string) (Meta, string, error) {
	abs, err := cleanAbs(path)
	if err != nil {
		return Meta{}, "", err
	}
	key := keyForPath(abs)
	meta := Meta{Path: abs, Key: key}
	b, err := os.ReadFile(filepath.Join(s.objectDir(key), "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return meta, key, nil
	}
	if err != nil {
		return meta, key, err
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return meta, key, err
	}
	if meta.Path == "" {
		meta.Path = abs
	}
	if meta.Key == "" {
		meta.Key = key
	}
	return meta, key, nil
}

func (s *store) saveMeta(meta Meta) error {
	dir := s.objectDir(meta.Key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dir, "meta.json"), meta, 0o644)
}

func snapshotFile(path string, opts options) (snapshotResult, error) {
	s, err := newStore()
	if err != nil {
		return snapshotResult{}, err
	}
	abs, err := cleanAbs(path)
	if err != nil {
		return snapshotResult{}, err
	}
	live, err := readLive(abs, opts.NoDeref, opts.MaxSize, opts.Yes)
	if err != nil {
		return snapshotResult{}, err
	}
	if live.size > opts.MaxSize {
		fmt.Fprintf(os.Stderr, "warning: %s is %s; storing because --yes was provided\n", compactPath(abs), humanSize(live.size))
	}

	meta, key, err := s.loadMeta(abs)
	if err != nil {
		return snapshotResult{}, err
	}
	if len(meta.Versions) > 0 {
		last := meta.Versions[len(meta.Versions)-1]
		if last.SHA == live.sha && last.Kind == live.kind && last.LinkTarget == live.linkTarget {
			return snapshotResult{
				Changed: false,
				Version: last,
				Message: fmt.Sprintf("no change since v%d.", last.V),
			}, nil
		}
	}

	if err := s.ensure(); err != nil {
		return snapshotResult{}, err
	}
	dir := s.objectDir(key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return snapshotResult{}, err
	}

	next := nextVersion(meta.Versions)
	stored := fmt.Sprintf("v%d.zst", next)
	payload := live.data
	compressed := !opts.NoCompress
	if compressed {
		payload, err = zstdEncode(live.data)
		if err != nil {
			return snapshotResult{}, err
		}
	} else {
		stored = fmt.Sprintf("v%d.raw", next)
	}
	if err := writeFileAtomic(filepath.Join(dir, stored), payload, 0o600); err != nil {
		return snapshotResult{}, err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if meta.CreatedAt == "" {
		meta.CreatedAt = now
	}
	meta.Path = abs
	meta.Key = key
	meta.UpdatedAt = now
	v := Version{
		V:          next,
		Time:       now,
		Size:       live.size,
		SHA:        live.sha,
		Note:       opts.Note,
		Mode:       live.mode,
		Stored:     stored,
		Compressed: compressed,
		Kind:       live.kind,
		LinkTarget: live.linkTarget,
	}
	meta.Versions = append(meta.Versions, v)

	pruned := 0
	if opts.Keep > 0 {
		pruned, err = pruneMeta(s, &meta, opts.Keep)
		if err != nil {
			return snapshotResult{}, err
		}
	}

	if err := s.saveMeta(meta); err != nil {
		return snapshotResult{}, err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return snapshotResult{}, err
	}
	updateIndex(&idx, meta)
	if err := s.saveIndex(idx); err != nil {
		return snapshotResult{}, err
	}

	msg := fmt.Sprintf("saved %s as v%d (%s).", compactPath(abs), v.V, humanSize(v.Size))
	if opts.Note != "" {
		msg = fmt.Sprintf("%s note: %q.", strings.TrimSuffix(msg, "."), opts.Note)
	}
	if pruned > 0 {
		msg = fmt.Sprintf("%s pruned %d old version(s).", msg, pruned)
	}
	return snapshotResult{Changed: true, Version: v, Message: msg}, nil
}

func listFile(path string, opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, _, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	if len(meta.Versions) == 0 {
		return fmt.Errorf("no history for %s", path)
	}
	if opts.JSON {
		return printJSON(meta)
	}

	currentSHA := ""
	if live, err := readLive(meta.Path, false, 1<<62, true); err == nil {
		currentSHA = live.sha
	}
	for i := len(meta.Versions) - 1; i >= 0; i-- {
		v := meta.Versions[i]
		t := formatTime(v.Time)
		marker := ""
		if currentSHA != "" && currentSHA == v.SHA {
			marker = "   (current"
			if v.Note != "" {
				marker += ", " + strconv.Quote(v.Note)
			}
			marker += ")"
		} else if v.Note != "" {
			marker = "   " + strconv.Quote(v.Note)
		}
		fmt.Printf("v%-3d %s   %8s%s\n", v.V, t, humanSize(v.Size), marker)
	}
	return nil
}

func listAll(opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	entries := make([]IndexEntry, 0, len(idx.Files))
	for _, entry := range idx.Files {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if opts.JSON {
		return printJSON(entries)
	}
	if len(entries) == 0 {
		fmt.Println("no tracked files.")
		return nil
	}
	for _, entry := range entries {
		last := "never"
		if t, err := time.Parse(time.RFC3339, entry.LastTime); err == nil {
			last = ago(t)
		}
		fmt.Printf("%-44s %3d versions   last %s\n", compactPath(entry.Path), entry.Versions, last)
	}
	return nil
}

func diffFile(path string, specs []string, opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, _, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	if len(meta.Versions) == 0 {
		return fmt.Errorf("no history for %s", path)
	}

	leftV, err := findVersion(meta.Versions, "")
	if err != nil {
		return err
	}
	rightName := "current"
	var right []byte
	var rightTime string

	switch len(specs) {
	case 0:
	case 1:
		leftV, err = findVersion(meta.Versions, specs[0])
		if err != nil {
			return err
		}
	case 2:
		leftV, err = findVersion(meta.Versions, specs[0])
		if err != nil {
			return err
		}
		rightV, err := findVersion(meta.Versions, specs[1])
		if err != nil {
			return err
		}
		right, err = s.readVersion(meta, rightV)
		if err != nil {
			return err
		}
		rightName = fmt.Sprintf("v%d", rightV.V)
		rightTime = rightV.Time
	default:
		return errors.New("too many diff arguments")
	}

	left, err := s.readVersion(meta, leftV)
	if err != nil {
		return err
	}
	leftName := fmt.Sprintf("v%d", leftV.V)

	if len(specs) < 2 {
		live, err := readLive(meta.Path, false, 1<<62, true)
		if err != nil {
			return fmt.Errorf("read current file: %w", err)
		}
		right = live.data
	}

	stat := diffStat(left, right)
	if opts.JSON {
		return printJSON(map[string]any{
			"file":    meta.Path,
			"from":    leftName,
			"to":      rightName,
			"added":   stat.added,
			"removed": stat.removed,
			"binary":  stat.binary,
			"differs": !bytes.Equal(left, right),
		})
	}
	if bytes.Equal(left, right) {
		fmt.Println("no differences.")
		return nil
	}
	if stat.binary {
		fmt.Printf("binary differs: %s %s -> %s\n", compactPath(meta.Path), leftName, rightName)
		return nil
	}

	fmt.Printf("--- %s %s%s\n", compactPath(meta.Path), leftName, timeSuffix(leftV.Time))
	fmt.Printf("+++ %s %s%s\n", compactPath(meta.Path), rightName, timeSuffix(rightTime))
	fmt.Print(unifiedDiff(string(left), string(right), opts.FullDiff))
	return nil
}

func restoreFile(path, spec string, opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, _, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	if len(meta.Versions) == 0 {
		return fmt.Errorf("no history for %s", path)
	}
	if spec == "" {
		picked, err := pickVersion(meta)
		if err != nil {
			return err
		}
		spec = picked
	}
	v, err := findVersion(meta.Versions, spec)
	if err != nil {
		return err
	}

	needSave := false
	if live, err := readLive(meta.Path, false, 1<<62, true); err == nil {
		needSave = !shaInVersions(live.sha, meta.Versions)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if !opts.Yes {
		fmt.Printf("restore %s from v%d? This overwrites the live file", compactPath(meta.Path), v.V)
		if needSave {
			fmt.Print(" after saving current as a new version")
		}
		fmt.Print(". [y/N] ")
		ok, err := readYes()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted.")
			return nil
		}
	}

	savedVersion := 0
	if needSave {
		saveOpts := opts
		saveOpts.Note = "auto before restore"
		saveOpts.Keep = 0
		saveOpts.Yes = true
		res, err := snapshotFile(meta.Path, saveOpts)
		if err != nil {
			return err
		}
		if res.Changed {
			savedVersion = res.Version.V
			meta, _, err = s.loadMeta(meta.Path)
			if err != nil {
				return err
			}
		}
	}

	data, err := s.readVersion(meta, v)
	if err != nil {
		return err
	}
	if err := writeRestored(meta.Path, v, data); err != nil {
		return err
	}

	if opts.JSON {
		return printJSON(map[string]any{
			"file":          meta.Path,
			"restored":      v.V,
			"saved_current": savedVersion,
		})
	}
	if savedVersion > 0 {
		fmt.Printf("current %s differs from all snapshots.\n  -> saving current as v%d first\n", compactPath(meta.Path), savedVersion)
	}
	fmt.Printf("restored %s <- v%d", compactPath(meta.Path), v.V)
	if savedVersion > 0 {
		fmt.Printf("   (undo: bak restore %s v%d)", shellPath(meta.Path), savedVersion)
	}
	fmt.Println()
	return nil
}

func showVersion(path, spec string) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, _, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	v, err := findVersion(meta.Versions, spec)
	if err != nil {
		return err
	}
	data, err := s.readVersion(meta, v)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func removeHistory(path string, opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, key, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	if len(meta.Versions) == 0 {
		return fmt.Errorf("no history for %s", path)
	}
	if !opts.Yes {
		fmt.Printf("drop all %d version(s) for %s? [y/N] ", len(meta.Versions), compactPath(meta.Path))
		ok, err := readYes()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted.")
			return nil
		}
	}
	if err := os.RemoveAll(s.objectDir(key)); err != nil {
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	delete(idx.Files, key)
	if err := s.saveIndex(idx); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(map[string]any{"removed": meta.Path, "versions": len(meta.Versions)})
	}
	fmt.Printf("removed history for %s (%d version(s)).\n", compactPath(meta.Path), len(meta.Versions))
	return nil
}

func pruneFile(path string, opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	meta, _, err := s.loadMeta(path)
	if err != nil {
		return err
	}
	if len(meta.Versions) == 0 {
		return fmt.Errorf("no history for %s", path)
	}
	if len(meta.Versions) <= opts.Keep {
		fmt.Printf("nothing to prune; %s has %d version(s).\n", compactPath(meta.Path), len(meta.Versions))
		return nil
	}
	toDrop := len(meta.Versions) - opts.Keep
	if !opts.Yes {
		fmt.Printf("prune %d old version(s) for %s? [y/N] ", toDrop, compactPath(meta.Path))
		ok, err := readYes()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted.")
			return nil
		}
	}
	dropped, err := pruneMeta(s, &meta, opts.Keep)
	if err != nil {
		return err
	}
	if err := s.saveMeta(meta); err != nil {
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	updateIndex(&idx, meta)
	if err := s.saveIndex(idx); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(map[string]any{"file": meta.Path, "pruned": dropped, "kept": len(meta.Versions)})
	}
	fmt.Printf("pruned %d old version(s); kept %d.\n", dropped, len(meta.Versions))
	return nil
}

func gc(opts options) error {
	s, err := newStore()
	if err != nil {
		return err
	}
	idx, err := s.loadIndex()
	if err != nil {
		return err
	}
	var deleted []IndexEntry
	for key, entry := range idx.Files {
		if _, err := os.Lstat(entry.Path); errors.Is(err, os.ErrNotExist) {
			entry.Key = key
			deleted = append(deleted, entry)
		}
	}
	if len(deleted) == 0 {
		fmt.Println("nothing to collect.")
		return nil
	}
	sort.Slice(deleted, func(i, j int) bool { return deleted[i].Path < deleted[j].Path })
	if !opts.Yes {
		fmt.Printf("purge histories for %d deleted file(s)? [y/N] ", len(deleted))
		ok, err := readYes()
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted.")
			return nil
		}
	}
	for _, entry := range deleted {
		if err := os.RemoveAll(s.objectDir(entry.Key)); err != nil {
			return err
		}
		delete(idx.Files, entry.Key)
	}
	if err := s.saveIndex(idx); err != nil {
		return err
	}
	if opts.JSON {
		return printJSON(map[string]any{"purged": deleted})
	}
	for _, entry := range deleted {
		fmt.Printf("purged %s\n", compactPath(entry.Path))
	}
	return nil
}

func readLive(path string, noDeref bool, maxSize int64, allowLarge bool) (fileContent, error) {
	if noDeref {
		fi, err := os.Lstat(path)
		if err != nil {
			return fileContent{}, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fileContent{}, err
			}
			data := []byte(target)
			return fileContent{
				data:       data,
				mode:       uint32(fi.Mode().Perm()),
				size:       int64(len(data)),
				sha:        shaHex(data),
				kind:       "symlink",
				linkTarget: target,
			}, nil
		}
	}

	fi, err := os.Stat(path)
	if err != nil {
		return fileContent{}, err
	}
	if !fi.Mode().IsRegular() {
		return fileContent{}, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > maxSize && !allowLarge {
		return fileContent{}, fmt.Errorf("%s is %s; pass --max-size larger or --yes to snapshot anyway", compactPath(path), humanSize(fi.Size()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileContent{}, err
	}
	return fileContent{
		data: data,
		mode: uint32(fi.Mode().Perm()),
		size: int64(len(data)),
		sha:  shaHex(data),
		kind: "file",
	}, nil
}

func (s *store) readVersion(meta Meta, v Version) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(s.objectDir(meta.Key), v.Stored))
	if err != nil {
		return nil, err
	}
	if !v.Compressed {
		return b, nil
	}
	return zstdDecode(b)
}

func writeRestored(path string, v Version, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if v.Kind == "symlink" {
		if fi, err := os.Lstat(path); err == nil {
			if fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("%s is a directory", path)
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(v.LinkTarget, path)
	}
	mode := os.FileMode(v.Mode)
	if mode == 0 {
		mode = 0o600
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(path, mode)
	}
	return nil
}

func findVersion(versions []Version, spec string) (Version, error) {
	if len(versions) == 0 {
		return Version{}, errors.New("no versions")
	}
	if spec == "" || spec == "latest" {
		return versions[len(versions)-1], nil
	}
	spec = strings.TrimPrefix(spec, "v")
	n, err := strconv.Atoi(spec)
	if err != nil {
		return Version{}, fmt.Errorf("invalid version %q", spec)
	}
	for _, v := range versions {
		if v.V == n {
			return v, nil
		}
	}
	return Version{}, fmt.Errorf("version v%d not found", n)
}

func pickVersion(meta Meta) (string, error) {
	fmt.Printf("versions for %s:\n", compactPath(meta.Path))
	for i := len(meta.Versions) - 1; i >= 0; i-- {
		v := meta.Versions[i]
		note := ""
		if v.Note != "" {
			note = " " + strconv.Quote(v.Note)
		}
		fmt.Printf("  v%d  %s  %s%s\n", v.V, formatTime(v.Time), humanSize(v.Size), note)
	}
	fmt.Print("restore version: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("no version selected")
	}
	return line, nil
}

func readYes() (bool, error) {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes", nil
}

func pruneMeta(s *store, meta *Meta, keep int) (int, error) {
	if keep <= 0 || len(meta.Versions) <= keep {
		return 0, nil
	}
	drop := meta.Versions[:len(meta.Versions)-keep]
	meta.Versions = append([]Version(nil), meta.Versions[len(meta.Versions)-keep:]...)
	for _, v := range drop {
		if v.Stored != "" {
			if err := os.Remove(filepath.Join(s.objectDir(meta.Key), v.Stored)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
		}
	}
	if len(meta.Versions) > 0 {
		meta.UpdatedAt = meta.Versions[len(meta.Versions)-1].Time
	}
	return len(drop), nil
}

func updateIndex(idx *Index, meta Meta) {
	if idx.Files == nil {
		idx.Files = map[string]IndexEntry{}
	}
	last := ""
	if len(meta.Versions) > 0 {
		last = meta.Versions[len(meta.Versions)-1].Time
	}
	idx.Files[meta.Key] = IndexEntry{
		Path:     meta.Path,
		Key:      meta.Key,
		Versions: len(meta.Versions),
		LastTime: last,
	}
}

func nextVersion(versions []Version) int {
	max := 0
	for _, v := range versions {
		if v.V > max {
			max = v.V
		}
	}
	return max + 1
}

func shaInVersions(sha string, versions []Version) bool {
	for _, v := range versions {
		if v.SHA == sha {
			return true
		}
	}
	return false
}

type statResult struct {
	added   int
	removed int
	binary  bool
}

func diffStat(a, b []byte) statResult {
	if isBinary(a) || isBinary(b) {
		return statResult{binary: true}
	}
	ops := diffOps(splitLines(string(a)), splitLines(string(b)))
	var st statResult
	for _, op := range ops {
		switch op.kind {
		case '+':
			st.added++
		case '-':
			st.removed++
		}
	}
	return st
}

func unifiedDiff(a, b string, full bool) string {
	left := splitLines(a)
	right := splitLines(b)
	ops := diffOps(left, right)
	if !full {
		return compactUnifiedDiff(ops, 3)
	}
	return renderDiffHunk(ops, 0, len(ops), len(left), len(right))
}

func compactUnifiedDiff(ops []diffOp, context int) string {
	var changes []int
	for i, op := range ops {
		if op.kind != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}

	var buf strings.Builder
	for i := 0; i < len(changes); {
		start := maxInt(0, changes[i]-context)
		end := minInt(len(ops), changes[i]+context+1)
		i++
		for i < len(changes) {
			nextStart := maxInt(0, changes[i]-context)
			if nextStart > end {
				break
			}
			end = maxInt(end, minInt(len(ops), changes[i]+context+1))
			i++
		}
		buf.WriteString(renderDiffHunk(ops, start, end, -1, -1))
	}
	return buf.String()
}

func renderDiffHunk(ops []diffOp, start, end, forcedOldCount, forcedNewCount int) string {
	oldStart, newStart := 1, 1
	for _, op := range ops[:start] {
		switch op.kind {
		case ' ':
			oldStart++
			newStart++
		case '-':
			oldStart++
		case '+':
			newStart++
		}
	}

	oldCount, newCount := 0, 0
	for _, op := range ops[start:end] {
		switch op.kind {
		case ' ':
			oldCount++
			newCount++
		case '-':
			oldCount++
		case '+':
			newCount++
		}
	}
	if forcedOldCount >= 0 {
		oldCount = forcedOldCount
	}
	if forcedNewCount >= 0 {
		newCount = forcedNewCount
	}

	var buf strings.Builder
	fmt.Fprintf(&buf, "@@ -%s +%s @@\n", diffRange(oldStart, oldCount), diffRange(newStart, newCount))
	for _, op := range ops[start:end] {
		buf.WriteByte(op.kind)
		buf.WriteString(op.line)
		if !strings.HasSuffix(op.line, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.String()
}

func diffRange(start, count int) string {
	if count == 0 {
		if start > 0 {
			start--
		}
		return fmt.Sprintf("%d,0", start)
	}
	if count == 1 {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

type diffOp struct {
	kind byte
	line string
}

func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, diffOp{' ', a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, diffOp{'-', a[i]})
			i++
		} else {
			ops = append(ops, diffOp{'+', b[j]})
			j++
		}
	}
	for i < n {
		ops = append(ops, diffOp{'-', a[i]})
		i++
	}
	for j < m {
		ops = append(ops, diffOp{'+', b[j]})
		j++
	}
	return ops
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.SplitAfter(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func isBinary(b []byte) bool {
	if bytes.IndexByte(b, 0) >= 0 {
		return true
	}
	if len(b) == 0 {
		return false
	}
	sample := b
	if len(sample) > 8000 {
		sample = sample[:8000]
	}
	return !utf8.Valid(sample)
}

func zstdEncode(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func zstdDecode(b []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func writeJSONAtomic(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b, mode)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cleanAbs(path string) (string, error) {
	path = expandHome(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func expandHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func keyForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func parseSize(s string) (int64, error) {
	raw := strings.TrimSpace(strings.ToLower(s))
	mult := int64(1)
	for _, suffix := range []struct {
		s string
		m int64
	}{
		{"gb", 1024 * 1024 * 1024},
		{"g", 1024 * 1024 * 1024},
		{"mb", 1024 * 1024},
		{"m", 1024 * 1024},
		{"kb", 1024},
		{"k", 1024},
		{"b", 1},
	} {
		if strings.HasSuffix(raw, suffix.s) {
			mult = suffix.m
			raw = strings.TrimSpace(strings.TrimSuffix(raw, suffix.s))
			break
		}
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return int64(n * float64(mult)), nil
}

func humanSize(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	if f >= 10 {
		return fmt.Sprintf("%.0f %s", f, units[i])
	}
	return fmt.Sprintf("%.1f %s", f, units[i])
}

func compactPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil {
		if path == home {
			return "~"
		}
		prefix := home + string(os.PathSeparator)
		if strings.HasPrefix(path, prefix) {
			return "~/" + strings.TrimPrefix(path, prefix)
		}
	}
	return path
}

func shellPath(path string) string {
	return strconv.Quote(compactPath(path))
}

func formatTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func timeSuffix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + formatTime(s) + ")"
}

func ago(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}
