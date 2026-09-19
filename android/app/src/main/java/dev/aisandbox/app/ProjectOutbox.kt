package dev.aisandbox.app

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream

object ProjectOutbox {
    fun load(context: Context, projectId: String): JSONArray {
        val file = file(context, projectId)
        if (!file.exists()) return JSONArray()
        return runCatching { JSONArray(file.readText()) }.getOrDefault(JSONArray())
    }

    fun save(context: Context, projectId: String, operations: JSONArray) {
        val target = file(context, projectId)
        val tmp = File(target.parentFile, "${target.name}.tmp")
        val bytes = operations.toString().toByteArray(Charsets.UTF_8)
        FileOutputStream(tmp).use { out ->
            out.write(bytes)
            out.flush()
            out.fd.sync()
        }
        if (!tmp.renameTo(target)) {
            target.delete()
            check(tmp.renameTo(target)) { "outbox save failed" }
        }
    }

    fun append(context: Context, projectId: String, command: JSONObject) {
        val operations = load(context, projectId)
        operations.put(command)
        save(context, projectId, operations)
    }

    fun clear(context: Context, projectId: String) = save(context, projectId, JSONArray())

    fun drop(context: Context, projectId: String, operations: JSONArray) {
        val sent = ids(operations)
        if (sent.isEmpty()) return
        val remaining = JSONArray()
        val current = load(context, projectId)
        for (index in 0 until current.length()) {
            val item = current.getJSONObject(index)
            if (item.optString("operation_id") !in sent) {
                remaining.put(item)
            }
        }
        save(context, projectId, remaining)
    }

    private fun ids(operations: JSONArray): Set<String> = buildSet {
        for (index in 0 until operations.length()) {
            val id = operations.getJSONObject(index).optString("operation_id")
            if (id.isNotBlank()) add(id)
        }
    }

    fun snapshot(context: Context, projectId: String): String = load(context, projectId).toString()

    fun restore(context: Context, projectId: String, snapshot: String) {
        save(context, projectId, JSONArray(snapshot))
    }

    private fun file(context: Context, projectId: String): File {
        val dir = File(context.filesDir, "projects").apply { mkdirs() }
        return File(dir, "$projectId.outbox.json")
    }
}
