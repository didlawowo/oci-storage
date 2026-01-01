package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type HealthResponse struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
	Version   string `json:"version"`
	Hostname  string `json:"hostname"`
}

type InfoResponse struct {
	App       string `json:"app"`
	Version   string `json:"version"`
	Hostname  string `json:"hostname"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

var (
	version  = "1.0.0"
	hostname string
)

func init() {
	hostname, _ = os.Hostname()
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{
		Status:    "healthy",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Version:   version,
		Hostname:  hostname,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func infoHandler(w http.ResponseWriter, r *http.Request) {
	resp := InfoResponse{
		App:       "oci-storage-test-app",
		Version:   version,
		Hostname:  hostname,
		Message:   "Hello from Helm Portal Test App! This validates OCI registry and Helm chart functionality.",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head>
    <title>Helm Portal Test App</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 40px; background: #1a1a2e; color: #eee; }
        .container { max-width: 600px; margin: 0 auto; }
        h1 { color: #00d9ff; }
        .info { background: #16213e; padding: 20px; border-radius: 8px; margin: 20px 0; }
        .success { color: #00ff88; }
        code { background: #0f0f23; padding: 2px 6px; border-radius: 4px; }
    </style>
</head>
<body>
    <div class="container">
        <h1>Helm Portal Test App</h1>
        <div class="info">
            <p class="success">Deployment successful!</p>
            <p><strong>Version:</strong> %s</p>
            <p><strong>Hostname:</strong> %s</p>
            <p><strong>Time:</strong> %s</p>
        </div>
        <div class="info">
            <h3>Validation</h3>
            <p>This app was deployed to validate:</p>
            <ul>
                <li>Docker image pushed to <code>oci-storage.dc-tech.work</code></li>
                <li>Helm chart stored in Helm Portal registry</li>
                <li>ArgoCD deployment from Helm Portal</li>
            </ul>
        </div>
        <div class="info">
            <h3>Endpoints</h3>
            <ul>
                <li><a href="/health">/health</a> - Health check</li>
                <li><a href="/info">/info</a> - App info JSON</li>
            </ul>
        </div>
    </div>
</body>
</html>`, version, hostname, time.Now().UTC().Format(time.RFC3339))
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/info", infoHandler)

	log.Printf("Starting oci-storage-test-app v%s on port %s", version, port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}
