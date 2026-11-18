package orchestrator

import (
	"net"
	"strings"
	"sync"
)

// sandboxDoors is the set of the server's addresses on sandbox bridges, and which sandbox each
// belongs to. The two doors of 20.3 are told apart by it: a request to one of these addresses
// came from a sandbox's network.
type sandboxDoors struct {
	mu    sync.RWMutex
	byIP  map[string]string     // the server's address on a bridge → the sandbox id
	guest map[string]string     // the sandbox id → its own address
	mode  map[string]string     // the sandbox id → its network mode
	nets  map[string]*net.IPNet // the sandbox id → its bridge's subnet
}

func (d *sandboxDoors) init() {
	if d.byIP == nil {
		d.byIP, d.guest, d.mode, d.nets = map[string]string{}, map[string]string{}, map[string]string{}, map[string]*net.IPNet{}
	}
}

// enter records the server's address on a sandbox's bridge and the bridge's subnet before the server
// joins it, so neither door answers the sandbox as someone else's while it starts. Before enter and
// after drop the ingress's door still refuses it, since it answers only the ingress's network (13.6).
func (d *sandboxDoors) enter(id, brokerIP string, subnet *net.IPNet) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	for ip, sid := range d.byIP {
		if sid == id {
			delete(d.byIP, ip)
		}
	}
	d.byIP[brokerIP] = id
	if subnet != nil {
		d.nets[id] = subnet
	}
}

func (d *sandboxDoors) put(id, brokerIP, guestIP, mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.init()
	d.mode[id] = mode
	for ip, sid := range d.byIP {
		if sid == id {
			delete(d.byIP, ip)
		}
	}
	d.byIP[brokerIP] = id
	d.guest[id] = guestIP
}

func (d *sandboxDoors) drop(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for ip, sid := range d.byIP {
		if sid == id {
			delete(d.byIP, ip)
		}
	}
	delete(d.guest, id)
	delete(d.mode, id)
	delete(d.nets, id)
}

// SandboxAt reports which sandbox a request reached the server from: local is the server's address
// the request arrived at, remote the address it came from. Both must match one sandbox, so a
// request is known by its network and by the sandbox that holds the address on it (12.6).
func (o *Orchestrator) SandboxAt(local, remote string) (id string, ok bool) {
	o.doors.mu.RLock()
	defer o.doors.mu.RUnlock()
	id, ok = o.doors.byIP[hostOnly(local)]
	if !ok || o.doors.guest[id] != hostOnly(remote) {
		return "", false
	}
	return id, true
}

// SandboxMode is the network mode a sandbox this server knows started in, or "".
func (o *Orchestrator) SandboxMode(id string) string {
	o.doors.mu.RLock()
	defer o.doors.mu.RUnlock()
	return o.doors.mode[id]
}

// IsSandboxDoor reports whether local is one of the server's addresses on a sandbox bridge.
func (o *Orchestrator) IsSandboxDoor(local string) bool {
	o.doors.mu.RLock()
	defer o.doors.mu.RUnlock()
	_, ok := o.doors.byIP[hostOnly(local)]
	return ok
}

// IsSandboxGuest reports whether remote is an address on the bridge of a sandbox or a hosted project
// this server knows. A guest is root in its microVM: it can route the server's address on another
// network through its own bridge, which the machine then delivers to the server under that other
// address (the weak-host model, 13.6), and it can send from any address of its bridge, not only the
// one Docker gave it. Such a request is told by where it came from, not where it arrived.
func (o *Orchestrator) IsSandboxGuest(remote string) bool {
	h := hostOnly(remote)
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	o.doors.mu.RLock()
	defer o.doors.mu.RUnlock()
	for _, n := range o.doors.nets {
		if n.Contains(ip) {
			return true
		}
	}
	for _, g := range o.doors.guest {
		if g == h {
			return true
		}
	}
	return false
}

func hostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

func brokerPort(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}
