#!/usr/bin/env python3
"""Generate internal/spec/spec.json from the Plane API reference.

The docs at https://developers.plane.so serve each page as Markdown. This
script reads every API reference page, takes the endpoint, the path, query
and body parameters, and maps each page to a CLI command path.

Usage:
  python3 scripts/gen-spec.py [--pages DIR]

With --pages, the script reads cached pages from DIR (file names are the URL
path under /api-reference/ with "/" replaced by "__"). Without it, the script
downloads them.
"""

import json
import os
import re
import sys
import urllib.request
from concurrent.futures import ThreadPoolExecutor

BASE = "https://developers.plane.so"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "internal", "spec", "spec.json")

# v2 doc section -> (noun path, resource root relative to /workspaces/{slug}/).
SECTIONS = {
    "artifacts": (["artifact"], "artifacts/"),
    "audit-logs": (["audit-log"], "audit-logs/"),
    "collections": (["collection"], "collections/"),
    "customer-properties": (["customer", "property"], "customer-properties/"),
    "customer-property-values": (["customer", "property-value"], "customers/{customer_id}/property-values/"),
    "customer-requests": (["customer", "request"], "customers/{customer_id}/requests/"),
    "customers": (["customer"], "customers/"),
    "cycles": (["cycle"], "projects/{project_id}/cycles/"),
    "estimate-points": (["estimate", "point"], "projects/{project_id}/estimates/{estimate_id}/points/"),
    "estimates": (["estimate"], "projects/{project_id}/estimates/"),
    "group-sync": (["group-sync"], "group-sync/"),
    "initiative-labels": (["initiative", "label"], "initiatives/labels/"),
    "initiatives": (["initiative"], "initiatives/"),
    "intake-work-items": (["intake"], "projects/{project_id}/intake-issues/"),
    "invitations": (["invitation"], "invitations/"),
    "labels": (["label"], "projects/{project_id}/labels/"),
    "milestones": (["milestone"], "projects/{project_id}/milestones/"),
    "modules": (["module"], "projects/{project_id}/modules/"),
    "permission-schemes": (["permission-scheme"], "permission-schemes/"),
    "project-automations": (["automation"], "projects/{project_id}/automations/"),
    "project-features": (["project", "features"], "projects/{project_id}/features/"),
    "project-pages": (["page"], "projects/{project_id}/pages/"),
    "project-views": (["view"], "projects/{project_id}/views/"),
    "project-work-item-templates": (["template"], "projects/{project_id}/work-item-templates/"),
    "projects": (["project"], "projects/"),
    "release-comments": (["release", "comment"], "releases/{release_id}/comments/"),
    "release-labels": (["release", "label"], "releases/labels/"),
    "release-links": (["release", "link"], "releases/{release_id}/links/"),
    "release-tags": (["release", "tag"], "releases/tags/"),
    "releases": (["release"], "releases/"),
    "roles": (["role"], "roles/"),
    "states": (["state"], "projects/{project_id}/states/"),
    "stickies": (["sticky"], "stickies/"),
    "teamspaces": (["teamspace"], "teamspaces/"),
    "user-assets": (["user", "asset"], "/api/v2/users/me/assets/"),
    "webhook-logs": (["webhook", "log"], "webhook-logs/{webhook_id}/"),
    "webhooks": (["webhook"], "webhooks/"),
    "work-item-activities": (["work-item", "activity"], "projects/{project_id}/work-items/{work_item_id}/activities/"),
    "work-item-attachments": (["work-item", "attachment"], "projects/{project_id}/work-items/{work_item_id}/attachments/"),
    "work-item-comments": (["work-item", "comment"], "projects/{project_id}/work-items/{work_item_id}/comments/"),
    "work-item-links": (["work-item", "link"], "projects/{project_id}/work-items/{work_item_id}/links/"),
    "work-item-properties": (["property"], "projects/{project_id}/work-item-properties/"),
    "work-item-property-contexts": (["property", "context"], "work-item-properties/{property_id}/contexts/"),
    "work-item-property-options": (["property", "option"], "projects/{project_id}/work-item-properties/{property_id}/options/"),
    "work-item-type-properties": (["work-item-type", "property"], "projects/{project_id}/work-item-types/{type_id}/properties/"),
    "work-item-types": (["work-item-type"], "projects/{project_id}/work-item-types/"),
    "work-item-worklogs": (["work-item", "worklog"], "projects/{project_id}/work-items/{work_item_id}/worklogs/"),
    "work-items": (["work-item"], "projects/{project_id}/work-items/"),
    "workflow-states": (["workflow", "state"], "projects/{project_id}/workflows/{workflow_id}/states/"),
    "workflow-transitions": (["workflow", "transition"], "projects/{project_id}/workflows/{workflow_id}/state-transitions/"),
    "workflows": (["workflow"], "projects/{project_id}/workflows/"),
    "workspace-assets": (["asset"], "assets/"),
    "workspace-automations": (["automation"], "automations/"),
    "workspace-features": (["workspace", "features"], "features/"),
    "workspace-pages": (["page"], "pages/"),
    "workspace-views": (["view"], "views/"),
    "workspace-work-item-properties": (["property"], "work-item-properties/"),
    "workspace-work-item-property-options": (["property", "option"], "work-item-properties/{property_id}/options/"),
    "workspace-work-item-templates": (["template"], "work-item-templates/"),
    "workspace-work-item-type-properties": (["work-item-type", "property"], "work-item-types/{type_id}/properties/"),
    "workspace-work-item-types": (["work-item-type"], "work-item-types/"),
}

# Sub-collections inside a section that become their own noun.
SUBS = {
    "nodes": "node",
    "edges": "edge",
    "activities": "activity",
    "project-mappings": "project-mapping",
    "workspace-mappings": "workspace-mapping",
}

# Sections whose root is a single object, not a collection.
SINGLETONS = {"project-features", "workspace-features"}

# Explicit command paths for v2 pages that the rules do not name well.
V2_OVERRIDES = {
    "users/get-current-user": ["user", "me"],
    "members/list-workspace-members": ["member", "list"],
    "members/list-project-members": ["member", "list"],
    "members/get-project-member": ["member", "get"],
    "members/create-project-member": ["member", "create"],
    "members/update-project-member": ["member", "update"],
    "members/delete-project-member": ["member", "delete"],
    "members/remove-workspace-member": ["member", "remove"],
    "members/get-project-role-distribution": ["member", "role-distribution"],
    "permissions/get-workspace-permissions": ["permission", "me"],
    "permissions/get-project-permissions": ["permission", "me"],
    "work-items/list-workspace-work-items": ["work-item", "list"],
    "work-items/get-work-item-by-identifier": ["work-item", "get"],
    "work-item-worklogs/get-project-worklog-summary": ["work-item", "worklog", "summary"],
    "group-sync/get-group-sync-config": ["group-sync", "config", "get"],
    "group-sync/update-group-sync-config": ["group-sync", "config", "update"],
    "collections/list-collection-members": ["collection", "members"],
    "collections/manage-collection-members": ["collection", "manage-members"],
    "collections/manage-collection-pages": ["collection", "manage-pages"],
    "collections/search-collection-pages": ["collection", "search-pages"],
    "customers/manage-customer-work-items": ["customer", "manage-work-items"],
    "customer-property-values/list-customer-property-values": ["customer", "property-value", "list"],
    "customer-property-values/set-customer-property-values": ["customer", "property-value", "set"],
    "cycles/manage-cycle-work-items": ["cycle", "manage-work-items"],
    "modules/manage-module-work-items": ["module", "manage-work-items"],
    "milestones/manage-milestone-work-items": ["milestone", "manage-work-items"],
    "initiatives/manage-initiative-labels": ["initiative", "manage-labels"],
    "initiatives/manage-initiative-projects": ["initiative", "manage-projects"],
    "initiatives/manage-initiative-work-items": ["initiative", "manage-work-items"],
    "releases/manage-release-labels": ["release", "manage-labels"],
    "releases/manage-release-work-items": ["release", "manage-work-items"],
    "releases/update-release-changelog": ["release", "update-changelog"],
    "user-assets/confirm-user-asset": ["user", "asset", "confirm"],
    "workspace-assets/confirm-workspace-asset": ["asset", "confirm"],
    "work-item-attachments/confirm-attachment-upload": ["work-item", "attachment", "confirm"],
    "artifacts/update-artifact": ["artifact", "update"],
    "invitations/bulk-invitations": ["invitation", "bulk-create"],
    "work-item-type-properties/attach-type-property": ["work-item-type", "property", "attach"],
    "work-item-type-properties/detach-type-property": ["work-item-type", "property", "detach"],
    "workspace-work-item-type-properties/attach-workspace-type-property": ["work-item-type", "property", "attach"],
    "workspace-work-item-type-properties/detach-workspace-type-property": ["work-item-type", "property", "detach"],
    "webhooks/regenerate-webhook-secret": ["webhook", "regenerate-secret"],
}

# v1-only endpoints (no v2 counterpart yet): page file -> command path.
V1_PAGES = {
    "epics/list-epics": ["epic", "list"],
    "epics/get-epic-detail": ["epic", "get"],
    "epics/create-epic": ["epic", "create"],
    "epics/update-epic": ["epic", "update"],
    "epics/delete-epic": ["epic", "delete"],
    "epics/list-epic-work-items": ["epic", "list-work-items"],
    "epics/add-epic-work-items": ["epic", "add-work-items"],
    "work-item-relations/list-work-item-relations": ["work-item", "relation", "list"],
    "work-item-relations/create-work-item-relation": ["work-item", "relation", "create"],
    "work-item-relations/remove-work-item-relation": ["work-item", "relation", "remove"],
    "work-item-pages/list-work-item-pages": ["work-item", "page", "list"],
    "work-item-pages/get-work-item-page-detail": ["work-item", "page", "get"],
    "work-item-pages/add-work-item-page": ["work-item", "page", "add"],
    "work-item-pages/delete-work-item-page": ["work-item", "page", "delete"],
    "issue/search-issues": ["work-item", "search"],
    "issue/advanced-search-work-items": ["work-item", "advanced-search"],
    "issue-types/values/list-property-values": ["work-item", "property-value", "list"],
    "issue-types/values/get-property-value-detail": ["work-item", "property-value", "get"],
    "issue-types/values/add-property-values": ["work-item", "property-value", "set"],
    "issue-types/values/update-property-value": ["work-item", "property-value", "update"],
    "issue-types/values/delete-property-value": ["work-item", "property-value", "delete"],
    "teamspace/list-teamspace-members": ["teamspace", "member", "list"],
    "teamspace/add-teamspace-members": ["teamspace", "member", "add"],
    "teamspace/remove-teamspace-members": ["teamspace", "member", "remove"],
    "teamspace/list-teamspace-projects": ["teamspace", "project", "list"],
    "teamspace/add-projects-to-teamspace": ["teamspace", "project", "add"],
    "teamspace/remove-projects-from-teamspace": ["teamspace", "project", "remove"],
    "cycle/list-archived-cycles": ["cycle", "list-archived"],
    "cycle/archive-cycle": ["cycle", "archive"],
    "cycle/unarchive-cycle": ["cycle", "unarchive"],
    "cycle/list-cycle-work-items": ["cycle", "list-work-items"],
    "module/list-archived-modules": ["module", "list-archived"],
    "module/archive-module": ["module", "archive"],
    "module/unarchive-module": ["module", "unarchive"],
    "module/list-module-work-items": ["module", "list-work-items"],
    "milestones/list-milestone-work-items": ["milestone", "list-work-items"],
    "page/archive-project-page": ["page", "archive"],
    "page/archive-workspace-page": ["page", "archive"],
    "page/restore-project-page": ["page", "unarchive"],
    "page/restore-workspace-page": ["page", "unarchive"],
    "initiative/list-initiative-epics": ["initiative", "list-epics"],
    "initiative/add-epics-to-initiative": ["initiative", "add-epics"],
    "initiative/remove-epics-from-initiative": ["initiative", "remove-epics"],
    "initiative/list-initiative-labels-for-initiative": ["initiative", "list-labels"],
    "initiative/list-initiative-projects": ["initiative", "list-projects"],
    "collection/list-collection-pages": ["collection", "list-pages"],
    "collection/move-or-reorder-collection-page": ["collection", "move-page"],
    "customer/list-customer-work-items": ["customer", "list-work-items"],
    "customer/get-customer-property-value": ["customer", "property-value", "get"],
    "customer/update-customer-property-value": ["customer", "property-value", "update"],
    "release/list-release-work-items": ["release", "list-work-items"],
    "release/list-labels-for-release": ["release", "list-labels"],
    "project-labels/list-project-labels": ["project-label", "list"],
    "project-labels/get-project-label-detail": ["project-label", "get"],
    "project-labels/add-project-label": ["project-label", "create"],
    "project-labels/update-project-label-detail": ["project-label", "update"],
    "project-labels/delete-project-label": ["project-label", "delete"],
    "project/create-project-with-template": ["project", "create-from-template"],
    "workspace-invitations/update-workspace-invitation": ["invitation", "update"],
}

NOUN_USAGE = {
    "artifact": "Workspace artifacts",
    "asset": "Workspace file assets (upload, confirm, delete)",
    "audit-log": "Workspace audit trail",
    "automation": "Automations (workspace, or project with --project)",
    "collection": "Page collections",
    "customer": "Customers, their properties, requests, and property values",
    "cycle": "Cycles (time-boxed iterations)",
    "epic": "Epics (v1 API)",
    "estimate": "Estimates and estimate points",
    "group-sync": "IdP group sync config and mappings",
    "initiative": "Initiatives and initiative labels",
    "intake": "Intake work items",
    "invitation": "Workspace invitations",
    "label": "Project labels",
    "member": "Workspace and project members",
    "milestone": "Milestones",
    "module": "Modules",
    "page": "Pages (workspace wiki, or project pages with --project)",
    "permission": "Effective permissions of the caller",
    "permission-scheme": "Permission schemes",
    "project": "Projects",
    "project-label": "Workspace-level project labels (v1 API)",
    "property": "Custom work item properties, options, and contexts",
    "release": "Releases, release comments, labels, links, and tags",
    "role": "Workspace roles",
    "state": "Workflow states",
    "sticky": "Stickies",
    "teamspace": "Teamspaces, their members and projects",
    "template": "Work item templates (workspace, or project with --project)",
    "user": "The current user and personal assets",
    "view": "Saved views (workspace, or project with --project)",
    "webhook": "Webhooks and delivery logs",
    "work-item": "Work items and their comments, links, attachments, worklogs, activity, relations",
    "work-item-type": "Work item types and their properties",
    "workflow": "Workflows, workflow states, and transitions",
    "workspace": "Workspace features",
}


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as r:
        return r.read().decode()


def load_pages(pages_dir):
    pages = {}
    if pages_dir:
        for f in os.listdir(pages_dir):
            if f.endswith(".md"):
                key = f[:-3].replace("__", "/")
                pages[key] = open(os.path.join(pages_dir, f)).read()
        return pages
    idx = fetch(BASE + "/llms.txt")
    urls = sorted(set(re.findall(r"https://developers\.plane\.so/api-reference/[^)\s]+\.md", idx)))

    def get(u):
        key = u.split("/api-reference/", 1)[1][:-3]
        return key, fetch(u)

    with ThreadPoolExecutor(16) as ex:
        for k, v in ex.map(get, urls):
            pages[k] = v
    return pages


PARAM_RE = re.compile(r'<ApiParam name="([^"]+)" type="([^"]*)" :required="(true|false)">\n(.*?)</ApiParam>', re.S)
BADGE_RE = re.compile(r'class="method (\w+)">\w+</span>\s*<span class="path">([^<]+)</span>')


def clean(s):
    s = re.sub(r"\[([^\]]+)\]\([^)]+\)", r"\1", s)
    s = s.replace("**", "")
    return re.sub(r"\s+", " ", s).strip()


def first_para(text):
    paras = [p.strip() for p in re.split(r"\n\s*\n", text) if p.strip()]
    for p in paras:
        if p.startswith(("<", ":::", "```", "|", "#")):
            continue
        return clean(p)
    return ""


def enums(text):
    vals = re.findall(r"^- `([^`]+)` - ", text, re.M)
    if vals:
        return vals
    m = re.search(r"One of ((?:`[^`]+`(?:, | or |, or )?)+)", text)
    if m:
        return re.findall(r"`([^`]+)`", m.group(1))
    return []


def short(desc, n=150):
    m = re.match(r"(.+?[.!?])(\s|$)", desc)
    s = m.group(1) if m else desc
    return s if len(s) <= n else s[: n - 1] + "…"


def parse_page(text):
    title = (re.search(r"^title:\s*(.+)$", text, re.M) or [None, ""])[1].strip()
    badges = BADGE_RE.findall(text)
    if not badges:
        return None
    method, path = badges[0][0].upper(), badges[0][1].strip()
    body = text.split("</div>", 1)[1] if "</div>" in text else text
    left = body.split("### ", 1)[0]
    summary = first_para(re.sub(r"<div[^>]*>", "", left))
    params = []
    section = ""
    pos = 0
    heads = [(m.start(), m.group(1)) for m in re.finditer(r"^#{2,3} (.+)$", text, re.M)]
    for m in PARAM_RE.finditer(text):
        for hpos, h in heads:
            if hpos < m.start():
                section = h
        loc = "query"
        if section.startswith("Path"):
            loc = "path"
        elif section.startswith("Body"):
            loc = "body"
        elif section.startswith(("Attributes", "Response Attributes", "Response fields", "Response Fields")):
            continue
        raw = m.group(4)
        desc = short(first_para(raw))
        ev = enums(raw)
        for name in [x.strip() for x in m.group(1).split(",")]:
            params.append({
                "name": name,
                "in": loc,
                "type": m.group(2),
                "required": m.group(3) == "true",
                "desc": desc,
                **({"enum": ev} if ev else {}),
            })
        pos = m.end()
    return {"title": title, "method": method, "path": path, "summary": summary, "params": params}


def is_param(seg):
    return seg.startswith("{") and seg.endswith("}")


def derive(section, method, path):
    nouns, root = SECTIONS[section]
    if root.startswith("/"):
        rel = path[len(root):] if path.startswith(root) else None
    else:
        prefix = re.match(r"/api/v2/workspaces/\{[a-z_]+\}/", path)
        rest = path[prefix.end():] if prefix else ""
        rel = rest[len(root):] if rest.startswith(root) else None
    if rel is None:
        return None
    segs = [s for s in rel.split("/") if s]
    nouns = list(nouns)
    while segs:
        if segs[0] in SUBS:
            nouns.append(SUBS[segs[0]])
            segs = segs[1:]
        elif len(segs) >= 2 and is_param(segs[0]) and segs[1] in SUBS:
            nouns.append(SUBS[segs[1]])
            segs = segs[2:]
        else:
            break
    if section in SINGLETONS and not segs:
        return nouns + [{"GET": "get", "PATCH": "update"}[method]]
    if not segs:
        return nouns + [{"GET": "list", "POST": "create"}[method]]
    if len(segs) == 1 and is_param(segs[0]):
        return nouns + [{"GET": "get", "PATCH": "update", "PUT": "update", "DELETE": "delete"}[method]]
    if len(segs) == 1:
        return nouns + [segs[0]]
    if len(segs) == 2 and is_param(segs[0]):
        return nouns + [segs[1]]
    if len(segs) == 3 and is_param(segs[0]) and is_param(segs[2]) and segs[1] in SUBS:
        return None
    return None


def main():
    pages_dir = None
    if "--pages" in sys.argv:
        pages_dir = sys.argv[sys.argv.index("--pages") + 1]
    pages = load_pages(pages_dir)

    commands = {}
    problems = []

    def add(cmd, version, key, ep):
        c = commands.setdefault(tuple(cmd), {"path": cmd, "variants": []})
        ep = dict(ep)
        ep["version"] = version
        ep["doc"] = f"{BASE}/api-reference/{key}"
        ep["path"] = re.sub(r"^https://api\.plane\.so", "", ep["path"])
        c["variants"].append(ep)

    for key, text in sorted(pages.items()):
        if not key.startswith("v2/"):
            continue
        parts = key.split("/")
        if len(parts) != 3 or parts[2] == "overview":
            continue
        section, slug = parts[1], parts[2]
        ep = parse_page(text)
        if not ep:
            continue
        okey = f"{section}/{slug}"
        cmd = V2_OVERRIDES.get(okey)
        if not cmd and section in SECTIONS:
            cmd = derive(section, ep["method"], ep["path"])
        if not cmd:
            problems.append(f"unmapped v2 {okey} {ep['method']} {ep['path']}")
            continue
        add(cmd, "v2", key, ep)

    for key, cmd in V1_PAGES.items():
        if key not in pages:
            problems.append(f"missing v1 page {key}")
            continue
        ep = parse_page(pages[key])
        if not ep:
            problems.append(f"no endpoint in v1 page {key}")
            continue
        add(cmd, "v1", key, ep)

    for c in commands.values():
        vs = c["variants"]
        scopes = sorted("{project_id}" in v["path"] for v in vs)
        if len(vs) > 2 or (len(vs) == 2 and scopes != [False, True]):
            problems.append(f"conflict {' '.join(c['path'])}: " + ", ".join(v["method"] + " " + v["path"] for v in vs))

    out = {
        "nouns": NOUN_USAGE,
        "commands": sorted(commands.values(), key=lambda c: c["path"]),
    }
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w") as f:
        json.dump(out, f, indent=1, sort_keys=True)
        f.write("\n")
    for p in problems:
        print(p, file=sys.stderr)
    print(f"wrote {len(commands)} commands to {OUT}", file=sys.stderr)


if __name__ == "__main__":
    main()
