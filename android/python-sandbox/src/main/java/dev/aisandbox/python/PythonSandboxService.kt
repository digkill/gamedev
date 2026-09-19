package dev.aisandbox.python

import android.app.Service
import android.content.Intent
import android.os.IBinder
import java.util.UUID

/**
 * Isolated process and typed Binder endpoint for the CPython feasibility spike.
 * It deliberately rejects all execution until an embedded interpreter and resource
 * enforcement have passed the security checks in the technical specification.
 */
class PythonSandboxService : Service() {
    private val binder = object : IPythonSandbox.Stub() {
        override fun protocolVersion() = 1

        override fun isInterpreterAvailable() = false

        override fun submitTick(tick: SandboxTick?): SandboxReply {
            if (tick == null || !tick.isStructurallyValid()) {
                return SandboxReply(SandboxReply.STATUS_INVALID_REQUEST, -1)
            }
            return SandboxReply(SandboxReply.STATUS_INTERPRETER_UNAVAILABLE, -1)
        }

        override fun stopSession(sessionId: String?) {
            // There is no interpreter/session to stop in stage 0.
        }
    }

    override fun onBind(intent: Intent?): IBinder = binder

    private fun SandboxTick.isStructurallyValid(): Boolean =
        sessionId.length <= 36 && runCatching { UUID.fromString(sessionId) }.isSuccess &&
            sequence >= 0 && tick >= 0 && eventType in 0..255 &&
            (targetId == null || targetId.length <= 36) && value.isFinite()
}
