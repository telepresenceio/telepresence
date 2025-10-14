package tls

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"golang.org/x/net/http2"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Manager interface {
	// GetDownstreamCertificate returns the TLS certificate for the given port.
	GetDownstreamCertificate(port uint16) *tls.Certificate

	// GetUpstreamCertificate returns the TLS certificate for the given port and a boolean indicating if
	// InsecureSkipVerify should be used when connecting to the upstream.
	GetUpstreamCertificate(port uint16) (*tls.Certificate, bool)

	// UseHTTP2 returns true if the given port supports HTTP/2.
	UseHTTP2(ctx context.Context, port uint16) bool

	// UseTLS returns true if the given port supports TLS.
	UseTLS(ctx context.Context, port uint16) bool

	// StartWatchers starts the watchers that keep the TLS configuration up to date.
	StartWatchers(g *dgroup.Group, certsReady chan<- struct{})
}

func NewManager(ctx context.Context, config *agentconfig.Sidecar, podIP netip.Addr, annotations map[string]string) (Manager, error) {
	m := &manager{
		sidecarConfig: config,
		podIP:         podIP,
		connInfos:     make(map[uint16]ConnectionInfo),
	}
	err := m.createPortConfigs(ctx, annotations)
	if err != nil {
		return nil, err
	}
	return m, nil
}

type ValueState int32

const (
	ValueUnknown ValueState = iota
	ValueNotSupported
	ValueSupported
)

type ConnectionInfo struct {
	TLS   ValueState
	HTTP2 ValueState
}

type manager struct {
	sync.RWMutex
	podIP         netip.Addr
	portConfigs   map[uint16]*portConfig
	sidecarConfig *agentconfig.Sidecar
	connInfos     map[uint16]ConnectionInfo
}

func readPathAnnotations(am map[string]string, ann string) (paths map[uint16]string, err error) {
	if _, ok := am[ann]; ok {
		return nil, fmt.Errorf(`annotation %q requires a ".<port>" suffix`, ann)
	}
	prefix := ann + "."
	paths = make(map[uint16]string)
	for k, v := range am {
		if strings.HasPrefix(k, prefix) {
			port, err := strconv.ParseUint(strings.TrimPrefix(k, prefix), 10, 16)
			if err != nil {
				return nil, fmt.Errorf("invalid port number in annotation %s: %w", k, err)
			}
			paths[uint16(port)] = v
		}
	}
	return paths, nil
}

func (m *manager) createPortConfigs(ctx context.Context, am map[string]string) error {
	dsPaths, err := readPathAnnotations(am, annotation.DownstreamCertificatePath)
	if err != nil {
		return err
	}
	usPaths, err := readPathAnnotations(am, annotation.UpstreamCertificatePath)
	if err != nil {
		return err
	}
	usISVs, err := readPathAnnotations(am, annotation.UpstreamInsecureSkipVerify)
	if err != nil {
		return err
	}
	m.portConfigs = make(map[uint16]*portConfig, len(m.sidecarConfig.Containers))

	for _, cn := range m.sidecarConfig.Containers {
		for _, ic := range cn.Intercepts {
			if ic.Protocol != types.ProtoTCP {
				continue
			}
			switch strings.ToLower(ic.AppProtocol) {
			case "tcp", "udp":
				// No app-layer sniffing
				continue
			}
			cp := ic.ContainerPort

			var dsPath, usPath, usISV string
			var ok bool
			if dsPath, ok = dsPaths[cp]; ok {
				delete(dsPaths, cp)
			}
			if usPath, ok = usPaths[cp]; ok {
				delete(usPaths, cp)
			}
			if usISV, ok = usISVs[cp]; ok {
				delete(usISVs, cp)
			}
			if dsPath == "" && usPath == "" && usISV == "" {
				continue
			}
			var usInsecure bool
			switch usISV {
			case "enabled":
				usInsecure = true
			case "", "disabled":
				usInsecure = false
			default:
				return fmt.Errorf(`invalid value %q for annotation %s. Expected "enabled" or "disabled": %w`, usISV, annotation.UpstreamInsecureSkipVerify, err)
			}
			dlog.Debugf(ctx, "Adding TLS config for container port %d: downstream %q, upstream %q", cp, dsPath, usPath)

			// The ports must be keyed by the proxy port, not the actual container port.
			cp = m.sidecarConfig.InterceptorInactivePort(cp, types.ProtoTCP)
			m.portConfigs[cp] = &portConfig{
				downstreamSecretPath:       dsPath,
				upstreamSecretPath:         usPath,
				upstreamInsecureSkipVerify: usInsecure,
				containerName:              cn.Name,
			}
		}
	}

	// Warn about ports that weren't dealt with when iterating over the containers
	warnNotHTTPPort := func(ports map[uint16]string, ann string) {
		for port := range ports {
			dlog.Warnf(ctx, "Annotation %s.%d does not match a port where HTTP-filters can be applied.", ann, port)
		}
	}
	warnNotHTTPPort(dsPaths, annotation.DownstreamCertificatePath)
	warnNotHTTPPort(usPaths, annotation.UpstreamCertificatePath)
	warnNotHTTPPort(usISVs, annotation.UpstreamInsecureSkipVerify)
	return nil
}

func (m *manager) GetDownstreamCertificate(port uint16) *tls.Certificate {
	if pc, ok := m.portConfigs[port]; ok {
		return pc.getDownstreamCert()
	}
	return nil
}

func (m *manager) GetUpstreamCertificate(port uint16) (*tls.Certificate, bool) {
	if pc, ok := m.portConfigs[port]; ok {
		return pc.getUpstreamCert()
	}
	return nil, false
}

func (m *manager) StartWatchers(g *dgroup.Group, certsReady chan<- struct{}) {
	allReady := sync.WaitGroup{}
	allReady.Add(len(m.portConfigs))
	for p, cn := range m.portConfigs {
		g.Go(fmt.Sprintf("watch-tls/%d", p), func(ctx context.Context) error {
			return cn.watchPaths(ctx, &allReady, func() {
				m.setUseTLS(ctx, p)
			})
		})
	}
	allReady.Wait()
	close(certsReady)
}

func (m *manager) setUseTLS(ctx context.Context, port uint16) {
	m.Lock()
	connInfo := m.connInfos[port]
	dlog.Debugf(ctx, "Port %d supports TLS", port)
	connInfo.TLS = ValueSupported
	m.connInfos[port] = connInfo
	m.Unlock()
}

func (m *manager) UseTLS(ctx context.Context, port uint16) bool {
	m.Lock()
	supported := m.useTLS(ctx, port)
	m.Unlock()
	return supported
}

func (m *manager) UseHTTP2(ctx context.Context, port uint16) bool {
	m.Lock()
	supported := m.useHTTP2(ctx, port)
	m.Unlock()
	return supported
}

func (m *manager) useTLS(ctx context.Context, port uint16) bool {
	connInfo, ok := m.connInfos[port]
	if ok && connInfo.TLS != ValueUnknown {
		return connInfo.TLS == ValueSupported
	}
	if state := m.configuredTLS(port); state != ValueUnknown {
		s := "supports"
		if state != ValueSupported {
			s = "does not support"
		}
		dlog.Debugf(ctx, "Port %d %s TLS", port, s)
		connInfo.TLS = state
		m.connInfos[port] = connInfo
		return state == ValueSupported
	}
	return m.probeTLS(ctx, port)
}

func (m *manager) configuredTLS(port uint16) ValueState {
	_, it := m.sidecarConfig.InterceptTarget(port, types.ProtoTCP)
	for _, ic := range it {
		switch strings.ToLower(ic.AppProtocol) {
		case "tcp", "udp": // No app-layer sniffing
			return ValueNotSupported
		case "kubernetes.io/ws": // WebSocket over cleartext
			return ValueNotSupported
		case "h2c", "kubernetes.io/h2c": // HTTP/2 over cleartext
			return ValueNotSupported
		case "wss", "kubernetes.io/wss": // WebSocket over TLS
			return ValueSupported
		case "http2", "https", "tls":
			return ValueSupported
		}
	}
	for _, ic := range it {
		if ic.ServicePort == 443 || ic.ServicePortName == "https" {
			return ValueSupported
		}
	}
	return ValueUnknown
}

func (m *manager) probeTLS(ctx context.Context, port uint16) bool {
	connInfo, ok := m.connInfos[port]
	if ok && connInfo.TLS != ValueUnknown && connInfo.HTTP2 != ValueUnknown {
		return connInfo.TLS == ValueSupported
	}
	dlog.Debugf(ctx, "Probing port %d for TLS and HTTP/2 support", port)
	addr := netip.AddrPortFrom(m.podIP, port).String()
	bc := backoff.NewExponentialBackOff()
	bc.MaxElapsedTime = 2 * time.Second
	bc.MaxInterval = 300 * time.Millisecond
	bc.InitialInterval = 100 * time.Millisecond
	err := backoff.Retry(func() error {
		ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		dialer := &net.Dialer{}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			dlog.Debugf(ctx, "Unable to dial %s: %v", addr, err)
			return err
		}
		defer conn.Close()

		tlsConn := tls.Client(conn, &tls.Config{
			NextProtos:         []string{"h2", "http/1.1"},
			InsecureSkipVerify: true,
		})

		if err = tlsConn.HandshakeContext(ctx); err != nil {
			dlog.Debugf(ctx, "Port %d does not support TLS: %v", port, err)
			connInfo.TLS = ValueNotSupported
		} else {
			dlog.Debugf(ctx, "Port %d supports TLS", port)
			connInfo.TLS = ValueSupported
			if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
				dlog.Debugf(ctx, "Port %d supports HTTP/2", port)
				connInfo.HTTP2 = ValueSupported
			} else {
				dlog.Debugf(ctx, "Port %d does not support HTTP/2: %v", port, err)
				connInfo.HTTP2 = ValueNotSupported
			}
		}
		m.connInfos[port] = connInfo
		return nil
	}, backoff.WithContext(bc, ctx))
	if err != nil {
		dlog.Warnf(ctx, "unable to dial port %d: %v", port, err)
	}
	return connInfo.TLS == ValueSupported
}

func (m *manager) useHTTP2(ctx context.Context, port uint16) bool {
	connInfo, ok := m.connInfos[port]
	if ok && connInfo.HTTP2 != ValueUnknown {
		return connInfo.HTTP2 == ValueSupported
	}
	if state := m.configuredHTTP2(port); state != ValueUnknown {
		s := "supports"
		if state != ValueSupported {
			s = "does not support"
		}
		dlog.Debugf(ctx, "Port %d %s HTTP/2", port, s)
		connInfo.HTTP2 = state
		m.connInfos[port] = connInfo
		return state == ValueSupported
	}
	return m.probeHTTP2(ctx, port)
}

func (m *manager) configuredHTTP2(port uint16) ValueState {
	_, it := m.sidecarConfig.InterceptTarget(port, types.ProtoTCP)
	for _, ic := range it {
		switch strings.ToLower(ic.AppProtocol) {
		case "tcp", "udp": // No app-layer sniffing
			return ValueNotSupported
		case "h2c", "kubernetes.io/h2c": // HTTP/2 over cleartext
			return ValueSupported
		case "http2", "grpc": // HTTP/2 over TLS
			return ValueSupported
		}
	}
	return ValueUnknown
}

func (m *manager) probeHTTP2(ctx context.Context, port uint16) bool {
	info := m.connInfos[port]
	if info.TLS == ValueUnknown {
		m.useTLS(ctx, port)
		info = m.connInfos[port]
	}
	if info.HTTP2 != ValueUnknown {
		return info.HTTP2 == ValueSupported
	}

	if info.TLS == ValueNotSupported {
		dlog.Debugf(ctx, "Probing port %d for HTTP/2 clear-text support", port)
		supported := m.probeHTTP2ClearText(ctx, port)
		if supported {
			dlog.Debugf(ctx, "Port %d supports HTTP/2 clear text", port)
			info.HTTP2 = ValueSupported
		} else {
			dlog.Debugf(ctx, "Port %d does not support HTTP/2 clear text", port)
			info.HTTP2 = ValueNotSupported
		}
		m.connInfos[port] = info
	} else {
		// TLS has been determined from appProtocol because otherwise the HTTP/2 status would already be known.
		// Let's probe TLS to also get HTTP/2 status.
		m.probeTLS(ctx, port)
	}
	return m.connInfos[port].HTTP2 == ValueSupported
}

func (m *manager) probeHTTP2ClearText(ctx context.Context, port uint16) bool {
	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.Dial("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		dlog.Warnf(ctx, "unable to dial port %d: %v", port, err)
		return false
	}
	defer conn.Close()

	// HTTP/2 connection preface for prior knowledge (direct h2c).
	// Send the preface.
	_, err = conn.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"))
	if err != nil {
		dlog.Warnf(ctx, "failed to send connection preface on port %d: %v", port, err)
		return false
	}
	// Send the required settings frame (empty).
	_, err = conn.Write([]byte{0, 0, 0, byte(http2.FrameSettings), 0, 0, 0, 0, 0})
	if err != nil {
		dlog.Warnf(ctx, "failed to send empty settings frame on port %d: %v", port, err)
		return false
	}

	// Read the initial response (expect SETTINGS frame).
	buf := make([]byte, 9) // Frame header is 9 bytes.
	n, err := conn.Read(buf)
	if err != nil || n != 9 {
		dlog.Debugf(ctx, "failed to read settings frame on port %d: %v", port, err)
		return false
	}

	// Parse as HTTP/2 frame header.
	fr, err := http2.ReadFrameHeader(bytes.NewReader(buf))
	if err != nil {
		dlog.Debugf(ctx, "invalid frame header: %v", err)
		return false
	}

	// Check if it's a valid, but empty SETTINGS frame with no flags.
	if fr.Type != http2.FrameSettings || fr.Flags != 0 {
		dlog.Debugf(ctx, "expected SETTINGS frame and empty flags, got %v, %b", fr.Type, fr.Flags)
		return false
	}

	// The frame payload should be empty for initial SETTINGS.
	if fr.Length > 0 {
		buf = make([]byte, fr.Length)
		n, err = conn.Read(buf)
		if err != nil || n != int(fr.Length) {
			dlog.Debugf(ctx, "failed to read settings payload on port %d: %v", port, err)
			return false
		} else {
			dlog.Debugf(ctx, "SETTINGS payload %s", hex.Dump(buf))
		}
	}
	_, err = conn.Write([]byte{0, 0, 0, byte(http2.FrameSettings), byte(http2.FlagSettingsAck), 0, 0, 0, 0})
	if err != nil {
		dlog.Debugf(ctx, "failed to write ack settings frame on port %d: %v", port, err)
	}
	return err == nil
}
