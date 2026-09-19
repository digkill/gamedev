package dev.aisandbox.python;

import dev.aisandbox.python.SandboxTick;
import dev.aisandbox.python.SandboxReply;

/** Stage-0 IPC shape. This service never executes user code. */
interface IPythonSandbox {
    int protocolVersion();
    boolean isInterpreterAvailable();
    SandboxReply submitTick(in SandboxTick tick);
    void stopSession(String sessionId);
}
