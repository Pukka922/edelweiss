// Command mock-github serves GitHub release metadata and assets from a directory tree, so the edelweiss installer
// can be developed and tested without GitHub: point [github] api_url at it.
//
// Layout: <root>/<owner>/<repo>/<tag>/<asset files>. The latest release of a repository is the tag named in
// <root>/<owner>/<repo>/latest, else the highest semantic version tag that is not a prerelease.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	root := flag.String("root", "releases", "directory with <owner>/<repo>/<tag>/<asset files>")
	listen := flag.String("listen", ":8080", "address to listen on")
	flag.Parse()
	log.Printf("mock-github: serving %s on %s", *root, *listen)
	srv := &http.Server{Addr: *listen, Handler: logRequests(newHandler(*root)), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// logRequests logs every request with its status and whether it carried a token.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("%s %s %d token=%t", r.Method, r.URL.Path, rec.status, r.Header.Get("Authorization") != "")
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
