// Command healthprobe performs an HTTP readiness check for container
// HEALTHCHECK directives and Kubernetes exec probes. It exits 0 when the
// target answers with a 2xx status and 1 otherwise, printing the outcome to
// stderr for log capture.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8080/health/ready", "readiness endpoint to probe")
	timeout := flag.Duration("timeout", 5*time.Second, "request timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	response, err := client.Get(*url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthprobe: %v\n", err)
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		fmt.Fprintf(os.Stderr, "healthprobe: %s returned %s\n", *url, response.Status)
		os.Exit(1)
	}
}
