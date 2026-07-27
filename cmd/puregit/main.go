package main

import (
	app "example.com/puregit-server/internal/server"
	"flag"
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
	h := app.New(app.Config{Root: *root, PublicURL: *publicURL, BootstrapUser: os.Getenv("PUREGIT_BOOTSTRAP_USER"), BootstrapToken: os.Getenv("PUREGIT_BOOTSTRAP_TOKEN")})
	s := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	log.Printf("puregit listening on %s", *listen)
	log.Fatal(s.ListenAndServe())
}
