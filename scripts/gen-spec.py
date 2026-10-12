#!/usr/bin/env python3
"""Make internal/spec/spec.json from the Slack Web API reference.

Reads https://docs.slack.dev/reference/methods.md and the Markdown page of
each method. Pages are kept in a cache dir, so a run again is fast.

Usage: scripts/gen-spec.py [--cache DIR] [--refresh]
"""
import argparse
import concurrent.futures
import json
import os
import re
import sys
import urllib.request

BASE = "https://docs.slack.dev/reference/methods"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "internal", "spec", "spec.json")

# Usage text of the top-level nouns.
NOUNS = {
    "admin": "Enterprise Grid admin methods (admin.*)",
    "agents": "Agent conversations and sessions",
    "api": "API test call",
    "apps": "Apps: connections, datastore, manifests, event authorizations",
    "assistant": "AI assistant threads and search",
    "auth": "Token test, revoke, and team list (API methods)",
    "blocks": "Block Kit validation",
    "bookmarks": "Channel bookmarks",
    "bots": "Bot users",
    "calls": "Calls",
    "canvases": "Canvases",
    "chat": "Messages: post, update, delete, schedule, ephemeral, permalink",
    "conversations": "Channels, DMs, and group DMs: list, history, replies, members, manage",
    "dialog": "Legacy dialogs",
    "dnd": "Do Not Disturb",
    "emoji": "Custom emoji",
    "entity": "Work Objects",
    "files": "Files: list, info, upload, delete, remote files",
    "functions": "Workflow functions",
    "migration": "Id migration",
    "oauth": "OAuth token exchange",
    "openid": "Sign in with Slack (OpenID Connect)",
    "pins": "Pinned items",
    "reactions": "Emoji reactions",
    "reminders": "Reminders",
    "rtm": "Real Time Messaging (legacy)",
    "search": "Search messages and files (user token)",
    "slack-lists": "Slack Lists",
    "stars": "Saved items (legacy)",
    "team": "Workspace info, logs, billing, preferences",
    "tooling": "Configuration token rotation",
    "usergroups": "User groups",
    "users": "Users, presence, and profiles",
    "views": "Modals and App Home views",
    "workflows": "Workflows and triggers",
}

# Params that hold JSON although the docs give no type or "string".
JSON_NAMES = {
    "blocks", "attachments", "metadata", "view", "criteria", "document_content",
    "prompts", "unfurled_attachments", "user_auth_blocks", "unfurls", "profile",
    "changes", "initial_fields", "column_defs", "schema", "manifest", "item",
    "items", "filters", "query", "chunks", "inputs", "outputs", "fields",
}


def kebab(s):
    s = re.sub(r"([a-z0-9])([A-Z])", r"\1-\2", s)
    return s.replace("_", "-").lower()


def fetch(url):
    req = urllib.request.Request(url, headers={"User-Agent": "slack-cli gen-spec"})
    with urllib.request.urlopen(req, timeout=60) as r:
        return r.read().decode("utf-8")


def cached(cache, name, url, refresh):
    path = os.path.join(cache, name)
    if not refresh and os.path.exists(path) and os.path.getsize(path) > 0:
        with open(path, encoding="utf-8") as f:
            return f.read()
    text = fetch(url)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return text


def front_matter(text):
    m = re.match(r"---\n(.*?)\n---\n", text, re.S)
    out = {"scopes": {}}
    if not m:
        return out
    section = None
    for line in m.group(1).splitlines():
        if line.startswith("scopes:"):
            section = "scopes"
            continue
        if section == "scopes" and line.startswith("  "):
            k, _, v = line.strip().partition(":")
            out["scopes"][k] = json.loads(v.strip() or "[]")
            continue
        section = None
        k, _, v = line.partition(":")
        v = v.strip()
        if v.startswith('"'):
            try:
                v = json.loads(v)
            except ValueError:
                v = v.strip('"')
        out[k.strip()] = v
    return out


def clean(s):
    s = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", s)  # links → text
    s = s.replace("**", "")
    s = re.sub(r"\s+", " ", s)
    return s.strip()


ARG_RE = re.compile(r"^\*\*`([A-Za-z0-9_.\[\]]+)`\*\*(?:`([a-z]+)`)?(Required|Optional)\s*$")


def parse_args(text):
    m = re.search(r"^## Arguments.*?$(.*?)(?=^## )", text, re.S | re.M)
    if not m:
        return []
    params = []
    cur = None
    for line in m.group(1).splitlines():
        a = ARG_RE.match(line.strip())
        if a:
            cur = {"name": a.group(1), "type": a.group(2) or "", "required": a.group(3) == "Required", "lines": []}
            params.append(cur)
            continue
        if cur is not None and not line.startswith("### "):
            cur["lines"].append(line)
    out = []
    for p in params:
        if p["name"] == "token":
            continue
        desc, example, default, enum = [], "", "", []
        for line in p.pop("lines"):
            s = line.strip()
            if s.startswith("_Example:_"):
                example = " ".join(re.findall(r"`([^`]*)`", s))
            elif s.startswith("_Default:_"):
                default = " ".join(re.findall(r"`([^`]*)`", s))
            elif s.startswith("_Acceptable values:_"):
                enum = re.findall(r"`([^`]*)`", s)
            elif s and s != "0":
                desc.append(s)
        p["desc"] = clean(" ".join(desc))
        if example:
            p["example"] = example
        if default:
            p["default"] = default
        if enum:
            p["enum"] = enum
        p["type"] = param_type(p)
        out.append(p)
    return out


def note(text):
    """Returns the first sentence of the text above the Facts section."""
    m = re.search(r"DocsCall generator\s*\n(.*?)^## Facts", text, re.S | re.M)
    if not m:
        return ""
    s = clean(m.group(1).replace(":::", " "))
    return re.split(r"(?<=\.)\s", s, maxsplit=1)[0] if s else ""


def param_type(p):
    t = p["type"]
    ex = p.get("example", "").strip()
    if t == "array":
        # Slack takes a list either as JSON or as comma-separated values.
        return "json" if ex.startswith("[") else "csv"
    if t == "object":
        return "json"
    if t in ("boolean", "integer", "number"):
        return t
    if p["name"] in JSON_NAMES or ex.startswith("{") or (ex.startswith("[{")):
        return "json"
    return "string"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cache", default=os.path.join(ROOT, ".spec-cache"))
    ap.add_argument("--refresh", action="store_true")
    args = ap.parse_args()
    os.makedirs(args.cache, exist_ok=True)

    index = cached(args.cache, "_methods.md", BASE + ".md", args.refresh)
    names = re.findall(r"^\| \[([A-Za-z0-9.]+)\]\((https://docs\.slack\.dev/reference/methods/[^)]+\.md)\)", index, re.M)
    if not names:
        sys.exit("no methods in the index")

    def load(item):
        name, url = item
        return name, url, cached(args.cache, name.lower() + ".md", url, args.refresh)

    with concurrent.futures.ThreadPoolExecutor(16) as ex:
        pages = list(ex.map(load, names))

    methods = []
    paths = {}
    for name, url, text in pages:
        fm = front_matter(text)
        parts = name.split(".")
        path = [kebab(x) for x in parts]
        key = tuple(path)
        if key in paths:
            sys.exit(f"duplicate command path {path} ({name}, {paths[key]})")
        paths[key] = name
        params = parse_args(text)
        methods.append({
            "name": fm.get("method_name") or name,
            "path": path,
            "summary": clean(fm.get("summary", "")),
            "http": fm.get("http_method", "POST"),
            "rate_limit": fm.get("rate_limit", ""),
            "scopes": fm.get("scopes") or {},
            "doc": url[:-3],
            "note": note(text),
            "params": params,
        })
    # A leaf must not also be a group.
    for key in paths:
        for i in range(1, len(key)):
            if key[:i] in paths:
                sys.exit(f"{'.'.join(key[:i])} is a method and a group")
    methods.sort(key=lambda m: m["path"])
    nouns = sorted({m["path"][0] for m in methods})
    missing = [n for n in nouns if n not in NOUNS]
    if missing:
        print("warning: no usage text for nouns:", ", ".join(missing), file=sys.stderr)
    out = {"nouns": {n: NOUNS.get(n, "") for n in nouns}, "methods": methods}
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=1, sort_keys=True, ensure_ascii=False)
        f.write("\n")
    print(f"wrote {len(methods)} methods to {OUT}")


if __name__ == "__main__":
    main()
