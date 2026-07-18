// Package quicfwd implements the pure wire-format logic a stateless QUIC packet
// forwarder needs, per "The forwarder" section of
// docs/reference/quic-transport-architecture.md: no sockets, no goroutines, no Kubernetes -- just
// parsing and codec functions that a runtime (the quic-forwarder command,
// cmd/traffic/cmd/quicforwarder) calls to route packets.
//
// Two mechanisms, following the QUIC-LB pattern (draft-ietf-quic-load-balancers), cover
// every packet in a connection's life:
//
//   - A connection's first packet (the client Initial) is routed by the SNI in its
//     ClientHello, extracted without terminating TLS (ExtractSNI). SNI names identify
//     the backend (ManagerSNI, AgentSNI, ParseSNI).
//   - Every subsequent packet is routed by its Destination Connection ID, which a
//     backend mints (via CIDGenerator, for use with quic-go's quic.Transport) to encode
//     its own pod IP (EncodeCID, DecodeCID). A forwarder can therefore route any
//     mid-connection packet with no flow table at all.
//
// ParsePacket classifies any QUIC packet -- long or short header -- far enough to
// dispatch it to one of the two mechanisms above.
package quicfwd
