package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var extToLang = map[string]string{
	".py": "Python", ".pyw": "Python",
	".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript",
	".java": "Java",
	".c":    "C", ".h": "C",
	".cpp": "C++", ".cc": "C++", ".cxx": "C++", ".hpp": "C++",
	".cs":    "C#",
	".go":    "Go",
	".rs":    "Rust",
	".rb":    "Ruby",
	".php":   "PHP",
	".swift": "Swift",
	".kt":    "Kotlin", ".kts": "Kotlin",
	".scala": "Scala",
	".r":     "R",
	".sh":    "Shell", ".bash": "Shell", ".zsh": "Shell",
	".html": "HTML", ".htm": "HTML",
	".css": "CSS", ".scss": "CSS", ".sass": "CSS", ".less": "CSS",
	".json": "JSON",
	".yaml": "YAML", ".yml": "YAML",
	".toml": "TOML",
	".xml":  "XML",
	".sql":  "SQL",
	".md":   "Markdown", ".mdx": "Markdown",
	".lua":  "Lua",
	".dart": "Dart",
	".ex":   "Elixir", ".exs": "Elixir",
	".erl": "Erlang",
	".hs":  "Haskell",
	".ml":  "OCaml", ".mli": "OCaml",
	".tf": "Terraform", ".tfvars": "Terraform",
	".vue":    "Vue",
	".svelte": "Svelte",
}

var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "__pycache__": true,
	".mypy_cache": true, ".pytest_cache": true,
	"dist": true, "build": true, ".next": true, ".nuxt": true,
	"venv": true, ".venv": true, "env": true,
	"vendor": true, "target": true,
	".idea": true, ".vscode": true,
	"coverage": true, ".tox": true,
}

type FileStats struct {
	Rel, Dir, Name, Lang string
	Code, Blank, Total   int
}

type DirStats struct {
	Name                      string
	Files, Code, Blank, Total int
}

type LangStats struct {
	Name                      string
	Files, Code, Blank, Total int
}

// ── .gitignore support ────────────────────────────────────────────────────────

// gitignorePattern holds one parsed pattern from a .gitignore file.
type gitignorePattern struct {
	raw      string // original text for display
	negative bool   // lines starting with !
	dirOnly  bool   // lines ending with /
	rooted   bool   // lines containing / (other than trailing)
	segments []string
}

// parseGitignore reads a .gitignore file and returns compiled patterns.
// baseDir is the directory that contains the .gitignore (used for rooted matches).
func parseGitignore(path string) []gitignorePattern {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var pats []gitignorePattern
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		// strip inline comments
		if i := strings.Index(line, " #"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimRight(line, " \t")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		p := gitignorePattern{raw: line}

		if strings.HasPrefix(line, "!") {
			p.negative = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		// rooted = slash appears anywhere except at the very end (already stripped)
		// or at the very start
		withoutLeadSlash := strings.TrimPrefix(line, "/")
		if strings.Contains(withoutLeadSlash, "/") || strings.HasPrefix(line, "/") {
			p.rooted = true
		}
		line = strings.TrimPrefix(line, "/")
		p.segments = strings.Split(line, "/")
		pats = append(pats, p)
	}
	return pats
}

// matchesGitignore checks whether relPath (relative to the .gitignore's dir,
// using forward slashes) is ignored by the given patterns.
// isDir should be true when checking a directory entry.
func matchesGitignore(pats []gitignorePattern, relPath string, isDir bool) bool {
	ignored := false
	parts := strings.Split(filepath.ToSlash(relPath), "/")

	for _, p := range pats {
		if p.dirOnly && !isDir {
			continue
		}
		var matched bool
		if p.rooted {
			// must match from the root
			matched = matchSegments(p.segments, parts)
		} else {
			// can match any suffix
			matched = matchAnySuffix(p.segments, parts)
		}
		if matched {
			if p.negative {
				ignored = false
			} else {
				ignored = true
			}
		}
	}
	return ignored
}

// matchSegments matches pattern segments against path parts from the start.
func matchSegments(pat, parts []string) bool {
	if len(pat) == 0 {
		return true
	}
	if len(pat) > len(parts) {
		return false
	}
	for i, seg := range pat {
		ok, _ := filepath.Match(seg, parts[i])
		if !ok {
			return false
		}
	}
	return true
}

// matchAnySuffix tries matchSegments at every starting position.
func matchAnySuffix(pat, parts []string) bool {
	for start := 0; start <= len(parts)-len(pat); start++ {
		if matchSegments(pat, parts[start:]) {
			return true
		}
	}
	return false
}

// gitignoreCache maps a directory path to its parsed patterns.
// We load the .gitignore for each directory we enter.
type gitignoreCache struct {
	root string
	data map[string][]gitignorePattern
}

func newGitignoreCache(root string) *gitignoreCache {
	return &gitignoreCache{root: root, data: map[string][]gitignorePattern{}}
}

func (gc *gitignoreCache) load(dir string) []gitignorePattern {
	if pats, ok := gc.data[dir]; ok {
		return pats
	}
	pats := parseGitignore(filepath.Join(dir, ".gitignore"))
	gc.data[dir] = pats
	return pats
}

// isIgnored checks whether a path is ignored by any .gitignore from root down to its parent.
func (gc *gitignoreCache) isIgnored(absPath string, isDir bool) bool {
	rel, err := filepath.Rel(gc.root, absPath)
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")

	// Walk from root down, checking each ancestor's .gitignore against the remaining relative path.
	cur := gc.root
	for i, part := range parts {
		_ = part
		pats := gc.load(cur)
		if len(pats) > 0 {
			suffix := strings.Join(parts[i:], "/")
			entryIsDir := isDir || i < len(parts)-1
			if matchesGitignore(pats, suffix, entryIsDir) {
				return true
			}
		}
		if i < len(parts)-1 {
			cur = filepath.Join(cur, parts[i])
		}
	}
	return false
}

// ── config ────────────────────────────────────────────────────────────────────

type config struct {
	root        string
	topN        int
	lang        string
	dirs        bool
	langs       bool
	all         bool
	noColor     bool
	listLangs   bool
	noGitignore bool
}

func parseArgs() config {
	cfg := config{root: ".", topN: 15}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dirs" || a == "-d":
			cfg.dirs = true
		case a == "--langs":
			cfg.langs = true
		case a == "--all" || a == "-a":
			cfg.all = true
		case a == "--no-color":
			cfg.noColor = true
		case a == "--list-langs":
			cfg.listLangs = true
		case a == "--no-gitignore":
			cfg.noGitignore = true
		case a == "--help" || a == "-h":
			printHelp()
			os.Exit(0)
		case strings.HasPrefix(a, "--top="):
			cfg.topN, _ = strconv.Atoi(strings.TrimPrefix(a, "--top="))
		case (a == "--top" || a == "-n") && i+1 < len(args):
			cfg.topN, _ = strconv.Atoi(args[i+1])
			i++
		case strings.HasPrefix(a, "--lang="):
			cfg.lang = strings.TrimPrefix(a, "--lang=")
		case (a == "--lang" || a == "-l") && i+1 < len(args):
			cfg.lang = args[i+1]
			i++
		case !strings.HasPrefix(a, "-"):
			cfg.root = a
		}
	}
	return cfg
}

func printHelp() {
	fmt.Print(`loc — Lines of Code explorer

Usage:
  loc [path] [flags]

Flags:
  --top N, -n N      number of results to show (default 15)
  --lang LANG, -l    filter by language, e.g. Go, Python, TypeScript
  --dirs, -d         show directory-level stats
  --langs            show language breakdown only
  --all, -a          show files + dirs + language breakdown
  --no-gitignore     include files that .gitignore would exclude
  --no-color         disable colour output (auto-disabled when piping)
  --list-langs       list all supported languages and exit
  --help, -h         show this help

Examples:
  loc .                        analyse current directory
  loc ./src --top 20           top 20 files
  loc . --dirs                 directory-level breakdown
  loc . --lang Go              only Go files
  loc . --all                  files + dirs + language summary
  loc . --no-gitignore         ignore .gitignore rules
  loc . --no-color             plain output, safe for piping
  loc . --list-langs           list all 33 supported languages
`)
}

// ── ANSI ─────────────────────────────────────────────────────────────────────

var useColor = true

func col(s string, code string) string {
	if !useColor {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func bold(s string) string    { return col(s, "1") }
func dim(s string) string     { return col(s, "2") }
func cyan(s string) string    { return col(s, "36") }
func green(s string) string   { return col(s, "32") }
func yellow(s string) string  { return col(s, "33") }
func blue(s string) string    { return col(s, "34") }
func magenta(s string) string { return col(s, "35") }
func white(s string) string   { return col(s, "37") }

func separator() { fmt.Println(dim(strings.Repeat("─", 80))) }

func human(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return strconv.Itoa(n)
}

func bar(value, maxVal, width int) string {
	if maxVal == 0 {
		return strings.Repeat("░", width)
	}
	filled := value * width / maxVal
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-(n-1):]
}

// ── Analysis ──────────────────────────────────────────────────────────────────

func countLines(path string) (total, code, blank int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		total++
		if strings.TrimSpace(sc.Text()) == "" {
			blank++
		}
	}
	code = total - blank
	return
}

func collect(root, langFilter string, noGitignore bool) []FileStats {
	var files []FileStats
	root, _ = filepath.Abs(root)

	var gi *gitignoreCache
	if !noGitignore {
		gi = newGitignoreCache(root)
	}

	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if skipDirs[n] || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			if gi != nil && gi.isIgnored(path, true) {
				return filepath.SkipDir
			}
			return nil
		}

		// Check gitignore for files
		if gi != nil && gi.isIgnored(path, false) {
			return nil
		}

		ext := filepath.Ext(d.Name())
		lang, ok := extToLang[strings.ToLower(ext)]
		if !ok {
			lang, ok = extToLang[ext]
		}
		if !ok {
			return nil
		}
		if langFilter != "" && !strings.EqualFold(lang, langFilter) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.Dir(rel)
		total, code, blank := countLines(path)
		files = append(files, FileStats{
			Rel: rel, Dir: dir, Name: d.Name(), Lang: lang,
			Code: code, Blank: blank, Total: total,
		})
		return nil
	})
	return files
}

// ── Views ─────────────────────────────────────────────────────────────────────

func header(title string) {
	fmt.Println()
	fmt.Println("  " + bold(cyan(title)))
	separator()
}

func showSummary(files []FileStats, root string) {
	tc, tb, tt := 0, 0, 0
	langs := map[string]bool{}
	for _, f := range files {
		tc += f.Code
		tb += f.Blank
		tt += f.Total
		langs[f.Lang] = true
	}
	fmt.Println()
	fmt.Printf("  %s\n", bold("Project: "+root))
	separator()
	fmt.Printf("  %-30s %s\n", dim("Files analysed"), bold(white(human(len(files)))))
	fmt.Printf("  %-30s %s\n", dim("Languages detected"), bold(white(strconv.Itoa(len(langs)))))
	fmt.Printf("  %-30s %s\n", dim("Lines of code"), bold(green(human(tc))))
	fmt.Printf("  %-30s %s\n", dim("Blank lines"), dim(human(tb)))
	fmt.Printf("  %-30s %s\n", dim("Total lines"), bold(white(human(tt))))
}

func showFiles(files []FileStats, topN int) {
	sorted := make([]FileStats, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code > sorted[j].Code })
	if topN < len(sorted) {
		sorted = sorted[:topN]
	}
	maxVal := 1
	if len(sorted) > 0 {
		maxVal = sorted[0].Code
	}
	header(fmt.Sprintf("Top %d files by lines of code", len(sorted)))
	fmt.Printf("  %-47s %-14s %7s %7s %7s  %s\n",
		bold("File"), bold("Lang"), bold("Code"), bold("Blank"), bold("Total"), bold("Bar"))
	separator()
	for _, f := range sorted {
		fmt.Printf("  %-47s %-14s %7s %7s %7s  %s\n",
			cyan(truncate(f.Rel, 45)),
			magenta(f.Lang),
			green(human(f.Code)),
			dim(human(f.Blank)),
			white(human(f.Total)),
			yellow(bar(f.Code, maxVal, 22)),
		)
	}
}

func showDirs(files []FileStats, topN int) {
	dm := map[string]*DirStats{}
	for _, f := range files {
		d := f.Dir
		if d == "" || d == "." {
			d = "(root)"
		}
		if _, ok := dm[d]; !ok {
			dm[d] = &DirStats{Name: d}
		}
		dm[d].Files++
		dm[d].Code += f.Code
		dm[d].Blank += f.Blank
		dm[d].Total += f.Total
	}
	sorted := make([]*DirStats, 0, len(dm))
	for _, v := range dm {
		sorted = append(sorted, v)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code > sorted[j].Code })
	if topN < len(sorted) {
		sorted = sorted[:topN]
	}
	maxVal := 1
	if len(sorted) > 0 {
		maxVal = sorted[0].Code
	}
	header(fmt.Sprintf("Top %d directories by lines of code", len(sorted)))
	fmt.Printf("  %-47s %6s %8s %7s %8s  %s\n",
		bold("Directory"), bold("Files"), bold("Code"), bold("Blank"), bold("Total"), bold("Bar"))
	separator()
	for _, d := range sorted {
		fmt.Printf("  %-47s %6s %8s %7s %8s  %s\n",
			blue(truncate(d.Name, 45)),
			dim(strconv.Itoa(d.Files)),
			green(human(d.Code)),
			dim(human(d.Blank)),
			white(human(d.Total)),
			yellow(bar(d.Code, maxVal, 22)),
		)
	}
}

func showLangs(files []FileStats) {
	lm := map[string]*LangStats{}
	grand := 0
	for _, f := range files {
		if _, ok := lm[f.Lang]; !ok {
			lm[f.Lang] = &LangStats{Name: f.Lang}
		}
		lm[f.Lang].Files++
		lm[f.Lang].Code += f.Code
		lm[f.Lang].Blank += f.Blank
		lm[f.Lang].Total += f.Total
		grand += f.Code
	}
	sorted := make([]*LangStats, 0, len(lm))
	for _, v := range lm {
		sorted = append(sorted, v)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code > sorted[j].Code })
	maxVal := 1
	if len(sorted) > 0 {
		maxVal = sorted[0].Code
	}
	header("Lines of code by language")
	fmt.Printf("  %-18s %6s %8s %6s  %s\n",
		bold("Language"), bold("Files"), bold("Code"), bold("%"), bold("Bar"))
	separator()
	for _, l := range sorted {
		pct := 0.0
		if grand > 0 {
			pct = float64(l.Code) / float64(grand) * 100
		}
		fmt.Printf("  %-18s %6s %8s %6s  %s\n",
			magenta(l.Name),
			dim(strconv.Itoa(l.Files)),
			green(human(l.Code)),
			dim(fmt.Sprintf("%.1f%%", pct)),
			yellow(bar(l.Code, maxVal, 22)),
		)
	}
}

func listLangs() {
	seen := map[string]bool{}
	var ls []string
	for _, v := range extToLang {
		if !seen[v] {
			seen[v] = true
			ls = append(ls, v)
		}
	}
	sort.Strings(ls)
	fmt.Println(bold("Supported languages:"))
	for _, l := range ls {
		fmt.Println("  " + l)
	}
}

func isatty() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	cfg := parseArgs()

	if !isatty() || cfg.noColor {
		useColor = false
	}

	if cfg.listLangs {
		listLangs()
		return
	}

	info, err := os.Stat(cfg.root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if !info.IsDir() {
		ext := filepath.Ext(cfg.root)
		lang, ok := extToLang[strings.ToLower(ext)]
		if !ok {
			lang = "Unknown"
		}
		total, code, blank := countLines(cfg.root)
		fmt.Printf("\n  %s  %s\n", cyan(cfg.root), magenta(lang))
		separator()
		fmt.Printf("  Code lines : %s\n", bold(green(human(code))))
		fmt.Printf("  Blank lines: %s\n", dim(human(blank)))
		fmt.Printf("  Total lines: %s\n", bold(white(human(total))))
		fmt.Println()
		return
	}

	fmt.Printf(dim("  Scanning %s …\r"), cfg.root)
	files := collect(cfg.root, cfg.lang, cfg.noGitignore)
	fmt.Print(strings.Repeat(" ", 50) + "\r")

	if len(files) == 0 {
		msg := fmt.Sprintf("No recognised source files found in '%s'", cfg.root)
		if cfg.lang != "" {
			msg += fmt.Sprintf(" for language '%s'", cfg.lang)
		}
		fmt.Println(yellow("\n  " + msg + "."))
		fmt.Println(dim("  Run --list-langs to see all supported languages."))
		fmt.Println()
		return
	}

	showSummary(files, cfg.root)

	switch {
	case cfg.all:
		showFiles(files, cfg.topN)
		showDirs(files, cfg.topN)
		showLangs(files)
	case cfg.dirs:
		showDirs(files, cfg.topN)
	case cfg.langs:
		showLangs(files)
	default:
		showFiles(files, cfg.topN)
		showLangs(files)
	}

	fmt.Println()
}
