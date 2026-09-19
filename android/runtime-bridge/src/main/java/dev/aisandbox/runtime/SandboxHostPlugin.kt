package dev.aisandbox.runtime

import org.godotengine.godot.Godot
import org.godotengine.godot.plugin.GodotPlugin
import org.godotengine.godot.plugin.SignalInfo
import org.godotengine.godot.plugin.UsedByGodot

/**
 * Narrow host-to-engine control surface for the bundled, trusted Godot project.
 * No path, script source, class name, or arbitrary method call crosses this bridge.
 */
class SandboxHostPlugin(godot: Godot) : GodotPlugin(godot) {
    companion object {
        private val SELECT_SAMPLE = SignalInfo("select_sample", String::class.java)
        private val SET_PLAYING = SignalInfo("set_playing", Boolean::class.javaObjectType)
        private val LOAD_PROJECT = SignalInfo("load_project", String::class.java)
        private const val MAX_PROJECT_CHARS = 2 * 1024 * 1024
    }

    @Volatile private var selectedMode = "2d"
    @Volatile private var playing = false
    @Volatile private var pendingProject = ""

    override fun getPluginName() = "SandboxHost"

    override fun getPluginSignals() = setOf(SELECT_SAMPLE, SET_PLAYING, LOAD_PROJECT)

    @UsedByGodot
    fun getSelectedMode(): String = selectedMode

    @UsedByGodot
    fun isPlaying(): Boolean = playing

    @UsedByGodot
    fun getPendingProject(): String = pendingProject

    fun selectSample(mode: String) {
        require(mode == "2d" || mode == "3d")
        selectedMode = mode
        emitSignal(SELECT_SAMPLE.name, mode)
    }

    fun loadProject(json: String) {
        require(json.length in 2..MAX_PROJECT_CHARS)
        pendingProject = json
        emitSignal(LOAD_PROJECT.name, json)
    }

    fun setPlaying(value: Boolean) {
        playing = value
        emitSignal(SET_PLAYING.name, value)
    }
}
