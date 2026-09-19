package dev.aisandbox.app

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.util.UUID

data class LocalProject(
    val id: String,
    val title: String,
    val mode: String,
    val headRevisionId: String = "",
    val lockVersion: Long = 0,
    val conflict: Boolean = false,
)

object LocalProjects {
    private const val PREFS = "stage1_projects"
    private const val KEY = "projects"
    private const val DIR = "projects"

    fun load(context: Context): List<LocalProject> = runCatching {
        val raw = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(KEY, "[]")
        val array = JSONArray(raw)
        buildList {
            for (index in 0 until array.length()) {
                val item = array.getJSONObject(index)
                val mode = item.getString("mode")
                if (mode == "2d" || mode == "3d") {
                    add(
                        LocalProject(
                            id = item.getString("id"),
                            title = item.getString("title"),
                            mode = mode,
                            headRevisionId = item.optString("head_revision_id"),
                            lockVersion = item.optLong("lock_version", 0),
                            conflict = item.optBoolean("conflict", false),
                        ),
                    )
                }
            }
        }
    }.getOrDefault(emptyList())

    fun create(context: Context, mode: String): LocalProject {
        require(mode == "2d" || mode == "3d")
        val projects = load(context)
        val title = if (mode == "2d") "Новая 2D-игра ${projects.size + 1}" else "Новая 3D-игра ${projects.size + 1}"
        val project = LocalProject(UUID.randomUUID().toString(), title, mode)
        val template = JSONObject(context.assets.open("demo/empty_$mode.project.json").bufferedReader().use { it.readText() })
        saveDocument(context, project.id, ProjectDocuments.newDocument(template, project.id, title))
        writeIndex(context, projects + project)
        return project
    }

    fun document(context: Context, id: String): JSONObject {
        val file = File(File(context.filesDir, DIR), "$id.json")
        return JSONObject(file.readText())
    }

    fun saveDocument(context: Context, id: String, document: JSONObject) {
        atomicWrite(File(File(context.filesDir, DIR).apply { mkdirs() }, "$id.json"), document.toString())
    }

    fun saveServerSnapshot(context: Context, id: String, document: JSONObject) {
        atomicWrite(File(File(context.filesDir, DIR).apply { mkdirs() }, "$id.server.json"), document.toString())
    }

    fun serverSnapshot(context: Context, id: String): JSONObject? {
        val file = File(File(context.filesDir, DIR), "$id.server.json")
        if (!file.exists()) return null
        return runCatching { JSONObject(file.readText()) }.getOrNull()
    }

    fun upsert(context: Context, project: LocalProject) {
        val projects = load(context).filterNot { it.id == project.id } + project
        writeIndex(context, projects)
    }

    fun replace(context: Context, project: LocalProject): List<LocalProject> {
        val projects = load(context).map { if (it.id == project.id) project else it }
        writeIndex(context, projects)
        return projects
    }

    private fun writeIndex(context: Context, projects: List<LocalProject>) {
        val array = JSONArray()
        projects.forEach { item ->
            array.put(
                JSONObject()
                    .put("id", item.id)
                    .put("title", item.title)
                    .put("mode", item.mode)
                    .put("head_revision_id", item.headRevisionId)
                    .put("lock_version", item.lockVersion)
                    .put("conflict", item.conflict),
            )
        }
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().putString(KEY, array.toString()).apply()
    }

    private fun atomicWrite(target: File, text: String) {
        val bytes = text.toByteArray(Charsets.UTF_8)
        require(bytes.size in 2..(2 * 1024 * 1024))
        val tmp = File(target.parentFile, "${target.name}.tmp")
        FileOutputStream(tmp).use { out ->
            out.write(bytes)
            out.flush()
            out.fd.sync()
        }
        if (!tmp.renameTo(target)) {
            target.delete()
            check(tmp.renameTo(target)) { "atomic save failed" }
        }
    }
}

object SyncKeys {
    private const val PREFS = "sync_keys"

    fun createKey(context: Context, projectId: String): String = getOrPut(context, "create:$projectId")

    fun changeKey(context: Context, projectId: String): String = getOrPut(context, "change:$projectId")

    fun rotateChangeKey(context: Context, projectId: String): String {
        val key = UUID.randomUUID().toString()
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString("change:$projectId", key)
            .apply()
        return key
    }

    fun clear(context: Context, projectId: String) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .remove("create:$projectId")
            .remove("change:$projectId")
            .apply()
    }

    private fun getOrPut(context: Context, name: String): String {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val existing = prefs.getString(name, null)
        if (!existing.isNullOrBlank()) return existing
        val key = UUID.randomUUID().toString()
        prefs.edit().putString(name, key).apply()
        return key
    }
}
