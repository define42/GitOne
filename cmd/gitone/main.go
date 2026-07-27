package main

import (
	"flag"
	app "github.com/define42/GitOne/internal/server"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	root := flag.String("root", "./data", "storage root")
	listen := flag.String("listen", ":8080", "listen address")
	publicURL := flag.String("public-url", "http://localhost:8080", "public base URL")
	flag.Parse()
	h := app.New(app.Config{Root: *root, PublicURL: *publicURL, BootstrapUser: os.Getenv("GITONE_BOOTSTRAP_USER"), BootstrapToken: os.Getenv("GITONE_BOOTSTRAP_TOKEN")})
	s := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("GitOne listening on %s", *listen)
	log.Fatal(s.ListenAndServe())
}
