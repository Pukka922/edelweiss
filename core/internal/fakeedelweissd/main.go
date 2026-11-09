// Command fakeedelweissd stands in for a release of edelweissd that misbehaves, for make test-selfupdate. It is built
// with -ldflags "-X main.version=<v> -X main.mode=<broken|crashy>". It lives below core/internal because it uses the
// init script and the configuration of the real daemon, so that the script is byte for byte the real one.
//
//	version                          prints <v>
//	service script --binary <path>   prints the init script of the real daemon
//	serve [--config <file>] ...      broken: exits 1 at once; crashy: answers /api/health on the configured listen
//	                                 address (with TLS as the configuration says) for 5 s, then exits 1
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Pukka922/edelweiss/core/internal/config"
	"github.com/Pukka922/edelweiss/core/internal/service"
	"github.com/Pukka922/edelweiss/core/internal/tlsutil"
)

// Set by the linker.
var (
	version = "0.0.0-fake"
	mode    = "broken"
)

const crashAfter = 5 * time.Second

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fakeedelweissd: a command is needed")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
		return 0
	case "service":
		return script(args[1:])
	case "serve":
		return serve(args[1:])
	}
	fmt.Fprintf(os.Stderr, "fakeedelweissd: unknown command %q\n", args[0])
	return 2
}

func script(args []string) int {
	fset := flag.NewFlagSet("service script", flag.ContinueOnError)
	binary := fset.String("binary", "", "absolute path of the binary the script runs")
	if len(args) == 0 || args[0] != "script" || fset.Parse(args[1:]) != nil || *binary == "" {
		fmt.Fprintln(os.Stderr, "usage: service script --binary <path>")
		return 2
	}
	s, err := service.Script(*binary)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fmt.Print(s)
	return 0
}

func serve(args []string) int {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fset.String("config", config.DefaultPath, "configuration file")
	fset.Bool("dev", false, "ignored")
	if fset.Parse(args) != nil {
		return 2
	}
	if mode != "crashy" {
		fmt.Fprintf(os.Stderr, "fakeedelweissd %s (%s): failing on purpose\n", version, mode)
		return 1
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version, "started": started})
	})
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		time.Sleep(crashAfter)
		fmt.Fprintf(os.Stderr, "fakeedelweissd %s (crashy): crashing on purpose\n", version)
		os.Exit(1)
	}()
	fmt.Fprintf(os.Stderr, "fakeedelweissd %s (crashy): answering on %s for %v\n", version, cfg.Listen, crashAfter)
	if cfg.TLS.Enabled {
		cert, key := cfg.TLS.Cert, cfg.TLS.Key
		if cert == "" {
			if cert, key, _, err = tlsutil.EnsureSelfSigned(cfg.TLSDir(), tlsutil.LocalHosts(), time.Now()); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		err = srv.ServeTLS(ln, cert, key)
	} else {
		err = srv.Serve(ln)
	}
	if !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
	}
	_ = srv.Shutdown(context.Background())
	return 1
}
