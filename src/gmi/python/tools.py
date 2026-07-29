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

    def __init__(self, instance: str = None):
        self._base = gmi.rest_url() + "/api/v1/" + gmi.resolve_instance(instance)

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


class ToolSlots:
    """REST client for the RAW tool table store at ``/api/v1/{instance}/``.

    This is the slot-addressed view — the tool table as the interpreter sees
    it, an array indexed by idx with idx 0 the spindle. It is what
    ``stat.tool_table`` is built from, because that list has always been
    subscripted by slot (``tool_table[0]`` is the tool in the spindle, and
    ``stat.pocket_prepped`` is an index into it).

    The operator-facing, tool-number-addressed API is ``ToolTable`` above;
    a UI that edits tools wants that one.
    """

    def __init__(self, instance: str = None):
        # The raw slot store's own resolver, not instance(): its name comes from
        # milltask's tooltable_instance, reported via /info.
        self._base = gmi.rest_url() + "/api/v1/" + (instance or gmi.tooltable_instance())

    def list(self):
        """Return all occupied slots as a list of dicts, in idx order."""
        with urllib.request.urlopen(self._base + "/", timeout=5) as resp:
            return json.loads(resp.read())

    def info(self):
        """Return this store's properties as a dict (``random_toolchanger``)."""
        with urllib.request.urlopen(self._base + "/info", timeout=5) as resp:
            return json.loads(resp.read())

    def random_toolchanger(self):
        """True when an idx here IS the carousel pocket.

        Ask the store, not [EMCIO]RANDOM_TOOLCHANGER: the store is the one
        told what its idx means (on its load line), and a client reading the
        INI resolves the global section — which on a multi-instance server is
        not necessarily the section belonging to THIS machine's store.
        """
        return bool(self.info().get("random_toolchanger", False))
