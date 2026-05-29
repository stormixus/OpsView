package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	scanWorkers     = 64
	scanHostTimeout = 800 * time.Millisecond
)

type lanScanHit struct {
	addr, protocol string
	port           int
}

// LANDevice is a DVR/NVR discovered on the local network.
type LANDevice struct {
	Addr         string `json:"addr"`
	Port         int    `json:"port"`
	Protocol     string `json:"protocol"` // isapi | dahua | rtsp
	Vendor       string `json:"vendor"`
	AlreadyAdded bool   `json:"already_added"`
}

// ListScanSubnets returns /24 CIDR strings for active non-loopback IPv4 interfaces.
func (m *CCTVManager) ListScanSubnets() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []string
	seen := make(map[string]struct{})
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip[0] == 169 && ip[1] == 254 {
				continue
			}
			mask := ipNet.Mask
			if len(mask) != 4 {
				continue
			}
			ones, _ := mask.Size()
			if ones < 8 || ones > 30 {
				continue
			}
			network := ip.Mask(mask)
			if ones >= 24 {
				cidr := fmt.Sprintf("%s/24", network.String())
				if _, ok := seen[cidr]; !ok {
					seen[cidr] = struct{}{}
					out = append(out, cidr)
				}
			}
		}
	}
	return out, nil
}

// scanCancel is set during ScanLAN; CancelScan stops an in-flight scan.
var scanCancel atomic.Pointer[context.CancelFunc]

// CancelScan requests cancellation of the current LAN scan.
func (m *CCTVManager) CancelScan() {
	if fn := scanCancel.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
}

// ScanLAN scans a single /24 subnet for Hikvision/Dahua HTTP APIs (ports 80, 8000).
func (m *CCTVManager) ScanLAN(subnet string, username, password string) ([]LANDevice, error) {
	_, ipNet, err := net.ParseCIDR(strings.TrimSpace(subnet))
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", subnet, err)
	}
	ip4 := ipNet.IP.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("subnet must be IPv4: %s", subnet)
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 || ones > 24 {
		return nil, fmt.Errorf("v1 scan supports /24 only, got /%d", ones)
	}

	ctx, cancel := context.WithCancel(context.Background())
	scanCancel.Store(&cancel)
	defer func() {
		scanCancel.Store(nil)
		cancel()
	}()

	existingKeys := make(map[string]struct{})
	if m.db != nil {
		if existing, err := m.ListDVRs(); err == nil {
			for _, d := range existing {
				existingKeys[fmt.Sprintf("%s:%d", d.Addr, d.Port)] = struct{}{}
			}
		}
	}

	if username == "" {
		username = "admin"
	}

	base := ip4.Mask(ipNet.Mask)
	var hosts []string
	for i := 1; i < 255; i++ {
		ip := make(net.IP, len(base))
		copy(ip, base)
		ip[3] = byte(i)
		hosts = append(hosts, ip.String())
	}

	ports := []int{80, 8000}
	results := make(chan lanScanHit, 512)
	var wg sync.WaitGroup
	sem := make(chan struct{}, scanWorkers)

	for _, host := range hosts {
		host := host
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			m.probeHostOnLAN(ctx, host, ports, username, password, results)
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	seen := make(map[string]LANDevice)
	for c := range results {
		key := fmt.Sprintf("%s:%d", c.addr, c.port)
		if _, ok := seen[key]; ok {
			continue
		}
		vendor := c.protocol
		if c.protocol == "isapi" {
			vendor = "Hikvision"
		} else if c.protocol == "dahua" {
			vendor = "Dahua"
		}
		_, already := existingKeys[key]
		seen[key] = LANDevice{
			Addr: c.addr, Port: c.port, Protocol: c.protocol, Vendor: vendor,
			AlreadyAdded: already,
		}
	}

	out := make([]LANDevice, 0, len(seen))
	for _, d := range seen {
		out = append(out, d)
	}
	return out, nil
}

func (m *CCTVManager) probeHostOnLAN(ctx context.Context, host string, ports []int, username, password string, out chan<- lanScanHit) {
	for _, port := range ports {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !probeTCPPort(host, port) {
			continue
		}
		dvr := DVRConfig{Addr: host, Port: port, Username: username, Password: password}
		proto := m.probeDVRProtocol(dvr)
		if proto != "isapi" && proto != "dahua" {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case out <- lanScanHit{addr: host, protocol: proto, port: port}:
		}
	}
}
