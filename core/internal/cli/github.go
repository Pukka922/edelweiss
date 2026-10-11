package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/extmgr"
	"github.com/Pukka922/edelweiss/core/internal/github"
	"github.com/Pukka922/edelweiss/core/internal/version"
)

func releasesFor(cfg *config.Config) *github.Client {
	return github.NewClient(cfg.GitHub.APIURL, cfg.GitHub.Token, "edelweiss/"+version.Version)
}

func installerFor(cfg *config.Config, mgr *extmgr.Manager) *extmgr.Installer {
	return extmgr.NewInstaller(extmgr.InstallerOptions{Manager: mgr, Releases: releasesFor(cfg), Arch: runtime.GOARCH,
		MaxDownload: int64(cfg.Extensions.MaxDownloadSize),
		// failures are reported on stderr by the command; the installer's own log would only duplicate them
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// installFromGitHub resolves the release (before anything is created on disk), shows what would be installed, asks
// for confirmation and installs exactly the confirmed file.
func installFromGitHub(ctx context.Context, cfg *config.Config, repo github.Source, id string, yes bool,
	stdin io.Reader, stdout, stderr io.Writer) int {
	res, err := installerFor(cfg, nil).Resolve(ctx, repo.String())
	if err != nil {
		reportFailure(stderr, "install", err)
		return 1
	}
	c, err := extmgr.Pick(res, id)
	if err != nil {
		reportFailure(stderr, "install", err)
		return 1
	}
	fmt.Fprintf(stdout, "Repository: %s\nRelease:    %s (published %s)\nExtension:  %s %s\nFile:       %s (%d bytes)\nSHA-256:    %s\n",
		res.Repo, res.Tag, res.PublishedAt.Format("2006-01-02"), c.ID, c.Version, c.Asset, c.Size, c.SHA256)
	printTrustWarning(stdout)
	if !yes && !confirm(stdin, stdout, fmt.Sprintf("Install %s %s? [y/N] ", c.ID, c.Version)) {
		fmt.Fprintln(stderr, "aborted")
		return 1
	}
	mgr, code := openStore(cfg, stderr)
	if mgr == nil {
		return code
	}
	// Without @tag the source means "the latest release". Pin the one that was shown: a release published since then
	// must neither be installed nor make the checksum check fail.
	repo.Tag = res.Tag
	ext, err := installerFor(cfg, mgr).Install(ctx, extmgr.InstallRequest{Source: repo.String(), ID: c.ID, SHA256: c.SHA256})
	if err != nil {
		reportFailure(stderr, "install", err)
		return 1
	}
	fmt.Fprintf(stdout, "installed %s %s from %s %s\n", ext.Manifest.ID, ext.Manifest.Version, ext.Source.Repo, ext.Source.Tag)
	return 0
}

// updateFromGitHub updates one extension (id) or, with id "", every extension installed from GitHub.
func updateFromGitHub(ctx context.Context, cfg *config.Config, id string, yes bool, stdin io.Reader, stdout, stderr io.Writer) int {
	mgr, code := openStore(cfg, stderr)
	if mgr == nil {
		return code
	}
	if id != "" {
		e, ok := mgr.Get(id)
		if !ok {
			fmt.Fprintf(stderr, "extension %q is not installed\n", id)
			return 1
		}
		if e.Dev || e.Source.Type != "github" {
			fmt.Fprintf(stderr, "extension %q was not installed from GitHub\n", id)
			return 1
		}
	}
	inst := installerFor(cfg, mgr)
	updates, failed := inst.Updates(ctx)
	exit := 0
	for _, f := range failed {
		if id == "" || f.ID == id {
			fmt.Fprintf(stderr, "%s: cannot check for updates: %v\n", f.ID, f.Err)
			exit = 1
		}
	}
	var todo []extmgr.Update
	for _, u := range updates {
		if id == "" || u.ID == id {
			todo = append(todo, u)
		}
	}
	if len(todo) == 0 {
		if exit == 0 {
			if id != "" {
				fmt.Fprintf(stdout, "%s is up to date\n", id)
			} else {
				fmt.Fprintln(stdout, "all extensions are up to date")
			}
		}
		return exit
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tINSTALLED\tAVAILABLE\tRELEASE\tASSET\tSHA-256")
	for _, u := range todo {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s %s\t%s\t%s\n", u.ID, u.Installed, u.Available, u.Repo, u.Tag, u.Asset, u.SHA256)
	}
	_ = tw.Flush()
	printTrustWarning(stdout)
	if !yes && !confirm(stdin, stdout, fmt.Sprintf("Update %d extension(s)? [y/N] ", len(todo))) {
		fmt.Fprintln(stderr, "aborted")
		return 1
	}
	for _, u := range todo {
		// u.SHA256 is the checksum that was listed (and, without --yes, confirmed): if the release changed since,
		// the update fails instead of installing something nobody reviewed.
		ext, err := inst.Update(ctx, u.ID, u.SHA256)
		if errors.Is(err, extmgr.ErrUpToDate) {
			continue
		}
		if err != nil {
			reportFailure(stderr, "update of "+u.ID, err)
			exit = 1
			continue
		}
		fmt.Fprintf(stdout, "updated %s %s → %s\n", u.ID, u.Installed, ext.Manifest.Version)
	}
	return exit
}

// printTrustWarning is shown, with the checksum of what would be installed, before every install and update.
func printTrustWarning(stdout io.Writer) {
	fmt.Fprintln(stdout, "Warning: an extension runs with the privileges of the signed-in user, in admin mode as root.")
	fmt.Fprintln(stdout, "Only install extensions from sources you trust.")
}

// confirm asks question on stdout and reads one answer line from stdin; only "y" and "yes" (any case) confirm.
func confirm(stdin io.Reader, stdout io.Writer, question string) bool {
	fmt.Fprint(stdout, question)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil {
		// End of input before Enter (Ctrl-D, empty or closed stdin): no newline was echoed, so end the prompt line
		// to keep what follows ("aborted") off it.
		fmt.Fprintln(stdout)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
