package dev.aisandbox.app

import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI
import java.nio.charset.StandardCharsets
import java.util.UUID

sealed class ApiResult {
    data class Ok(val status: Int, val body: JSONObject?) : ApiResult()
    data class Err(val status: Int, val code: String, val message: String, val details: JSONObject?) : ApiResult()
}

class SandboxApi(private val settings: CloudSettings) {
    fun createProject(document: JSONObject, idempotencyKey: String): ApiResult {
        val manifest = document.getJSONObject("manifest")
        val body = JSONObject()
            .put("title", manifest.getString("title"))
            .put("mode", manifest.getString("mode"))
            .put("schema_version", 1)
            .put("manifest", document)
        return request("POST", "/api/v1/projects", body, idempotencyKey)
    }

    fun getProject(id: String): ApiResult = request("GET", "/api/v1/projects/$id", null, null)

    fun applyChanges(projectId: String, baseRevisionId: String, operations: JSONArray, idempotencyKey: String): ApiResult {
        val body = JSONObject()
            .put("base_revision_id", baseRevisionId)
            .put("operations", operations)
        return request("POST", "/api/v1/projects/$projectId/changes", body, idempotencyKey)
    }

    fun generate(projectId: String, baseRevisionId: String, prompt: String, idempotencyKey: String): ApiResult {
        val body = JSONObject()
            .put("prompt", prompt)
            .put("base_revision_id", baseRevisionId)
        return request("POST", "/api/v1/projects/$projectId/ai/generations", body, idempotencyKey, connectTimeoutMs = 8_000, readTimeoutMs = 180_000)
    }

    private fun request(
        method: String,
        path: String,
        body: JSONObject?,
        idempotencyKey: String?,
        connectTimeoutMs: Int = 8_000,
        readTimeoutMs: Int = 20_000,
    ): ApiResult {
        if (!settings.enabled) {
            return ApiResult.Err(0, "CLOUD_DISABLED", "Облако выключено", null)
        }
        val url = URI(settings.baseUrl.trimEnd('/') + path).toURL()
        val connection = (url.openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = connectTimeoutMs
            readTimeout = readTimeoutMs
            useCaches = false
            instanceFollowRedirects = false
            setRequestProperty("Accept", "application/json")
            setRequestProperty("Authorization", "Bearer ${settings.token}")
            if (idempotencyKey != null) {
                setRequestProperty("Idempotency-Key", idempotencyKey)
            }
            if (body != null) {
                doOutput = true
                setRequestProperty("Content-Type", "application/json; charset=utf-8")
            }
        }
        return try {
            if (body != null) {
                connection.outputStream.use { it.write(body.toString().toByteArray(StandardCharsets.UTF_8)) }
            }
            val status = connection.responseCode
            val raw = (if (status >= 400) connection.errorStream else connection.inputStream)
                ?.bufferedReader(StandardCharsets.UTF_8)?.use { it.readText() }
                .orEmpty()
            parse(status, raw)
        } catch (error: IOException) {
            ApiResult.Err(0, "NETWORK", "Нет связи с сервером", null)
        } finally {
            connection.disconnect()
        }
    }

    private fun parse(status: Int, raw: String): ApiResult {
        val json = raw.trim().takeIf { it.startsWith("{") }?.let { runCatching { JSONObject(it) }.getOrNull() }
        if (status in 200..299) {
            return ApiResult.Ok(status, json)
        }
        val error = json?.optJSONObject("error")
        return ApiResult.Err(
            status = status,
            code = error?.optString("code").orEmpty().ifBlank { "HTTP_$status" },
            message = error?.optString("message").orEmpty().ifBlank { "Ошибка сервера" },
            details = error?.optJSONObject("details"),
        )
    }

    companion object {
        fun newKey(): String = UUID.randomUUID().toString()

        fun projectFrom(body: JSONObject): RemoteProject = RemoteProject(
            id = body.getString("id"),
            title = body.getString("title"),
            mode = body.getString("mode"),
            headRevisionId = body.getString("head_revision_id"),
            lockVersion = body.optLong("lock_version", 0),
            manifest = body.optJSONObject("manifest"),
        )
    }
}

data class RemoteProject(
    val id: String,
    val title: String,
    val mode: String,
    val headRevisionId: String,
    val lockVersion: Long,
    val manifest: JSONObject?,
)
