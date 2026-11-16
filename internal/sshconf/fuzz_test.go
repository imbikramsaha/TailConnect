package sshconf

import (
	"strings"
	"testing"
)

// No device-supplied value may ever add a line to the generated config.
func FuzzRender(f *testing.F) {
	f.Add("bob", "10.0.0.1", "tc-a", "p-1")
	f.Add("x\nHost *", "h\r\nProxyCommand x", "tc-b", "id;rm")
	f.Fuzz(func(t *testing.T, user, host, alias, id string) {
		out := Render([]Entry{{Alias: alias, HostName: host, User: user, Port: 22, DeviceID: id, Proxy: id != ""}},
			Options{Exe: "/bin/tc", KeyPath: "/k", KnownHosts: "/kh"})
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			kw := strings.Fields(l)[0]
			switch kw {
			case "Host", "HostName", "User", "Port", "IdentityFile", "IdentitiesOnly", "HostKeyAlias",
				"UserKnownHostsFile", "StrictHostKeyChecking", "ServerAliveInterval", "ProxyCommand",
				"ForwardAgent", "ForwardX11", "ForwardX11Trusted", "PermitLocalCommand":
			default:
				t.Fatalf("unexpected directive %q in:\n%s", kw, out)
			}
			if kw == "Host" && l != "Host "+alias {
				t.Fatalf("injected Host line %q", l)
			}
			if (kw == "ForwardAgent" || kw == "ForwardX11" || kw == "ForwardX11Trusted" || kw == "PermitLocalCommand") && !strings.HasSuffix(l, " no") {
				t.Fatalf("forwarding enabled: %q", l)
			}
			if kw == "ProxyCommand" && l != `ProxyCommand "/bin/tc" proxy `+id+" %p" {
				t.Fatalf("injected ProxyCommand %q", l)
			}
		}
	})
}
