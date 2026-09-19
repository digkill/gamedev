"""Validate a GameProject v1 with the shared JSON Schema and semantic checks.

This tool runs during development/CI. The Android runtime and Go backend must
also enforce their own supported subset before executing or storing a project.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from urllib.parse import urljoin

from jsonschema import Draft202012Validator
from referencing import Registry, Resource


ROOT = Path(__file__).resolve().parent
SCHEMA_DIR = ROOT / "project-schema"
PROJECT_SCHEMA_URI = "https://sandbox.example/contracts/project/v1"


class ProjectError(ValueError):
    pass


def _validator() -> Draft202012Validator:
    registry = Registry()
    root_schema: dict | None = None
    for path in SCHEMA_DIR.glob("*.schema.json"):
        schema = json.loads(path.read_text(encoding="utf-8"))
        Draft202012Validator.check_schema(schema)
        resource = Resource.from_contents(schema)
        registry = registry.with_resource(schema["$id"], resource)
        # The standalone schemas use stable IDs; relative $refs in the bundle
        # also need a path alias under the project schema base URI.
        registry = registry.with_resource(urljoin(PROJECT_SCHEMA_URI, path.name), resource)
        if path.name == "project.schema.json":
            root_schema = schema
    if root_schema is None:
        raise ProjectError("project.schema.json is missing")
    return Draft202012Validator(root_schema, registry=registry)


VALIDATOR = _validator()


def validate_project(project: object) -> None:
    errors = sorted(VALIDATOR.iter_errors(project), key=lambda e: str(e.absolute_path))
    if errors:
        err = errors[0]
        location = "/".join(map(str, err.absolute_path)) or "root"
        raise ProjectError(f"schema at {location}: {err.message}")
    assert isinstance(project, dict)
    manifest = project["manifest"]
    scene_ids: set[str] = set()
    all_entity_ids: set[str] = set()
    prefab_ids = {p["id"] for p in project["prefabs"]}
    graph_ids = {g["id"] for g in project["graphs"]}
    script_ids = {s["id"] for s in project["scripts"]}
    asset_ids = {a["id"] for a in project["assets"]}
    for category, values in (
        ("prefab", project["prefabs"]), ("graph", project["graphs"]),
        ("script", project["scripts"]), ("asset", project["assets"]),
    ):
        if len({item["id"] for item in values}) != len(values):
            raise ProjectError(f"duplicate {category} ID")

    for scene in project["scenes"]:
        if scene["id"] in scene_ids:
            raise ProjectError("duplicate scene ID")
        scene_ids.add(scene["id"])
        if scene["mode"] != manifest["mode"]:
            raise ProjectError("scene mode differs from project mode")
        _validate_entities(scene["entities"], scene["mode"], prefab_ids, graph_ids, script_ids, asset_ids, all_entity_ids)

    if manifest["entry_scene_id"] not in scene_ids:
        raise ProjectError("entry scene does not exist")
    for prefab in project["prefabs"]:
        if prefab["mode"] != manifest["mode"]:
            raise ProjectError("prefab mode differs from project mode")
        _validate_entities(prefab["entities"], prefab["mode"], prefab_ids, graph_ids, script_ids, asset_ids, all_entity_ids)
    for graph in project["graphs"]:
        node_ids = [node["id"] for node in graph["nodes"]]
        if len(set(node_ids)) != len(node_ids):
            raise ProjectError("duplicate graph node ID")
        allowed_nodes = set(node_ids)
        for edge in graph["edges"]:
            if edge["from"] not in allowed_nodes or edge["to"] not in allowed_nodes:
                raise ProjectError("graph edge references a missing node")
        for node in graph["nodes"]:
            if node.get("target_scene_id") not in (None, *scene_ids):
                raise ProjectError("graph references a missing scene")
            if node.get("target_prefab_id") not in (None, *prefab_ids):
                raise ProjectError("graph references a missing prefab")
            if node.get("target_entity_id") not in (None, *all_entity_ids):
                raise ProjectError("graph references a missing entity")


def _validate_entities(
    entities: list[dict], mode: str, prefab_ids: set[str], graph_ids: set[str],
    script_ids: set[str], asset_ids: set[str], all_entity_ids: set[str],
) -> None:
    local_ids = [e["id"] for e in entities]
    if len(set(local_ids)) != len(local_ids):
        raise ProjectError("duplicate entity ID in scene/prefab")
    if any(entity_id in all_entity_ids for entity_id in local_ids):
        raise ProjectError("entity ID reused in another scene/prefab")
    all_entity_ids.update(local_ids)
    parents = {e["id"]: e["parent_id"] for e in entities}
    for entity in entities:
        if entity.get("prefab_id") not in (None, *prefab_ids):
            raise ProjectError("entity references a missing prefab")
        logic = entity.get("logic", {})
        if logic.get("graph_id") not in (None, *graph_ids):
            raise ProjectError("entity references a missing graph")
        if logic.get("script_id") not in (None, *script_ids):
            raise ProjectError("entity references a missing script")
        seen_types: set[str] = set()
        for component in entity["components"]:
            kind = component["type"]
            if kind in seen_types:
                raise ProjectError("duplicate component type on entity")
            seen_types.add(kind)
            if kind == "Transform" and component["space"] != mode:
                raise ProjectError("transform mode mismatch")
            if (kind == "Sprite" and mode != "2d") or (kind in {"Mesh", "Light"} and mode != "3d"):
                raise ProjectError("component is not valid in this scene mode")
            if kind == "Camera" and mode == "2d" and component["projection"] != "orthographic":
                raise ProjectError("2D camera must be orthographic")
            if "asset_id" in component and component["asset_id"] not in asset_ids:
                raise ProjectError("component references a missing approved asset")
            if kind == "Health" and component["initial"] > component["maximum"]:
                raise ProjectError("initial health exceeds maximum")
        if "Transform" not in seen_types:
            raise ProjectError("entity has no Transform")
        seen_parents = {entity["id"]}
        parent_id = entity["parent_id"]
        while parent_id is not None:
            if parent_id not in parents or parent_id in seen_parents:
                raise ProjectError("missing parent or parent cycle")
            seen_parents.add(parent_id)
            parent_id = parents[parent_id]


def main(paths: list[str]) -> int:
    files = [Path(p) for p in paths]
    if not files:
        files = sorted((ROOT / "fixtures" / "valid").glob("*.project.json"))
    if not files:
        print("No project fixtures found", file=sys.stderr)
        return 2
    failures = 0
    for path in files:
        try:
            validate_project(json.loads(path.read_text(encoding="utf-8")))
            print(f"OK {path}")
        except (OSError, json.JSONDecodeError, ProjectError) as exc:
            print(f"FAIL {path}: {exc}", file=sys.stderr)
            failures += 1
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
