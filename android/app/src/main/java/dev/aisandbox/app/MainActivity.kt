package dev.aisandbox.app

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.ServiceConnection
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import androidx.activity.addCallback
import androidx.appcompat.app.AppCompatActivity
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.platform.ComposeView
import androidx.compose.ui.platform.ViewCompositionStrategy
import androidx.fragment.app.FragmentContainerView
import dev.aisandbox.python.IPythonSandbox
import dev.aisandbox.python.PythonSandboxService
import dev.aisandbox.runtime.SandboxHostPlugin
import org.godotengine.godot.Godot
import org.godotengine.godot.GodotFragment
import org.godotengine.godot.GodotHost
import org.godotengine.godot.plugin.GodotPlugin
import org.json.JSONObject
import java.util.UUID
import java.util.concurrent.Executors

/** Keeps one Godot instance for library, editor preview, and play mode. */
class MainActivity : AppCompatActivity(), GodotHost {
    private var godotFragment: GodotFragment? = null
    private var hostPlugin: SandboxHostPlugin? = null
    private val projects = mutableStateListOf<LocalProject>()
    private var selectedProject by mutableStateOf<LocalProject?>(null)
    private val entities = mutableStateListOf<SceneEntity>()
    private var selectedEntityId by mutableStateOf<String?>(null)
    private var playing by mutableStateOf(false)
    private var sandboxStatus by mutableStateOf("Проверка IPC…")
    private var cloudStatus by mutableStateOf("Локальный режим")
    private var apiUrl by mutableStateOf(CloudSettings.DEFAULT_URL)
    private var apiToken by mutableStateOf("")
    private var canUndo by mutableStateOf(false)
    private var syncing by mutableStateOf(false)
    private var pendingCount by mutableStateOf(0)
    private var inspector by mutableStateOf<EntityInspector?>(null)
    private var sceneName by mutableStateOf("Сцена")
    private var promptText by mutableStateOf("")
    private var aiStatus by mutableStateOf("Опишите игру и нажмите «Создать»")
    private var generating by mutableStateOf(false)
    private val undoStack = ArrayDeque<UndoFrame>()
    private val io = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private lateinit var toolbarView: View
    private lateinit var navigatorView: View
    private lateinit var inspectorView: View
    private lateinit var promptView: View
    private lateinit var libraryView: View

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        projects.addAll(LocalProjects.load(this))
        CloudSettings.load(this).also { settings ->
            apiUrl = settings.baseUrl
            apiToken = settings.token
            cloudStatus = if (settings.enabled) "Облако: вы вошли" else "Нажмите «Войти»"
        }

        val root = FrameLayout(this)
        val editor = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        }
        toolbarView = composeView {
            selectedProject?.let { project ->
                EditorTopBar(
                    project = project,
                    playing = playing,
                    canUndo = canUndo,
                    syncing = syncing,
                    pendingCount = pendingCount,
                    cloudStatus = cloudStatus,
                    onLibrary = ::showLibrary,
                    onPlay = ::startPlay,
                    onStop = ::stopPlay,
                    onUndo = ::undo,
                    onSync = { selectedProject?.let(::queueSync) },
                    onTakeServer = { selectedProject?.let(::queueTakeServer) },
                    onRetryBase = { selectedProject?.let(::queueRetryBase) },
                )
            }
        }
        val body = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            layoutParams = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f)
        }
        navigatorView = composeView {
            NavigatorPanel(
                sceneName = sceneName,
                entities = entities,
                selectedEntityId = selectedEntityId,
                syncing = syncing,
                onAdd = ::addPrimitive,
                onDelete = ::deleteSelected,
                onSelectEntity = { entity ->
                    selectedEntityId = entity.id
                    selectedProject?.let { reloadInspector(it) }
                },
            )
        }.apply {
            layoutParams = LinearLayout.LayoutParams(dp(176), ViewGroup.LayoutParams.MATCH_PARENT)
        }
        val viewport = FragmentContainerView(this).apply {
            id = R.id.godot_container
            layoutParams = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, 1f)
        }
        inspectorView = composeView {
            InspectorPanel(
                inspector = inspector,
                syncing = syncing,
                onApply = ::applyProperty,
            )
        }.apply {
            layoutParams = LinearLayout.LayoutParams(dp(208), ViewGroup.LayoutParams.MATCH_PARENT)
        }
        promptView = composeView {
            AiPromptBar(
                prompt = promptText,
                status = aiStatus,
                sending = generating,
                onPrompt = { promptText = it },
                onGenerate = ::requestGeneration,
            )
        }
        body.addView(navigatorView)
        body.addView(viewport)
        body.addView(inspectorView)
        editor.addView(toolbarView, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        editor.addView(body)
        editor.addView(promptView, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        libraryView = composeView {
            if (selectedProject == null) {
                LibraryScreen(
                    projects = projects,
                    sandboxStatus = sandboxStatus,
                    cloudStatus = cloudStatus,
                    signedIn = CloudSettings(apiUrl, apiToken).enabled,
                    onSignIn = ::signInCloud,
                    onSignOut = ::signOutCloud,
                    onCreate = ::createProject,
                    onSelect = ::selectProject,
                )
            }
        }.apply {
            layoutParams = FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
        }
        root.addView(editor)
        root.addView(libraryView)
        setContentView(root)
        refreshChrome()

        godotFragment = (supportFragmentManager.findFragmentById(R.id.godot_container) as? GodotFragment)
            ?: GodotFragment().also { fragment ->
                supportFragmentManager.beginTransaction()
                    .replace(R.id.godot_container, fragment)
                    .commitNow()
            }

        onBackPressedDispatcher.addCallback(this) {
            if (playing) stopPlay() else showLibrary()
        }
        probeSandboxService()
    }

    override fun onDestroy() {
        io.shutdownNow()
        super.onDestroy()
    }

    override fun onStop() {
        if (playing) stopPlay()
        super.onStop()
    }

    private fun signInCloud() {
        val settings = CloudSettings.signIn(this)
        apiUrl = settings.baseUrl
        apiToken = settings.token
        cloudStatus = if (settings.enabled) "Облако: вы вошли" else "В приложении нет данных входа"
    }

    private fun signOutCloud() {
        CloudSettings.signOut(this)
        apiUrl = CloudSettings.DEFAULT_URL
        apiToken = ""
        cloudStatus = "Локальный режим"
    }

    private fun createProject(mode: String) {
        val project = LocalProjects.create(this, mode)
        projects.add(project)
        selectProject(project)
        queueSync(project)
    }

    private fun selectProject(project: LocalProject) {
        selectedProject = project
        playing = false
        undoStack.clear()
        canUndo = false
        pendingCount = ProjectOutbox.load(this, project.id).length()
        reloadScene(project)
        hostPlugin?.setPlaying(false)
        refreshChrome()
    }

    private fun showLibrary() {
        playing = false
        hostPlugin?.setPlaying(false)
        selectedProject = null
        selectedEntityId = null
        inspector = null
        sceneName = "Сцена"
        entities.clear()
        undoStack.clear()
        canUndo = false
        pendingCount = 0
        refreshChrome()
    }

    private fun startPlay() {
        val project = selectedProject ?: return
        reloadScene(project)
        hostPlugin?.setPlaying(true)
        playing = true
        refreshChrome()
    }

    private fun stopPlay() {
        playing = false
        hostPlugin?.setPlaying(false)
        refreshChrome()
    }

    private fun addPrimitive() {
        val project = selectedProject ?: return
        val current = LocalProjects.document(this, project.id)
        val applied = ProjectCommands.createPrimitive(current, primitiveName(project), UUID.randomUUID().toString())
        pushUndo(project, current, applied)
        ProjectSync.enqueue(this, project.id, applied.command)
        persistAndShow(project, applied.document)
        queueSync(project)
    }

    private fun deleteSelected() {
        val project = selectedProject ?: return
        val entityId = selectedEntityId ?: return
        val current = LocalProjects.document(this, project.id)
        val applied = runCatching { ProjectCommands.deleteEntity(current, entityId) }.getOrElse { return }
        pushUndo(project, current, applied)
        ProjectSync.enqueue(this, project.id, applied.command)
        selectedEntityId = null
        persistAndShow(project, applied.document)
        queueSync(project)
    }

    private fun undo() {
        val project = selectedProject ?: return
        val frame = undoStack.removeFirstOrNull() ?: return
        canUndo = undoStack.isNotEmpty()
        LocalProjects.saveDocument(this, project.id, JSONObject(frame.document))
        val queued = ProjectOutbox.load(this, project.id)
        var stillQueued = false
        for (index in 0 until queued.length()) {
            if (queued.getJSONObject(index).optString("operation_id") == frame.operationId) {
                stillQueued = true
                break
            }
        }
        if (stillQueued) {
            ProjectOutbox.restore(this, project.id, frame.outbox)
        } else {
            ProjectSync.enqueue(this, project.id, frame.inverse)
        }
        persistAndShow(project, JSONObject(frame.document))
        queueSync(project)
    }

    private fun persistAndShow(project: LocalProject, document: JSONObject) {
        LocalProjects.saveDocument(this, project.id, document)
        pendingCount = ProjectOutbox.load(this, project.id).length()
        reloadScene(project, document)
    }

    private fun reloadScene(project: LocalProject, document: JSONObject = LocalProjects.document(this, project.id)) {
        entities.clear()
        entities.addAll(ProjectDocuments.entities(document))
        sceneName = ProjectDocuments.sceneName(document)
        if (selectedEntityId != null && entities.none { it.id == selectedEntityId }) {
            selectedEntityId = null
        }
        inspector = ProjectDocuments.inspector(document, selectedEntityId)
        hostPlugin?.loadProject(document.toString())
    }

    private fun reloadInspector(project: LocalProject) {
        inspector = ProjectDocuments.inspector(LocalProjects.document(this, project.id), selectedEntityId)
    }

    private fun applyProperty(property: String, value: Any) {
        val project = selectedProject ?: return
        val entityId = selectedEntityId ?: return
        val current = LocalProjects.document(this, project.id)
        val applied = runCatching { ProjectCommands.setProperty(current, entityId, property, value) }.getOrElse { return }
        pushUndo(project, current, applied)
        ProjectSync.enqueue(this, project.id, applied.command)
        persistAndShow(project, applied.document)
        queueSync(project)
    }

    private fun requestGeneration() {
        val project = selectedProject ?: return
        val prompt = promptText.trim()
        if (prompt.isEmpty() || generating || playing) return
        generating = true
        aiStatus = "Генерация…"
        val snapshot = latest(project)
        val ctx = applicationContext
        io.execute {
            val outcome = runCatching { ProjectSync.generate(ctx, snapshot, prompt) }
                .getOrElse { SyncOutcome.Failed(it.javaClass.simpleName) }
            main.post {
                if (isDestroyed) return@post
                generating = false
                applyOutcome(outcome)
            }
        }
    }

    private fun pushUndo(project: LocalProject, document: JSONObject, applied: AppliedCommand) {
        undoStack.addFirst(
            UndoFrame(
                document = document.toString(),
                outbox = ProjectOutbox.snapshot(this, project.id),
                operationId = applied.command.getString("operation_id"),
                inverse = applied.inverse,
            ),
        )
        while (undoStack.size > 100) undoStack.removeLast()
        canUndo = true
    }

    private fun primitiveName(project: LocalProject): String {
        val kind = if (project.mode == "2d") "Прямоугольник" else "Куб"
        return "$kind ${entities.size + 1}"
    }

    private fun queueSync(project: LocalProject) {
        val snapshot = latest(project)
        runCloud { ctx -> ProjectSync.sync(ctx, snapshot) }
    }

    private fun queueTakeServer(project: LocalProject) {
        val snapshot = latest(project)
        runCloud { ctx -> ProjectSync.takeServer(ctx, snapshot) }
    }

    private fun queueRetryBase(project: LocalProject) {
        val snapshot = latest(project)
        runCloud { ctx -> ProjectSync.retryOnServerBase(ctx, snapshot) }
    }

    private fun latest(project: LocalProject): LocalProject =
        projects.firstOrNull { it.id == project.id } ?: project

    private fun runCloud(block: (Context) -> SyncOutcome) {
        if (syncing) return
        syncing = true
        val ctx = applicationContext
        io.execute {
            val outcome = runCatching { block(ctx) }.getOrElse { SyncOutcome.Failed(it.javaClass.simpleName) }
            main.post {
                if (isDestroyed) return@post
                syncing = false
                applyOutcome(outcome)
            }
        }
    }

    private fun applyOutcome(outcome: SyncOutcome) {
        val project = when (outcome) {
            is SyncOutcome.Synced -> outcome.project.also { cloudStatus = "Облако: ревизия ${it.headRevisionId.take(8)}" }
            is SyncOutcome.Conflict -> outcome.project.also { cloudStatus = outcome.message }
            is SyncOutcome.Generated -> {
                aiStatus = outcome.summary
                cloudStatus = "Облако: ревизия ${outcome.project.headRevisionId.take(8)}"
                outcome.project
            }
            is SyncOutcome.Failed -> {
                aiStatus = outcome.message
                cloudStatus = "Облако: ${outcome.message}"
                selectedProject
            }
            is SyncOutcome.Offline -> {
                aiStatus = outcome.message
                cloudStatus = outcome.message
                selectedProject
            }
        } ?: return
        publish(project)
        pendingCount = ProjectOutbox.load(this, project.id).length()
        if (selectedProject?.id == project.id && (outcome is SyncOutcome.Synced || outcome is SyncOutcome.Generated)) {
            reloadScene(project)
        }
    }

    private fun publish(project: LocalProject) {
        val index = projects.indexOfFirst { it.id == project.id }
        if (index >= 0) projects[index] = project else projects.add(project)
        if (selectedProject?.id == project.id) selectedProject = project
    }

    private fun refreshChrome() {
        if (!::libraryView.isInitialized) return
        val inLibrary = selectedProject == null
        val inEditor = selectedProject != null && !playing
        libraryView.visibility = if (inLibrary) View.VISIBLE else View.GONE
        toolbarView.visibility = if (inLibrary) View.GONE else View.VISIBLE
        navigatorView.visibility = if (inEditor) View.VISIBLE else View.GONE
        inspectorView.visibility = if (inEditor) View.VISIBLE else View.GONE
        promptView.visibility = if (inEditor) View.VISIBLE else View.GONE
    }

    private fun composeView(content: @Composable () -> Unit): ComposeView =
        ComposeView(this).apply {
            setBackgroundColor(Color.TRANSPARENT)
            setViewCompositionStrategy(ViewCompositionStrategy.DisposeOnViewTreeLifecycleDestroyed)
            setContent(content)
        }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    override fun getActivity() = this

    override fun getGodot(): Godot? = godotFragment?.godot

    override fun getHostPlugins(godot: Godot): Set<GodotPlugin> {
        val bridge = hostPlugin ?: SandboxHostPlugin(godot).also { hostPlugin = it }
        selectedProject?.let { reloadScene(it) }
        return setOf(bridge)
    }

    private fun probeSandboxService() {
        val appContext = applicationContext
        val mainHandler = Handler(Looper.getMainLooper())
        val connection = object : ServiceConnection {
            override fun onServiceConnected(name: ComponentName, binder: IBinder) {
                Thread({
                    val result = runCatching {
                        val service = IPythonSandbox.Stub.asInterface(binder)
                        val version = service.protocolVersion()
                        if (version == 1 && !service.isInterpreterAvailable) {
                            "IPC готов; CPython пока не встроен"
                        } else {
                            "Неподдерживаемый sandbox"
                        }
                    }.getOrElse { "IPC недоступен: ${it.javaClass.simpleName}" }
                    mainHandler.post {
                        sandboxStatus = result
                        runCatching { appContext.unbindService(this) }
                    }
                }, "sandbox-ipc-probe").start()
            }

            override fun onServiceDisconnected(name: ComponentName) {
                sandboxStatus = "IPC отключён"
            }
        }
        val bound = appContext.bindService(
            Intent(appContext, PythonSandboxService::class.java),
            connection,
            Context.BIND_AUTO_CREATE,
        )
        if (!bound) sandboxStatus = "IPC не запущен"
    }
}

private data class UndoFrame(
    val document: String,
    val outbox: String,
    val operationId: String,
    val inverse: JSONObject,
)
