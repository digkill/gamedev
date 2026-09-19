package dev.aisandbox.app

import org.json.JSONArray
import org.json.JSONObject

data class SceneEntity(
    val id: String,
    val name: String,
    val parentId: String? = null,
    val kinds: String = "",
    val depth: Int = 0,
)

data class InspectorField(
    val property: String,
    val label: String,
    val kind: FieldKind,
    val numbers: List<Double> = emptyList(),
    val text: String = "",
)

enum class FieldKind { Number, Integer, Vec2, Vec3, Color }

data class EntityInspector(
    val id: String,
    val name: String,
    val kinds: String,
    val fields: List<InspectorField>,
)

object ProjectDocuments {
    fun entryScene(document: JSONObject): JSONObject {
        val entryId = document.getJSONObject("manifest").getString("entry_scene_id")
        val scenes = document.getJSONArray("scenes")
        for (index in 0 until scenes.length()) {
            val scene = scenes.getJSONObject(index)
            if (scene.getString("id") == entryId) return scene
        }
        error("entry scene missing")
    }

    fun sceneName(document: JSONObject): String = entryScene(document).optString("name", "Сцена")

    fun entities(document: JSONObject): List<SceneEntity> {
        val raw = entryScene(document).getJSONArray("entities")
        val items = buildList {
            for (index in 0 until raw.length()) {
                val entity = raw.getJSONObject(index)
                add(
                    SceneEntity(
                        id = entity.getString("id"),
                        name = entity.getString("name"),
                        parentId = entity.parentId(),
                        kinds = entity.componentKinds(),
                    ),
                )
            }
        }
        val children = items.groupBy { it.parentId }
        val ordered = mutableListOf<SceneEntity>()
        fun walk(parentId: String?, depth: Int) {
            children[parentId].orEmpty().forEach { child ->
                ordered.add(child.copy(depth = depth))
                walk(child.id, depth + 1)
            }
        }
        walk(null, 0)
        items.filter { entity -> ordered.none { it.id == entity.id } }.forEach { ordered.add(it) }
        return ordered
    }

    fun inspector(document: JSONObject, entityId: String?): EntityInspector? {
        if (entityId.isNullOrBlank()) return null
        val entities = entryScene(document).getJSONArray("entities")
        for (index in 0 until entities.length()) {
            val entity = entities.getJSONObject(index)
            if (entity.getString("id") != entityId) continue
            return EntityInspector(
                id = entityId,
                name = entity.getString("name"),
                kinds = entity.componentKinds(),
                fields = inspectorFields(entity.getJSONArray("components")),
            )
        }
        return null
    }

    fun newDocument(template: JSONObject, projectId: String, title: String): JSONObject {
        val document = JSONObject(template.toString())
        document.getJSONObject("manifest").put("project_id", projectId).put("title", title)
        return document
    }
}

data class AppliedCommand(val document: JSONObject, val command: JSONObject, val inverse: JSONObject)

object ProjectCommands {
    fun createPrimitive(document: JSONObject, name: String, id: String): AppliedCommand {
        val next = JSONObject(document.toString())
        val scene = ProjectDocuments.entryScene(next)
        val entities = scene.getJSONArray("entities")
        require(entities.length() < 2000)
        val mode = next.getJSONObject("manifest").getString("mode")
        val offset = entities.length() * 48
        val entity = JSONObject()
            .put("id", id)
            .put("name", name)
            .put("parent_id", JSONObject.NULL)
            .put("components", primitiveComponents(mode, offset))
        entities.put(entity)
        val sceneId = scene.getString("id")
        val command = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "CreateEntity")
            .put("scene_id", sceneId)
            .put("entity", JSONObject(entity.toString()))
        val inverse = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "DeleteEntity")
            .put("scene_id", sceneId)
            .put("entity_id", id)
        return AppliedCommand(next, command, inverse)
    }

    fun deleteEntity(document: JSONObject, entityId: String): AppliedCommand {
        val next = JSONObject(document.toString())
        val scene = ProjectDocuments.entryScene(next)
        val entities = scene.getJSONArray("entities")
        val kept = JSONArray()
        var removed: JSONObject? = null
        for (index in 0 until entities.length()) {
            val entity = entities.getJSONObject(index)
            if (entity.optString("parent_id") == entityId) {
                error("entity has children")
            }
            if (entity.getString("id") == entityId) {
                removed = entity
            } else {
                kept.put(entity)
            }
        }
        val deleted = removed ?: error("entity missing")
        scene.put("entities", kept)
        val sceneId = scene.getString("id")
        val command = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "DeleteEntity")
            .put("scene_id", sceneId)
            .put("entity_id", entityId)
        val inverse = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "CreateEntity")
            .put("scene_id", sceneId)
            .put("entity", JSONObject(deleted.toString()))
        return AppliedCommand(next, command, inverse)
    }

    private fun primitiveComponents(mode: String, offset: Int): JSONArray {
        val components = JSONArray()
        if (mode == "2d") {
            components.put(
                JSONObject()
                    .put("type", "Transform")
                    .put("space", "2d")
                    .put("position", JSONArray().put(offset.toDouble()).put(0.0))
                    .put("rotation", 0)
                    .put("scale", JSONArray().put(1).put(1)),
            )
            components.put(
                JSONObject()
                    .put("type", "Sprite")
                    .put("shape", "rectangle")
                    .put("tint", "#4DA3FFFF")
                    .put("layer", 0),
            )
        } else {
            components.put(
                JSONObject()
                    .put("type", "Transform")
                    .put("space", "3d")
                    .put("position", JSONArray().put(offset / 48.0).put(0.0).put(0.0))
                    .put("rotation", JSONArray().put(0).put(0).put(0))
                    .put("scale", JSONArray().put(1).put(1).put(1)),
            )
            components.put(
                JSONObject()
                    .put("type", "Mesh")
                    .put("primitive", "box")
                    .put("material_color", "#FF9944FF"),
            )
        }
        return components
    }

    fun setProperty(document: JSONObject, entityId: String, property: String, value: Any): AppliedCommand {
        require(property in PROPERTY_ALLOWLIST)
        val next = JSONObject(document.toString())
        val scene = ProjectDocuments.entryScene(next)
        val entities = scene.getJSONArray("entities")
        var target: JSONObject? = null
        for (index in 0 until entities.length()) {
            val entity = entities.getJSONObject(index)
            if (entity.getString("id") == entityId) {
                target = entity
                break
            }
        }
        val entity = target ?: error("entity missing")
        val type = property.substringBefore('.')
        val field = property.substringAfter('.')
        val components = entity.getJSONArray("components")
        var component: JSONObject? = null
        for (index in 0 until components.length()) {
            val item = components.getJSONObject(index)
            if (item.getString("type") == type) {
                component = item
                break
            }
        }
        val current = component ?: error("component missing")
        val previous = cloneJson(current.get(field))
        current.put(field, jsonValue(value))
        val sceneId = scene.getString("id")
        val command = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "SetProperty")
            .put("scene_id", sceneId)
            .put("entity_id", entityId)
            .put("property", property)
            .put("value", jsonValue(value))
        val inverse = JSONObject()
            .put("operation_id", java.util.UUID.randomUUID().toString())
            .put("type", "SetProperty")
            .put("scene_id", sceneId)
            .put("entity_id", entityId)
            .put("property", property)
            .put("value", previous)
        return AppliedCommand(next, command, inverse)
    }
}

private val PROPERTY_ALLOWLIST = setOf(
    "Transform.position",
    "Transform.rotation",
    "Transform.scale",
    "Sprite.tint",
    "Sprite.layer",
    "Mesh.material_color",
    "Camera.active",
    "Camera.fov_degrees",
    "Camera.size",
    "Light.color",
    "Light.intensity",
)

private fun JSONObject.parentId(): String? =
    if (isNull("parent_id")) null else optString("parent_id").takeIf { it.isNotBlank() && it != "null" }

private fun JSONObject.componentKinds(): String {
    val components = optJSONArray("components") ?: return ""
    return buildList {
        for (index in 0 until components.length()) {
            add(components.getJSONObject(index).optString("type"))
        }
    }.filter { it.isNotBlank() }.joinToString(" · ")
}

private fun inspectorFields(components: JSONArray): List<InspectorField> = buildList {
    for (index in 0 until components.length()) {
        val component = components.getJSONObject(index)
        val type = component.getString("type")
        component.keys().asSequence().filter { it != "type" && it != "space" && it != "shape" && it != "primitive" }.forEach { field ->
            val raw = component.get(field)
            if (raw is Boolean) return@forEach
            val property = "$type.$field"
            if (property in PROPERTY_ALLOWLIST) {
                add(fieldOf(property, "$type · $field", raw))
            }
        }
    }
}

private fun fieldOf(property: String, label: String, raw: Any): InspectorField = when (raw) {
    is JSONArray -> {
        val numbers = (0 until raw.length()).map { raw.getDouble(it) }
        InspectorField(
            property = property,
            label = label,
            kind = if (numbers.size >= 3) FieldKind.Vec3 else FieldKind.Vec2,
            numbers = numbers,
            text = numbers.joinToString(" "),
        )
    }
    is Number -> InspectorField(
        property = property,
        label = label,
        kind = if (property.endsWith(".layer")) FieldKind.Integer else FieldKind.Number,
        numbers = listOf(raw.toDouble()),
        text = if (property.endsWith(".layer")) raw.toInt().toString() else raw.toString(),
    )
    else -> InspectorField(
        property = property,
        label = label,
        kind = FieldKind.Color,
        text = raw.toString(),
    )
}

private fun jsonValue(value: Any): Any = when (value) {
    is JSONArray -> JSONArray(value.toString())
    is List<*> -> JSONArray().also { array -> value.forEach { item -> array.put(item) } }
    else -> value
}

private fun cloneJson(value: Any): Any = when (value) {
    is JSONArray -> JSONArray(value.toString())
    is JSONObject -> JSONObject(value.toString())
    else -> value
}
