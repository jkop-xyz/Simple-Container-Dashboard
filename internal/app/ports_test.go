package app_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

// getAs fetches the dashboard as if the browser had used host to reach it.
func (h *harness) getAs(host string) string {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/containers", nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func portsCell(body, id string) string {
	_, rest, _ := strings.Cut(row(body, id), `class="ports`)
	_, rest, _ = strings.Cut(rest, ">")
	cell, _, _ := strings.Cut(rest, "</td>")
	return cell
}

func bind(ip string, public int, typ string) dockertest.Port {
	return dockertest.Port{IP: ip, PrivatePort: public, PublicPort: public, Type: typ}
}

func TestPublishedTCPPortsLinkToTheBrowsersHostname(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "j", Name: "jellyfin", State: "running", Ports: []dockertest.Port{
		bind("0.0.0.0", 8096, "tcp"), bind("::", 8096, "tcp"), bind("192.168.1.5", 8920, "tcp"),
	}})

	cell := portsCell(h.getAs("nas.lan:1234"), "j")
	assertContains(t, cell,
		`href="http://nas.lan:8096" target="_blank" rel="noopener" aria-label="Open port 8096 in a new tab">8096<svg`,
		`href="http://nas.lan:8920"`)
	if n := strings.Count(cell, ">8096<svg"); n != 1 {
		t.Errorf("IPv4 and IPv6 bindings should merge into one link, got %d", n)
	}
}

func TestPortLinksWorkForIPAddressesAndIPv6Hosts(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "a", Name: "a", State: "running", Ports: []dockertest.Port{bind("0.0.0.0", 80, "tcp")}})
	assertContains(t, portsCell(h.getAs("10.0.0.7:1234"), "a"), `href="http://10.0.0.7:80"`)
	assertContains(t, portsCell(h.getAs("[fd00::7]:1234"), "a"), `href="http://[fd00::7]:80"`)
}

func TestPort443DefaultsToHTTPSAndPerPortLabelsWin(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "p", Name: "proxy", State: "running",
		Labels: map[string]string{"scd.scheme.8080": "https"},
		Ports:  []dockertest.Port{bind("0.0.0.0", 80, "tcp"), bind("0.0.0.0", 443, "tcp"), bind("0.0.0.0", 8080, "tcp")}})
	cell := portsCell(h.getAs("nas.lan:1234"), "p")
	assertContains(t, cell, `href="http://nas.lan:80"`, `href="https://nas.lan:443"`, `href="https://nas.lan:8080"`)
}

func TestSchemeLabelSwitchesLinksToHTTPS(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "t", Name: "traefik", State: "running",
		Labels: map[string]string{"scd.scheme": "https"},
		Ports:  []dockertest.Port{bind("0.0.0.0", 443, "tcp")}})
	assertContains(t, portsCell(h.getAs("nas.lan:1234"), "t"), `href="https://nas.lan:443"`)
}

func TestLoopbackAndUDPPortsAreText(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "x", Name: "x", State: "running", Ports: []dockertest.Port{
		bind("127.0.0.1", 5432, "tcp"), bind("::1", 5432, "tcp"),
		bind("0.0.0.0", 51820, "udp"), bind("::", 51820, "udp"),
	}})
	cell := portsCell(h.getAs("nas.lan:1234"), "x")
	assertContains(t, cell, ">5432/tcp (local)<", ">51820/udp<")
	if strings.Contains(cell, "href") {
		t.Errorf("loopback/UDP ports must not be links: %s", cell)
	}
	if strings.Count(cell, "51820/udp") != 1 || strings.Count(cell, "5432/tcp") != 1 {
		t.Errorf("duplicates not merged: %s", cell)
	}
}

func TestUnpublishedPortsAreNotShown(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "n", Name: "n", State: "running", Ports: []dockertest.Port{
		{PrivatePort: 6379, Type: "tcp"},
	}})
	if cell := portsCell(h.getAs("nas.lan:1234"), "n"); strings.Contains(cell, `class="port`) || strings.Contains(cell, "6379") {
		t.Errorf("expected no ports, got %q", cell)
	}
}
