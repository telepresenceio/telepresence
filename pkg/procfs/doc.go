//go:build linux

// Package procfs reads /proc/<pid> of *other* processes: their environment,
// filesystem root, and user-namespace UID mapping. It is used by the
// node-hosted traffic-agent to acquire a target container's environment and
// filesystem without entering the container (no setns, no exec inside it).
//
// Reading a foreign process's /proc entries this way requires ptrace access
// to that process (CAP_SYS_PTRACE, or same-uid subject to the kernel's
// ptrace_scope sysctl).
package procfs
