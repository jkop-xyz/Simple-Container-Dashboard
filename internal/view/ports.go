package view

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
)

// SchemeLabel switches all of a container's port links to another scheme, e.g. "https".
// SchemeLabel + ".<port>" (e.g. "scd.scheme.8080=https") sets it for one port and wins.
// Without labels, ports 443 and 8443 link with https and everything else with http.
const SchemeLabel = "scd.scheme"

// schemeFor picks a port link's scheme.
func schemeFor(c dockerapi.Container, port int) string {
	if s := c.Labels[SchemeLabel+"."+strconv.Itoa(port)]; s != "" {
		return s
	}
	if s := c.Labels[SchemeLabel]; s != "" {
		return s
	}
	if port == 443 || port == 8443 {
		return "https"
	}
	return "http"
}

// PortView is one published port as the dashboard shows it.
type PortView struct {
	Port int
	Type string
	// Local marks a port published only on a loopback address.
	Local bool
	// URL is set for ports that open in the browser.
	URL string
}

// Label is the port's text when it isn't a link, e.g. "5432/tcp (local)" or "51820/udp".
func (p PortView) Label() string {
	if p.Local {
		return fmt.Sprintf("%d/%s (local)", p.Port, p.Type)
	}
	return fmt.Sprintf("%d/%s", p.Port, p.Type)
}

// Ports turns a container's bindings into what the dashboard shows. TCP ports published
// on all interfaces or a non-loopback address link to http(s)://<host>:<port>, where host
// is the hostname the browser used to reach the dashboard. IPv4 and IPv6 bindings of the
// same port are merged; unpublished ports are left out.
func Ports(c dockerapi.Container, host string) []PortView {
	type key struct {
		port int
		typ  string
	}
	merged := map[key]*PortView{}
	for _, b := range c.Ports {
		if b.PublicPort == 0 {
			continue
		}
		typ := b.Type
		if typ == "" {
			typ = "tcp"
		}
		k := key{b.PublicPort, typ}
		local := isLoopback(b.IP)
		pv, ok := merged[k]
		if !ok {
			pv = &PortView{Port: b.PublicPort, Type: typ, Local: true}
			merged[k] = pv
		}
		// A port is local only if every binding of it is on loopback.
		pv.Local = pv.Local && local
	}
	out := make([]PortView, 0, len(merged))
	for _, pv := range merged {
		if pv.Type == "tcp" && !pv.Local && host != "" {
			u := url.URL{Scheme: schemeFor(c, pv.Port), Host: net.JoinHostPort(host, strconv.Itoa(pv.Port))}
			pv.URL = u.String()
		}
		out = append(out, *pv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Type < out[j].Type
	})
	return out
}

func isLoopback(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}
