package dev.aisandbox.app

import android.content.Context
import org.json.JSONObject

sealed class SyncOutcome {
    data class Synced(val project: LocalProject) : SyncOutcome()
    data class Conflict(val project: LocalProject, val message: String) : SyncOutcome()
    data class Failed(val message: String) : SyncOutcome()
    data class Offline(val message: String) : SyncOutcome()
    data class Generated(val project: LocalProject, val summary: String) : SyncOutcome()
}

object ProjectSync {
    fun enqueue(context: Context, projectId: String, command: JSONObject) {
        ProjectOutbox.append(context, projectId, command)
        SyncKeys.rotateChangeKey(context, projectId)
    }

    fun sync(context: Context, project: LocalProject): SyncOutcome {
        val settings = CloudSettings.load(context)
        if (!settings.enabled) {
            return SyncOutcome.Offline("Локальный режим")
        }
        val api = SandboxApi(settings)
        val document = LocalProjects.document(context, project.id)
        var current = project
        if (current.headRevisionId.isBlank()) {
            when (val created = ensureRemote(api, context, current, document)) {
                is SyncOutcome.Synced -> current = created.project
                else -> return created
            }
        }
        return flushChanges(api, context, current)
    }

    fun generate(context: Context, project: LocalProject, prompt: String): SyncOutcome {
        val settings = CloudSettings.load(context)
        if (!settings.enabled) {
            return SyncOutcome.Offline("Для AI нужно облако и токен")
        }
        if (prompt.trim().isEmpty() || prompt.length > 10000) {
            return SyncOutcome.Failed("Промпт слишком короткий или длинный")
        }
        val synced = when (val flushed = sync(context, project)) {
            is SyncOutcome.Synced -> flushed.project
            else -> return flushed
        }
        if (ProjectOutbox.load(context, synced.id).length() > 0 || synced.headRevisionId.isBlank()) {
            return SyncOutcome.Failed("Сначала синхронизируйте локальную очередь")
        }
        val api = SandboxApi(settings)
        return when (val generated = api.generate(synced.id, synced.headRevisionId, prompt.trim(), SandboxApi.newKey())) {
            is ApiResult.Ok -> {
                val body = generated.body ?: return SyncOutcome.Failed("Пустой ответ сервера")
                val remote = SandboxApi.projectFrom(body.optJSONObject("project") ?: body)
                val manifest = remote.manifest ?: return SyncOutcome.Failed("Серверная ревизия без документа")
                LocalProjects.saveDocument(context, synced.id, manifest)
                ProjectOutbox.clear(context, synced.id)
                val adopted = adopt(synced, remote, conflict = false)
                persist(context, adopted)
                SyncOutcome.Generated(adopted, body.optString("summary").ifBlank { "Сцена обновлена" })
            }
            is ApiResult.Err -> if (generated.code == "REVISION_CONFLICT") {
                rememberServer(api, context, synced, generated)
            } else {
                SyncOutcome.Failed(generated.message)
            }
        }
    }

    private fun flushChanges(api: SandboxApi, context: Context, project: LocalProject): SyncOutcome {
        val operations = ProjectOutbox.load(context, project.id)
        if (operations.length() == 0) {
            val synced = project.copy(conflict = false)
            persist(context, synced)
            return SyncOutcome.Synced(synced)
        }
        return when (val changed = api.applyChanges(project.id, project.headRevisionId, operations, SyncKeys.changeKey(context, project.id))) {
            is ApiResult.Ok -> {
                val remote = SandboxApi.projectFrom(changed.body ?: return SyncOutcome.Failed("Пустой ответ сервера"))
                ProjectOutbox.drop(context, project.id, operations)
                val synced = adopt(project, remote, conflict = false)
                persist(context, synced)
                if (ProjectOutbox.load(context, synced.id).length() > 0) {
                    SyncKeys.rotateChangeKey(context, synced.id)
                    flushChanges(api, context, synced)
                } else {
                    SyncOutcome.Synced(synced)
                }
            }
            is ApiResult.Err -> if (changed.code == "REVISION_CONFLICT") {
                rememberServer(api, context, project, changed)
            } else {
                SyncOutcome.Failed(changed.message)
            }
        }
    }

    fun takeServer(context: Context, project: LocalProject): SyncOutcome {
        val settings = CloudSettings.load(context)
        if (!settings.enabled) return SyncOutcome.Offline("Локальный режим")
        val api = SandboxApi(settings)
        return when (val result = api.getProject(project.id)) {
            is ApiResult.Ok -> {
                val remote = SandboxApi.projectFrom(result.body ?: return SyncOutcome.Failed("Пустой ответ сервера"))
                val manifest = remote.manifest ?: return SyncOutcome.Failed("Серверная ревизия без документа")
                LocalProjects.saveDocument(context, project.id, manifest)
                ProjectOutbox.clear(context, project.id)
                val synced = adopt(project, remote, conflict = false)
                persist(context, synced)
                SyncOutcome.Synced(synced)
            }
            is ApiResult.Err -> SyncOutcome.Failed(result.message)
        }
    }

    fun retryOnServerBase(context: Context, project: LocalProject): SyncOutcome {
        val settings = CloudSettings.load(context)
        if (!settings.enabled) return SyncOutcome.Offline("Локальный режим")
        val api = SandboxApi(settings)
        return when (val result = api.getProject(project.id)) {
            is ApiResult.Ok -> {
                val remote = SandboxApi.projectFrom(result.body ?: return SyncOutcome.Failed("Пустой ответ сервера"))
                remote.manifest?.let { LocalProjects.saveServerSnapshot(context, project.id, it) }
                val rebased = project.copy(
                    headRevisionId = remote.headRevisionId,
                    lockVersion = remote.lockVersion,
                    conflict = false,
                )
                persist(context, rebased)
                SyncKeys.rotateChangeKey(context, rebased.id)
                sync(context, rebased)
            }
            is ApiResult.Err -> SyncOutcome.Failed(result.message)
        }
    }

    private fun ensureRemote(api: SandboxApi, context: Context, project: LocalProject, document: JSONObject): SyncOutcome {
        val posted = ProjectOutbox.load(context, project.id)
        return when (val created = api.createProject(document, SyncKeys.createKey(context, project.id))) {
            is ApiResult.Ok -> {
                val remote = SandboxApi.projectFrom(created.body ?: return SyncOutcome.Failed("Пустой ответ сервера"))
                ProjectOutbox.drop(context, project.id, posted)
                val synced = adopt(project, remote, conflict = false)
                persist(context, synced)
                SyncOutcome.Synced(synced)
            }
            is ApiResult.Err -> when (val existing = api.getProject(project.id)) {
                is ApiResult.Ok -> {
                    val remote = SandboxApi.projectFrom(existing.body ?: return SyncOutcome.Failed("Пустой ответ сервера"))
                    val synced = adopt(project, remote, conflict = false)
                    persist(context, synced)
                    SyncOutcome.Synced(synced)
                }
                is ApiResult.Err -> SyncOutcome.Failed(created.message)
            }
        }
    }

    private fun rememberServer(api: SandboxApi, context: Context, project: LocalProject, error: ApiResult.Err): SyncOutcome {
        val serverRevision = error.details?.optString("current_revision_id").orEmpty()
        when (val existing = api.getProject(project.id)) {
            is ApiResult.Ok -> {
                val remote = SandboxApi.projectFrom(existing.body ?: JSONObject())
                remote.manifest?.let { LocalProjects.saveServerSnapshot(context, project.id, it) }
                val conflicted = project.copy(
                    conflict = true,
                    lockVersion = remote.lockVersion,
                )
                persist(context, conflicted)
                val hint = serverRevision.ifBlank { remote.headRevisionId }
                return SyncOutcome.Conflict(conflicted, "Конфликт ревизий. Сервер: ${hint.take(8)}")
            }
            is ApiResult.Err -> {
                val conflicted = project.copy(conflict = true)
                persist(context, conflicted)
                return SyncOutcome.Conflict(conflicted, error.message)
            }
        }
    }

    private fun adopt(local: LocalProject, remote: RemoteProject, conflict: Boolean) = local.copy(
        title = remote.title,
        headRevisionId = remote.headRevisionId,
        lockVersion = remote.lockVersion,
        conflict = conflict,
    )

    private fun persist(context: Context, project: LocalProject) {
        LocalProjects.upsert(context, project)
    }
}
