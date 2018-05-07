import json
import platform
from pathlib import Path
from urllib import request
from uuid import uuid4

import os

from telepresence import __version__


class Scout:
    def __init__(self, app, version, install_id, **kwargs):
        self.app = Scout.__not_blank("app", app)
        self.version = Scout.__not_blank("version", version)
        self.install_id = Scout.__not_blank("install_id", install_id)
        self.metadata = kwargs if kwargs is not None else {}
        self.user_agent = self.create_user_agent()

        # scout options; controlled via env vars
        self.scout_host = os.getenv("SCOUT_HOST", "kubernaut.io")
        self.use_https = os.getenv("SCOUT_HTTPS",
                                   "1").lower() in {"1", "true", "yes"}
        self.disabled = Scout.__is_disabled()

    def report(self, **kwargs):
        result = {'latest_version': self.version}

        if self.disabled:
            return result

        merged_metadata = Scout.__merge_dicts(self.metadata, kwargs)

        headers = {
            'User-Agent': self.user_agent,
            'Content-Type': 'application/json'
        }

        payload = {
            'application': self.app,
            'version': self.version,
            'install_id': self.install_id,
            'user_agent': self.create_user_agent(),
            'metadata': merged_metadata
        }

        url = ("https://" if self.use_https else
               "http://") + "{}/scout".format(self.scout_host).lower()
        try:
            req = request.Request(
                url,
                data=json.dumps(payload).encode("UTF-8"),
                headers=headers,
                method="POST"
            )
            resp = request.urlopen(req)
            if resp.code / 100 == 2:
                result = Scout.__merge_dicts(
                    result, json.loads(resp.read().decode("UTF-8"))
                )
        except Exception as e:
            # If scout is down or we are getting errors just proceed as if
            # nothing happened. It should not impact the user at all.
            result["FAILED"] = str(e)

        return result

    def create_user_agent(self):
        result = "{0}/{1} ({2}; {3}; python {4})".format(
            self.app, self.version, platform.system(), platform.release(),
            platform.python_version()
        ).lower()

        return result

    @staticmethod
    def __not_blank(name, value):
        if value is None or str(value).strip() == "":
            raise ValueError(
                "Value for '{}' is blank, empty or None".format(name)
            )

        return value

    @staticmethod
    def __merge_dicts(x, y):
        z = x.copy()
        z.update(y)
        return z

    @staticmethod
    def __is_disabled():
        if str(os.getenv("TRAVIS_REPO_SLUG")).startswith("datawire/"):
            return True

        return os.getenv("SCOUT_DISABLE", "0").lower() in {"1", "true", "yes"}


def call_scout(session):
    args = session.args
    kube_info = session.kube_info

    config_root = Path.home() / ".config" / "telepresence"
    config_root.mkdir(parents=True, exist_ok=True)
    id_file = config_root / 'id'

    if args.deployment:
        operation = "deployment"
    elif args.new_deployment:
        operation = "new_deployment"
    elif args.swap_deployment:
        operation = "swap_deployment"
    else:
        operation = "bad_args"

    scout_kwargs = dict(
        kubectl_version=kube_info.kubectl_version,
        kubernetes_version=kube_info.cluster_version,
        operation=operation,
        method=args.method
    )

    try:
        with id_file.open('x') as f:
            install_id = str(uuid4())
            f.write(install_id)
            scout_kwargs["new_install"] = True
    except FileExistsError:
        with id_file.open('r') as f:
            install_id = f.read()
            scout_kwargs["new_install"] = False

    scout = Scout("telepresence", __version__, install_id)
    scouted = scout.report(**scout_kwargs)

    session.output.write("Scout info: {}\n".format(scouted))
