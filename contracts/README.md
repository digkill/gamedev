# GameProject v1 contracts

`project-schema/` contains Draft 2020-12 JSON Schema definitions for the editable project and typed change set. `sdk-protocol/` defines IPC envelopes for the future Python sandbox. The schemas do not grant runtime access: the app and backend accept only components they can execute safely.

Validate bundled fixtures and cross references:

```sh
python3 -m pip install -r contracts/requirements-dev.txt
python3 contracts/validate.py
python3 -m unittest discover -s contracts/tests -v
```

`validate.py` checks schema shape, scene/entity uniqueness, parent cycles, component modes, asset references, and graph references. It does not execute Python or download assets. For now, `engine/godot-host/demo` holds identical examples for offline Godot smoke tests; CI should compare these files byte-for-byte.

The Go backend stores a strict subset of this v1 schema: built-in primitives plus the gameplay layer — `Collider`, `Body`, `CharacterController`, `Health`, `Damage`, `Collectible`, `Goal`, `Patrol`, `Chase`, `Spawner`, `Timer`, `Tag`, `CameraFollow`, `UIWidget` — and per-scene `rules` that state how a level is won and lost. It still rejects external assets, user scripts, graphs, and prefabs. Every accepted component is a closed enumeration with bounded numbers, so a document that validates describes behaviour a trusted runtime can execute without sandboxing user code. Collider sizes are in the entity's local space: the visual primitive is one unit across and `Transform.scale` gives it its world size.

`fixtures/valid/platformer_2d`, `obstacle_3d`, and `arena_3d` are emitted by the backend's own compiler (`go test ./internal/gamespec -run TestWriteFixtures` with `GAMESPEC_FIXTURE_DIR` set). They exist so the two validators are checked against each other: whatever the Go compiler produces must also pass these schemas.

`engine/godot-host` renders only `Transform`, `Sprite`, and `Mesh`. It ignores the gameplay components, so a generated game validates and stores but is not yet playable in the bundled host. Implemented HTTP methods are listed in `openapi/openapi.yaml`.
