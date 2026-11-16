package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestProductionEdgePreservesLandingPDFsAndContact(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me" && r.Header.Get("Cookie") != "session=test" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, "API")
	}))
	defer api.Close()
	contact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Real-IP") == "" {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, "CONTACT")
	}))
	defer contact.Close()
	port := func(server *httptest.Server) int {
		_, raw, _ := net.SplitHostPort(server.Listener.Addr().String())
		p, _ := strconv.Atoi(raw)
		return p
	}
	template, err := os.ReadFile("../../deploy/app/maxim-app.caddy")
	if err != nil {
		t.Fatal(err)
	}
	config := strings.NewReplacer("__DOMAIN__", ":8080", "__ADMIN_ALLOW__", "192.0.2.1/32",
		"__PREFIX__-api-a:8080", fmt.Sprintf("host.testcontainers.internal:%d", port(api)),
		"__PREFIX__-api-b:8080", fmt.Sprintf("host.testcontainers.internal:%d", port(api)),
		"chanly-landing-contact-production:8091", fmt.Sprintf("host.testcontainers.internal:%d", port(contact)),
		"__PREFIX__", "maxim-test").Replace(string(template))
	directory := t.TempDir()
	file := filepath.Join(directory, "Caddyfile")
	if err = os.WriteFile(file, []byte("{\n admin off\n auto_https off\n}\n"+config), 0600); err != nil {
		t.Fatal(err)
	}
	ctr, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "caddy:2.11.7", ExposedPorts: []string{"8080/tcp"}, HostAccessPorts: []int{port(api), port(contact)},
		Files:      []testcontainers.ContainerFile{{HostFilePath: file, ContainerFilePath: "/etc/caddy/Caddyfile", FileMode: 0600}},
		WaitingFor: wait.ForListeningPort("8080/tcp").WithStartupTimeout(time.Minute)}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	defer testcontainers.TerminateContainer(ctr)
	defer func() {
		if t.Failed() {
			logs, e := ctr.Logs(context.Background())
			if e == nil {
				defer logs.Close()
				b, _ := io.ReadAll(logs)
				t.Log(string(b))
			}
		}
	}()
	put := func(path, body string) {
		t.Helper()
		if err := ctr.CopyToContainer(t.Context(), []byte(body), "/srv/maxim-test/"+path, 0644); err != nil {
			t.Fatal(err)
		}
	}
	put("landing/current/index.html", "LANDING")
	for _, id := range []string{"offer", "offer-author", "rules", "privacy_policy", "consent-feedback", "requisites"} {
		put("landing/current/"+id+".pdf", "%PDF-test-"+id)
	}
	put("landing/shared/landing-assets/consent-version.pdf", "%PDF-test-version")
	base, err := ctr.Endpoint(t.Context(), "http")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, session, want string, status int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), "GET", base+path, nil)
		if session != "" {
			req.Header.Set("Cookie", session)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != status || (want != "" && string(body) != want) {
			t.Fatalf("%s: status=%d body=%q", path, response.StatusCode, body)
		}
		if strings.HasSuffix(path, ".pdf") && response.Header.Get("Content-Type") != "application/pdf" {
			t.Fatalf("%s: wrong PDF MIME", path)
		}
	}
	get("/", "", "LANDING", 200)
	get("/auth/login", "", "", 503) // Backend may be installed before its separate frontend.
	get("/readyz", "", "API", 200)
	get("/api/v1/orders", "", "API", 200)
	get("/api/v1/admin/orders", "", "", 403)
	get("/api/landing/contact", "", "CONTACT", 200)
	for _, id := range []string{"offer", "offer-author", "rules", "privacy_policy", "consent-feedback", "requisites"} {
		get("/"+id+".pdf", "", "%PDF-test-"+id, 200)
		get("/"+id, "", "%PDF-test-"+id, 200)
	}
	get("/landing-assets/consent-version.pdf", "", "%PDF-test-version", 200)
	put("current/index.html", "APP")
	get("/", "", "LANDING", 200)
	get("/", "session=test", "APP", 200)
	get("/auth/login", "", "APP", 200)
	get("/offer.pdf", "session=test", "%PDF-test-offer", 200)
}
