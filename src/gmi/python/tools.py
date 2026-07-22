"""GMI ToolTable client — REST interface to the tool table shared memory."""

import json
import urllib.request

import gmi


class ToolTable:
    """REST client for the tool table API.

    Provides list/get/put/delete/reload operations against the tools API the
    milltask module registers under its own instance name — routes are
    ``/api/v1/{instance}/…`` (default instance "milltask"). Not to be
    confused with the tooltable module's separate raw store at
    ``/api/v1/tooltable/`` (review finding GP-14).
    """

    def __init__(self, instance: str = "milltask"):
        self._base = gmi.rest_url() + "/api/v1/" + instance

    def list(self):
        """Return all tools as a list of dicts."""
        with urllib.request.urlopen(self._base + "/", timeout=5) as resp:
            return json.loads(resp.read())

    def get(self, toolno):
        """Return a single tool dict by tool number, or None if not found.

        The server deliberately answers an absent tool with the zero entry
        (toolno == 0), not a 404 — mapping only 404 to None returned a
        phantom all-zero tool for every missing number (finding GP-6).
        """
        url = self._base + "/%d" % toolno
        try:
            with urllib.request.urlopen(url, timeout=5) as resp:
                t = json.loads(resp.read())
        except urllib.error.HTTPError as e:
            if e.code == 404:
                return None
            raise
        if not t or t.get("toolno", 0) == 0:
            return None
        return t

    def put(self, toolno, entry):
        """Create or update a tool. entry is a dict of ToolEntry fields.

        The wire contract is ``{"entry": {...}}`` (put_tool's ``entry``
        param; toolno comes from the path). The fields were once sent flat —
        the server ignored them as unknown keys and persisted a zero entry
        while returning 200 (finding GP-1).
        """
        e = dict(entry)
        e["toolno"] = toolno
        data = json.dumps({"entry": e}).encode("utf-8")
        req = urllib.request.Request(
            self._base + "/%d" % toolno,
            data=data,
            headers={"Content-Type": "application/json"},
            method="PUT",
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read())

    def delete(self, toolno):
        """Delete a tool by tool number."""
        req = urllib.request.Request(
            self._base + "/%d" % toolno,
            method="DELETE",
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read())

    def reload(self):
        """Reload the tool table from file (sends NML load-tool-table command)."""
        req = urllib.request.Request(
            self._base + "/reload",
            method="POST",
            headers={"Content-Type": "application/json"},
            data=b"{}",
        )
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read())
