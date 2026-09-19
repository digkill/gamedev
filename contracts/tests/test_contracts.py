from __future__ import annotations

import copy
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from validate import ProjectError, validate_project  # noqa: E402


ROOT = Path(__file__).resolve().parents[2]


def sample(mode: str) -> dict:
    path = ROOT / "contracts" / "fixtures" / "valid" / f"empty_{mode}.project.json"
    return json.loads(path.read_text(encoding="utf-8"))


class ProjectContractTests(unittest.TestCase):
    def test_bundled_godot_examples_match_contract_fixtures(self) -> None:
        for mode in ("2d", "3d"):
            with self.subTest(mode=mode):
                a = ROOT / "contracts" / "fixtures" / "valid" / f"empty_{mode}.project.json"
                b = ROOT / "engine" / "godot-host" / "demo" / f"empty_{mode}.project.json"
                self.assertEqual(a.read_bytes(), b.read_bytes())
                validate_project(sample(mode))

    def test_rejects_unsafe_fields_and_broken_references(self) -> None:
        base = sample("2d")

        def script_field(d: dict) -> None:
            d["scenes"][0]["entities"][0]["components"][1]["script_url"] = "https://example.test/code"

        def missing_scene(d: dict) -> None:
            d["manifest"]["entry_scene_id"] = d["manifest"]["project_id"]

        def parent_cycle(d: dict) -> None:
            entity = d["scenes"][0]["entities"][0]
            entity["parent_id"] = entity["id"]

        def wrong_mode(d: dict) -> None:
            d["scenes"][0]["entities"][0]["components"][0]["space"] = "3d"

        def unresolved_asset(d: dict) -> None:
            component = d["scenes"][0]["entities"][0]["components"][1]
            component.pop("shape")
            component["asset_id"] = d["manifest"]["project_id"]

        for name, mutate in {
            "script field": script_field,
            "missing scene": missing_scene,
            "parent cycle": parent_cycle,
            "wrong mode": wrong_mode,
            "unresolved asset": unresolved_asset,
        }.items():
            with self.subTest(name=name):
                doc = copy.deepcopy(base)
                mutate(doc)
                with self.assertRaises(ProjectError):
                    validate_project(doc)


if __name__ == "__main__":
    unittest.main()
