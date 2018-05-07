from typing import Dict

from telepresence.remote import RemoteInfo
from telepresence.runner import Runner


def _get_remote_env(
    runner: Runner, context: str, namespace: str, pod_name: str,
    container_name: str
) -> Dict[str, str]:
    """Get the environment variables in the remote pod."""
    env = runner.get_kubectl(
        context, namespace,
        ["exec", pod_name, "--container", container_name, "env"]
    )
    result = {}  # type: Dict[str,str]
    prior_key = None
    for line in env.splitlines():
        try:
            key, value = line.split("=", 1)
            prior_key = key
        except ValueError:
            # Prior key's value contains one or more newlines
            assert prior_key is not None
            key = prior_key
            value = result[key] + "\n" + line
        result[key] = value
    return result


def get_env_variables(runner: Runner, remote_info: RemoteInfo,
                      context: str) -> Dict[str, str]:
    """
    Generate environment variables that match kubernetes.
    """
    span = runner.span()
    # Get the environment:
    remote_env = _get_remote_env(
        runner, context, remote_info.namespace, remote_info.pod_name,
        remote_info.container_name
    )
    # Tell local process about the remote setup, useful for testing and
    # debugging:
    result = {
        "TELEPRESENCE_POD": remote_info.pod_name,
        "TELEPRESENCE_CONTAINER": remote_info.container_name
    }
    # Alpine, which we use for telepresence-k8s image, automatically sets these
    # HOME, PATH, HOSTNAME. The rest are from Kubernetes:
    for key in ("HOME", "PATH", "HOSTNAME"):
        if key in remote_env:
            del remote_env[key]
    result.update(remote_env)
    span.end()
    return result
