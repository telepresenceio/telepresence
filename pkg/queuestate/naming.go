package queuestate

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// maxNameLen bounds every name this file derives, so a single constant fits
// a Kubernetes object name, a Kafka topic or group, and a RabbitMQ queue
// simultaneously.
const maxNameLen = 63

// hashHexLen is how many hex characters of a SHA-256 digest are kept. 20 hex
// characters (80 bits) leaves collisions practically impossible at any real
// fleet size while leaving room, within maxNameLen, for a readable segment
// derived from the logical queue name.
const hashHexLen = 20

// Prefixes identify which naming function produced a name and, combined with
// the domain-separated hash input below, guarantee that no two of these
// functions can produce the same name for the same identity tuple.
const (
	appShadowPrefix       = "tp-app-"
	appGroupPrefix        = "tp-appgrp-"
	sessionShadowPrefix   = "tp-sess-"
	splitterGroupPrefix   = "tp-split-"
	sessionGroupPrefix    = "tp-sessg-"
	drainGroupPrefix      = "tp-drain-"
	lockQueuePrefix       = "tp-lock-"
	stateConfigMapPrefix  = "tp-state-"
	agentDeploymentPrefix = "tp-agent-"
)

// AppShadowName is the bounded deterministic name of the durable shadow that
// holds messages matching no developer route.
func AppShadowName(installID, workloadUID, logicalQueue, activationID string) string {
	return buildName(appShadowPrefix, "app-shadow", logicalQueue, installID, workloadUID, logicalQueue, activationID)
}

// SessionShadowName is the bounded deterministic name of one developer
// route's durable shadow.
func SessionShadowName(installID, workloadUID, logicalQueue, activationID, routeID string) string {
	return buildName(sessionShadowPrefix, "session-shadow", logicalQueue, installID, workloadUID, logicalQueue, activationID, routeID)
}

// AppGroupName is the bounded deterministic name of the Kafka consumer group
// the redirected application uses to consume its app shadow topic.
func AppGroupName(installID, workloadUID, logicalQueue, activationID string) string {
	return buildName(appGroupPrefix, "app-group", logicalQueue, installID, workloadUID, logicalQueue, activationID)
}

// SplitterGroupName is the bounded deterministic name of the Kafka consumer
// group (or equivalent source-consuming identity) the engine uses to consume
// the source stream.
func SplitterGroupName(installID, workloadUID, logicalQueue, activationID string) string {
	return buildName(splitterGroupPrefix, "splitter-group", logicalQueue, installID, workloadUID, logicalQueue, activationID)
}

// SessionGroupName is the bounded deterministic name of the shadow-topic
// consumer group a developer's local consumer reads from.
func SessionGroupName(installID, workloadUID, logicalQueue, activationID, routeID string) string {
	return buildName(sessionGroupPrefix, "session-group", logicalQueue, installID, workloadUID, logicalQueue, activationID, routeID)
}

// DrainGroupName is the bounded deterministic name of the group that drains
// a route's unconsumed shadow suffix into the app shadow after the route's
// local consumer is gone.
func DrainGroupName(installID, workloadUID, logicalQueue, activationID, routeID string) string {
	return buildName(drainGroupPrefix, "drain-group", logicalQueue, installID, workloadUID, logicalQueue, activationID, routeID)
}

// LockQueueName is the bounded deterministic name of the exclusive lock
// queue an engine holds before consuming the source, fencing a delayed
// predecessor process.
func LockQueueName(installID, workloadUID, logicalQueue, activationID string) string {
	return buildName(lockQueuePrefix, "lock-queue", logicalQueue, installID, workloadUID, logicalQueue, activationID)
}

// StateConfigMapName is the bounded deterministic name of the ConfigMap that
// holds a workload's WorkloadState document.
func StateConfigMapName(installID, workloadUID string) string {
	return buildName(stateConfigMapPrefix, "state-configmap", "", installID, workloadUID)
}

// AgentDeploymentName is the bounded deterministic name of the queue-agent
// Deployment for a workload.
func AgentDeploymentName(installID, workloadUID string) string {
	return buildName(agentDeploymentPrefix, "agent-deployment", "", installID, workloadUID)
}

// buildName derives a name from prefix, kind, and hashParts, then -- when
// room permits -- appends a sanitized, truncated rendering of humanSeg for
// readability. kind domain-separates the hash so that two naming functions
// given identical hashParts still hash differently, and humanSeg is never
// embedded verbatim: it is hashed like every other identity component and,
// when included at all, is also sanitized and bounded before being rendered.
func buildName(prefix, kind, humanSeg string, hashParts ...string) string {
	digest := digestHex(append([]string{kind}, hashParts...)...)
	if len(digest) > hashHexLen {
		digest = digest[:hashHexLen]
	}
	base := prefix + digest
	if humanSeg == "" {
		return base
	}
	seg := sanitizeSegment(humanSeg)
	if seg == "" {
		return base
	}
	budget := maxNameLen - len(prefix) - len(digest) - 1 // 1 for the dash before digest
	if budget <= 0 {
		return base
	}
	if len(seg) > budget {
		seg = strings.TrimRight(seg[:budget], "-")
	}
	if seg == "" {
		return base
	}
	return prefix + seg + "-" + digest
}

// digestHex returns the lowercase hex SHA-256 digest of parts, each framed as
// its decimal length, a ':', and its bytes. Length-prefixing, rather than a
// separator byte, means no part's content -- whatever it contains -- can
// shift where one part ends and the next begins.
func digestHex(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sanitizeSegment lowercases s, maps every character outside [a-z0-9] to
// '-', and collapses and trims '-' so the result has no leading, trailing,
// or repeated dash.
func sanitizeSegment(s string) string {
	lower := strings.ToLower(s)
	b := make([]byte, 0, len(lower))
	for _, r := range lower {
		var c byte
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			c = byte(r)
		default:
			c = '-'
		}
		if c == '-' {
			if len(b) == 0 || b[len(b)-1] == '-' {
				continue
			}
		}
		b = append(b, c)
	}
	return strings.TrimRight(string(b), "-")
}
