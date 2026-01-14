package tls

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/telepresenceio/dlib/v2/dgroup"
	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type Manager interface {
	// GetDownstreamCertificate returns the TLS certificate for the given proxy port.
	GetDownstreamCertificate(proxyPort uint16) *tls.Certificate

	// GetUpstreamCertificate returns the TLS certificate for the given proxy port and a boolean indicating if
	// InsecureSkipVerify should be used when connecting to the upstream.
	GetUpstreamCertificate(proxyPort uint16) (*tls.Certificate, bool)

	// UseHTTP2 returns true if the given proxy port supports HTTP/2.
	UseHTTP2(ctx context.Context, proxyPort uint16) bool

	// UseTLS returns true if the given proxy port supports TLS.
	UseTLS(ctx context.Context, proxyPort uint16) bool

	// StartWatchers starts the watchers that keep the TLS configuration up to date.
	StartWatchers(g *dgroup.Group, certsReady chan<- struct{})
}

func NewManager(ctx context.Context, config *agentconfig.Sidecar, podIP netip.Addr, annotations map[string]string) (Manager, error) {
	m := &manager{
		sidecarConfig: config,
		podIP:         podIP,
		portConfigs:   make(map[uint16]*portConfig, len(config.Containers)),
	}
	err := m.createPortConfigs(ctx, annotations)
	if err != nil {
		return nil, err
	}
	return m, nil
}

const defaultProbeTimeout = 2 * time.Second

type manager struct {
	// The IP address of this pod.
	podIP netip.Addr

	// portConfigs maps proxy port numbers to port configurations. One for each interceptable port that
	// might be targeted by HTTP filtered intercepts.
	portConfigs map[uint16]*portConfig

	// The sidecar configuration created by the traffic manager.
	sidecarConfig *agentconfig.Sidecar
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
	usPTs, err := readPathAnnotations(am, annotation.UpstreamProbeTimeout)
	if err != nil {
		return err
	}

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
			// The ports must be keyed by the proxy port, not the actual container port.
			cp := ic.ContainerPort

			// The port config is associated with the proxy port, not the container port.
			iap := m.sidecarConfig.InterceptorInactivePort(cp, types.ProtoTCP)
			pc := newPortConfig(iap, cn.Name, m.sidecarConfig.EnableH2cProbing)
			m.portConfigs[iap] = pc

			// The configured TLS and HTTP/2 values must be determined using the container port.
			pc.TLS = m.configuredTLS(cp)
			pc.HTTP2 = m.configuredHTTP2(cp)

			var dsPath, usPath, usISV, usPT string
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
			if usPT, ok = usPTs[cp]; ok {
				delete(usPTs, cp)
			}
			if dsPath == "" && usPath == "" && usISV == "" && usPT == "" {
				continue
			}

			pc.downstreamSecretPath = dsPath
			pc.upstreamSecretPath = usPath
			switch usISV {
			case "enabled":
				pc.upstreamInsecureSkipVerify = true
			case "", "disabled":
			default:
				return fmt.Errorf(`invalid value %q for annotation %s. Expected "enabled" or "disabled": %w`, usISV, annotation.UpstreamInsecureSkipVerify, err)
			}
			if usPT != "" {
				pc.upstreamProbeTimeout, err = time.ParseDuration(usPT)
				if err != nil {
					return fmt.Errorf(`invalid value %q for annotation %s. Expected a duration: %w`, usPT, annotation.UpstreamProbeTimeout, err)
				}
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
	warnNotHTTPPort(usISVs, annotation.UpstreamProbeTimeout)
	return nil
}

func (m *manager) GetDownstreamCertificate(proxyPort uint16) *tls.Certificate {
	pc, ok := m.portConfigs[proxyPort]
	if ok {
		return pc.getDownstreamCert()
	}
	return nil
}

func (m *manager) GetUpstreamCertificate(proxyPort uint16) (*tls.Certificate, bool) {
	pc, ok := m.portConfigs[proxyPort]
	if ok {
		return pc.getUpstreamCert()
	}
	return nil, false
}

func (m *manager) StartWatchers(g *dgroup.Group, certsReady chan<- struct{}) {
	allReady := sync.WaitGroup{}
	allReady.Add(len(m.portConfigs))
	for p, pc := range m.portConfigs {
		g.Go(fmt.Sprintf("watch-tls/%d", p), func(ctx context.Context) error {
			return pc.watchPaths(ctx, &allReady)
		})
	}
	allReady.Wait()
	close(certsReady)
}

func (m *manager) UseTLS(ctx context.Context, proxyPort uint16) bool {
	pc, ok := m.portConfigs[proxyPort]
	if !ok {
		return false
	}
	if pc.TLS != ValueUnknown {
		return pc.TLS == ValueSupported
	}
	return pc.probeTLS(ctx, m.podIP)
}

func (m *manager) UseHTTP2(ctx context.Context, proxyPort uint16) bool {
	pc, ok := m.portConfigs[proxyPort]
	if !ok {
		return false
	}
	if pc.HTTP2 != ValueUnknown {
		return pc.HTTP2 == ValueSupported
	}
	return pc.probeHTTP2(ctx, m.podIP)
}

func (m *manager) configuredHTTP2(containerPort uint16) ValueState {
	_, it := m.sidecarConfig.InterceptTarget(containerPort, types.ProtoTCP)
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

func (m *manager) configuredTLS(containerPort uint16) ValueState {
	_, it := m.sidecarConfig.InterceptTarget(containerPort, types.ProtoTCP)
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
