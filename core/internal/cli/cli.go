// Package cli implements the edelweissd command line.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/daemon"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/service"
	"github.com/Pukka922/edelweiss/core/internal/version"
)

const usageText = `Usage:
  edelweissd [serve] [--config FILE] [--dev]
  edelweissd ext list [--config FILE]
  edelweissd ext install <file|github-repo> [--id ID] [--yes] [--config FILE]
  edelweissd ext update [<id>|--all] [--yes] [--config FILE]
  edelweissd ext remove <id> [--config FILE]
  edelweissd service install
  edelweissd service uninstall [--purge]
  edelweissd version

GitHub repositories: owner/repo, github.com/owner/repo or https://github.com/owner/repo, each optionally
followed by @tag (default: the latest release).
`

// Main runs the command line and returns the exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return runServe(args, stderr)
	case "ext":
		return runExt(args, stdin, stdout, stderr)
	case "service":
		return runService(args, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "help":
		fmt.Fprint(stdout, usageText)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usageText)
		return 2
	}
}

func loadConfig(path string, stderr io.Writer) (*config.Config, error) {
	cfg, err := config.Load(path)
	if errors.Is(err, fs.ErrNotExist) && path == config.DefaultPath {
		fmt.Fprintf(stderr, "warning: %s not found, using defaults\n", path)
		return config.Default(), nil
	}
	return cfg, err
}

func runServe(args []string, stderr io.Writer) int {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	fset.SetOutput(stderr)
	configPath := fset.String("config", config.DefaultPath, "configuration file")
	dev := fset.Bool("dev", false, "development mode: debug logging, [dev] settings")
	if err := fset.Parse(args); err != nil {
		return 2
	}
	if fset.NArg() > 0 { // flag parsing stops at the first positional argument and would drop later flags
		fmt.Fprintf(stderr, "serve: unexpected argument %q\n\n%s", fset.Arg(0), usageText)
		return 2
	}
	cfg, err := loadConfig(*configPath, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	level := cfg.LogLevel()
	if *dev {
		level = slog.LevelDebug
	} else {
		cfg.Dev = config.Dev{} // dev settings only apply with --dev
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// After the first signal restore the default behaviour, so that a second
	// SIGINT/SIGTERM ends the process even while the shutdown is still draining.
	go func() { <-ctx.Done(); stop() }()
	if err := daemon.Run(ctx, cfg, logger); err != nil {
		logger.Error("edelweissd failed", "err", err)
		return 1
	}
	return 0
}

// parseInterspersed allows flags before and after positional arguments.
func parseInterspersed(fset *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fset.Parse(args); err != nil {
			return nil, err
		}
		args = fset.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// extArgs is the number of positional arguments of each "ext" subcommand (update: 0 with --all, else 1).
var extArgs = map[string][2]int{"list": {0, 0}, "install": {1, 1}, "update": {0, 1}, "remove": {1, 1}}

func runExt(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	// Validate the command line completely before the configuration is read or
	// anything is created on disk.
	sub := args[0]
	nargs, known := extArgs[sub]
	if !known {
		fmt.Fprintf(stderr, "unknown ext command %q\n\n%s", sub, usageText)
		return 2
	}
	fset := flag.NewFlagSet("ext "+sub, flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() { fmt.Fprint(stderr, usageText) } // an unknown flag shows the whole usage, like other mistakes
	configPath := fset.String("config", config.DefaultPath, "configuration file")
	var expectID string
	var yes, all bool
	if sub == "install" {
		fset.StringVar(&expectID, "id", "", "extension id (required if a release offers several)")
	}
	if sub == "install" || sub == "update" {
		fset.BoolVar(&yes, "yes", false, "do not ask for confirmation")
	}
	if sub == "update" {
		fset.BoolVar(&all, "all", false, "update every extension installed from GitHub")
	}
	pos, err := parseInterspersed(fset, args[1:])
	if err != nil {
		return 2
	}
	if len(pos) < nargs[0] || len(pos) > nargs[1] || (sub == "update" && (len(pos) == 1) == all) {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	var file string
	var repo *github.Source
	if sub == "install" {
		var ok bool
		if file, repo, ok = classifySource(pos[0], stderr); !ok {
			return 1
		}
	}

	cfg, err := loadConfig(*configPath, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := context.Background()
	switch {
	case sub == "list":
		return listExtensions(cfg, nil, stdout, stderr)
	case sub == "install" && repo != nil:
		return installFromGitHub(ctx, cfg, *repo, expectID, yes, stdin, stdout, stderr)
	case sub == "update":
		id := ""
		if len(pos) == 1 {
			id = pos[0]
		}
		return updateFromGitHub(ctx, cfg, id, yes, stdin, stdout, stderr)
	}
	mgr, code := openStore(cfg, stderr)
	if mgr == nil {
		return code
	}
	if sub == "install" {
		ext, err := mgr.InstallFile(ctx, file, extmgr.Source{Type: "file", Location: file}, expectID)
		if err != nil {
			reportFailure(stderr, "install", err)
			return 1
		}
		fmt.Fprintf(stdout, "installed %s %s\n", ext.Manifest.ID, ext.Manifest.Version)
		return 0
	}
	// "remove"
	dataDir := mgr.ExtDataDir(pos[0])
	// Lstat: a symlink in place of the directory counts too, even a dangling one
	_, statErr := os.Lstat(dataDir)
	err = mgr.Remove(pos[0])
	var dataErr *extmgr.DataNotRemovedError
	switch {
	case errors.Is(err, extmgr.ErrNotFound):
		fmt.Fprintf(stderr, "extension %q is not installed\n", pos[0])
		return 1
	case errors.As(err, &dataErr):
		// the extension is gone, there is nothing to retry: name what is left to delete by hand
		fmt.Fprintf(stdout, "removed %s\n", pos[0])
		fmt.Fprintf(stderr, "warning: the data directory %s could not be deleted: %v\n", dataErr.Dir, dataErr.Err)
		return 1
	case err != nil:
		fmt.Fprintf(stderr, "remove failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed %s\n", pos[0])
	if dataDir != "" && statErr == nil {
		fmt.Fprintf(stdout, "deleted %s\n", dataDir)
	}
	return 0
}

func runService(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	sub := args[0]
	fset := flag.NewFlagSet("service "+sub, flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() { fmt.Fprint(stderr, usageText) }
	purge := false
	if sub == "uninstall" {
		fset.BoolVar(&purge, "purge", false, "also delete /etc/conf.d/edelweiss, /etc/edelweiss and /var/lib/edelweiss")
	}
	pos, err := parseInterspersed(fset, args[1:])
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintf(stderr, "service %s must be run as root\n", sub)
		return 1
	}
	opts := service.Options{Root: "/", Out: stdout}
	if sub == "uninstall" {
		if err := service.Uninstall(opts, purge); err != nil {
			fmt.Fprintf(stderr, "service uninstall failed: %v\n", err)
			return 1
		}
		return 0
	}
	bin, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if strings.HasPrefix(bin, "/tmp/") {
		fmt.Fprintf(stderr, "warning: %s is below /tmp; the service needs the binary at a permanent location such as /usr/local/sbin\n", bin)
	}
	opts.Binary = bin
	if err := service.Install(opts); err != nil {
		fmt.Fprintf(stderr, "service install failed: %v\n", err)
		return 1
	}
	return 0
}

// extDataRoot is the directory below which extensions keep their data; a variable only so that tests can point it at a
// temporary directory.
var extDataRoot = extmgr.DefaultExtDataRoot

// openStore creates the state directories and opens the extension store with a manifest querier; on failure it
// reports the error and returns a nil manager and exit code 1.
func openStore(cfg *config.Config, stderr io.Writer) (*extmgr.Manager, int) {
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	q, err := extmgr.NewExecQuerier()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	mgr, err := extmgr.New(extmgr.Options{Dir: cfg.ExtensionsDir(), TmpDir: cfg.TmpDir(), Querier: q, ExtDataRoot: extDataRoot,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	if err := mgr.Load(context.Background()); err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 1
	}
	return mgr, 0
}

// listExtensions prints the installed extensions. It never creates the state
// directory: without an extension store there is nothing to list. packages
// checks the required Alpine packages; nil means the apk database. The manager
// logs its warnings to stderr: that is where the cause shows when the database
// cannot be read and every required package counts as missing.
func listExtensions(cfg *config.Config, packages extmgr.PackageChecker, stdout, stderr io.Writer) int {
	var exts []*extmgr.Extension
	missing := func(*extmgr.Extension) []string { return nil }
	switch _, err := os.Stat(cfg.ExtensionsDir()); {
	case err == nil:
		// Listing neither installs nor runs anything, so no manifest querier is needed.
		mgr, err := extmgr.New(extmgr.Options{Dir: cfg.ExtensionsDir(), TmpDir: cfg.TmpDir(), Packages: packages,
			Logger: slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))})
		if err == nil {
			err = mgr.Load(context.Background())
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		exts = mgr.List()
		missing = mgr.MissingPackages
	case !errors.Is(err, fs.ErrNotExist):
		fmt.Fprintln(stderr, err)
		return 1
	}
	printExtensions(stdout, exts, missing)
	return 0
}

// printExtensions writes the table of ext list, then a line for every extension with missing packages.
func printExtensions(w io.Writer, exts []*extmgr.Extension, missing func(*extmgr.Extension) []string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tVERSION\tSOURCE\tNAME")
	for _, e := range exts {
		fmt.Fprintf(tw, "%s\t%s\t%s:%s\t%s\n", e.Manifest.ID, e.Manifest.Version, e.Source.Type, e.Source.Location, e.Manifest.Name["en"])
	}
	_ = tw.Flush()
	for _, e := range exts {
		if m := missing(e); len(m) > 0 {
			fmt.Fprintf(w, "%s: missing packages: %s; install them as root: %s\n", e.Manifest.ID, strings.Join(m, ", "), apkAdd(m))
		}
	}
}

// reportFailure prints that op failed; for missing packages it adds the apk command that installs them.
func reportFailure(w io.Writer, op string, err error) {
	fmt.Fprintf(w, "%s failed: %v\n", op, err)
	var mp *extmgr.MissingPackagesError
	if errors.As(err, &mp) {
		fmt.Fprintf(w, "install them as root first: %s\n", apkAdd(mp.Packages))
	}
}

// apkAdd is the command that installs packages.
func apkAdd(packages []string) string { return "apk add " + strings.Join(packages, " ") }

// githubShorthand matches owner/repo and owner/repo@tag.
var githubShorthand = regexp.MustCompile(`^[\w.-]+/[\w.-]+(@\S+)?$`)

// looksLikeGitHub reports whether an install source names a GitHub repository
// (github.com/owner/repo, https://github.com/owner/repo, owner/repo[@tag])
// instead of a file. An explicit ./ or ../ path is always a file.
func looksLikeGitHub(src string) bool {
	if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		return false
	}
	return strings.HasPrefix(src, "github.com/") || strings.HasPrefix(src, "https://github.com/") || githubShorthand.MatchString(src)
}

// classifySource resolves the argument of "ext install": an existing regular file (returned as an absolute path) or
// a GitHub repository. Otherwise it reports why the source cannot be installed and returns ok=false. It only stats
// the path.
func classifySource(src string, stderr io.Writer) (file string, repo *github.Source, ok bool) {
	fi, err := os.Stat(src)
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist) && looksLikeGitHub(src):
		s, perr := github.ParseSource(src)
		if perr != nil {
			fmt.Fprintln(stderr, perr)
			return "", nil, false
		}
		return "", &s, true
	case err != nil:
		fmt.Fprintln(stderr, err)
	case !fi.Mode().IsRegular():
		fmt.Fprintf(stderr, "%s: not a regular file\n", src)
	default:
		abs, err := filepath.Abs(src)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return "", nil, false
		}
		return abs, nil, true
	}
	return "", nil, false
}
