package bootstrap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Opt in locally; CI always checks the real production Caddy template and HTTP/2.
func TestSSECaddyHTTP2LongStreamAndIPGate(t *testing.T) {
	if os.Getenv("SSE_PROXY_TEST") != "1" && os.Getenv("CI") != "true" {
		t.Skip("set SSE_PROXY_TEST=1 for 135s Caddy integration")
	}
	a := newTestApp(t)
	adv := a.newAdvertiser("sse-proxy@example.test")
	admin := a.adminClient("sse-proxy-admin@example.test", accountdomain.AdminSenior)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := newHTTPServer("", a.srv.Config.Handler)
	go backend.Serve(ln)
	defer backend.Close()
	backendPort := ln.Addr().(*net.TCPAddr).Port
	template, err := os.ReadFile("../../deploy/app/maxim-app.caddy")
	if err != nil {
		t.Fatal(err)
	}
	render := func(port, allow string) string {
		s := strings.NewReplacer("__DOMAIN__", "https://localhost:"+port, "__ADMIN_ALLOW__", allow,
			"__PREFIX__-api-a:8080", fmt.Sprintf("host.testcontainers.internal:%d", backendPort),
			"__PREFIX__-api-b:8080", fmt.Sprintf("host.testcontainers.internal:%d", backendPort)).Replace(string(template))
		return strings.Replace(s, "\tencode zstd gzip", "\ttls internal\n\tencode zstd gzip", 1)
	}
	config := filepath.Join(t.TempDir(), "Caddyfile")
	if err = os.WriteFile(config, []byte("{\n admin off\n auto_https disable_redirects\n}\n"+render("8443", "0.0.0.0/0 ::/0")+render("8444", "192.0.2.1")), 0600); err != nil {
		t.Fatal(err)
	}
	ctr, err := testcontainers.GenericContainer(t.Context(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: "caddy:2.11.7", ExposedPorts: []string{"8443/tcp", "8444/tcp"},
			HostAccessPorts: []int{backendPort}, Files: []testcontainers.ContainerFile{{HostFilePath: config, ContainerFilePath: "/etc/caddy/Caddyfile", FileMode: 0600}},
			WaitingFor: wait.ForListeningPort("8443/tcp").WithStartupTimeout(time.Minute)}, Started: true})
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
	allowedPort, err := ctr.MappedPort(t.Context(), "8443/tcp")
	if err != nil {
		t.Fatal(err)
	}
	deniedPort, err := ctr.MappedPort(t.Context(), "8444/tcp")
	if err != nil {
		t.Fatal(err)
	}
	proxyHost, err := ctr.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	allowed := "https://localhost:" + allowedPort.Port()
	denied := "https://localhost:" + deniedPort.Port()
	newClient := func(c *client) *http.Client {
		origin, _ := url.Parse(a.srv.URL)
		target, _ := url.Parse(allowed)
		c.http.Jar.SetCookies(target, c.http.Jar.Cookies(origin))
		return &http.Client{Jar: c.http.Jar, Transport: &http.Transport{ForceAttemptHTTP2: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(proxyHost, port))
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // Isolated Caddy internal CA, test only.
	}
	ac := newClient(admin)
	// The listening TLS socket can precede issuance of the internal localhost certificate.
	until := time.Now().Add(10 * time.Second)
	for {
		resp, e := ac.Get(denied + "/livez")
		if e == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(until) {
			t.Fatal(e)
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, target := range []struct {
		base string
		want int
	}{{denied, 403}, {allowed, 200}} {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", target.base+BaseURL+"/admin/events", nil)
		resp, err := ac.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != target.want {
			resp.Body.Close()
			t.Fatalf("admin IP gate got=%d want=%d", resp.StatusCode, target.want)
		}
		resp.Body.Close()
	}
	c := newClient(adv)
	defer c.CloseIdleConnections()
	defer ac.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", allowed+BaseURL+"/events", nil)
	started := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ProtoMajor != 2 {
		t.Fatalf("proxy status=%d protocol=%s", resp.StatusCode, resp.Proto)
	}
	if resp.Header.Get("Connection") != "" {
		t.Fatal("HTTP/1 Connection header leaked into HTTP/2")
	}
	s := readStream(resp.Body)
	s.next(t, "ready", 3*time.Second)
	for i := 0; i < 9; i++ {
		f := s.next(t, "heartbeat", 18*time.Second)
		if string(f.Data) != `{"version":1}` {
			t.Fatal(string(f.Data))
		}
	}
	if time.Since(started) < 2*time.Minute {
		t.Fatal("long stream did not exceed two minutes")
	}
	t.Logf("HTTP/2 through production Caddy template: %s, heartbeats=9, write_timeout=%s, upstream_port=%s", time.Since(started), backend.WriteTimeout, strconv.Itoa(backendPort))
	// Cancellation must clean up the stream without waiting for HTTP Shutdown's timeout.
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = backend.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
