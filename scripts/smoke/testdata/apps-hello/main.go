// Command apps-hello is the "Go binary app" of smoke 5: it answers every request
// with "go-ok" on the port in $PORT. It lives under testdata so `go build ./...`
// and `go vet ./...` skip it; the smoke script cross-compiles it on its own.
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		fmt.Fprintln(os.Stderr, "PORT is not set: the panel passes an app's port as PORT")
		os.Exit(2)
	}
	fmt.Println("apps-hello listening on", port)
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "go-ok") })
	if err := http.ListenAndServe("0.0.0.0:"+port, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
