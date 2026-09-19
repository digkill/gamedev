package dev.aisandbox.app

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp

@Composable
fun LibraryScreen(
    projects: List<LocalProject>,
    sandboxStatus: String,
    cloudStatus: String,
    signedIn: Boolean,
    onSignIn: () -> Unit,
    onSignOut: () -> Unit,
    onCreate: (String) -> Unit,
    onSelect: (LocalProject) -> Unit,
) {
    MaterialTheme {
        Surface(Modifier.fillMaxSize()) {
            Column(
                Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                Text("AI Sandbox", style = MaterialTheme.typography.headlineMedium)
                Text("Локальный GameProject и синхронизация с Go API")
                Text(sandboxStatus, style = MaterialTheme.typography.bodySmall)
                Text(cloudStatus, style = MaterialTheme.typography.bodySmall)
                if (signedIn) {
                    OutlinedButton(onClick = onSignOut, modifier = Modifier.heightIn(min = 48.dp)) {
                        Text("Выйти")
                    }
                } else {
                    Button(onClick = onSignIn, modifier = Modifier.heightIn(min = 48.dp)) {
                        Text("Войти")
                    }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    Button(onClick = { onCreate("2d") }, modifier = Modifier.heightIn(min = 48.dp)) {
                        Text("Создать 2D")
                    }
                    Button(onClick = { onCreate("3d") }, modifier = Modifier.heightIn(min = 48.dp)) {
                        Text("Создать 3D")
                    }
                }
                Text("Мои проекты", style = MaterialTheme.typography.titleLarge)
                if (projects.isEmpty()) Text("Проектов пока нет")
                projects.forEach { project ->
                    OutlinedButton(
                        onClick = { onSelect(project) },
                        modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp),
                    ) {
                        val state = when {
                            project.conflict -> "конфликт"
                            project.headRevisionId.isNotBlank() -> "в облаке"
                            else -> "локально"
                        }
                        Text("${project.title} · ${project.mode.uppercase()} · $state")
                    }
                }
            }
        }
    }
}

@Composable
fun EditorTopBar(
    project: LocalProject,
    playing: Boolean,
    canUndo: Boolean,
    syncing: Boolean,
    pendingCount: Int,
    cloudStatus: String,
    onLibrary: () -> Unit,
    onPlay: () -> Unit,
    onStop: () -> Unit,
    onUndo: () -> Unit,
    onSync: () -> Unit,
    onTakeServer: () -> Unit,
    onRetryBase: () -> Unit,
) {
    MaterialTheme {
        Surface(tonalElevation = 6.dp) {
            Column(Modifier.fillMaxWidth().padding(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = onLibrary, modifier = Modifier.heightIn(min = 48.dp), enabled = !playing) {
                        Text("Библиотека")
                    }
                    Text(
                        "${project.title} · ${project.mode.uppercase()}",
                        modifier = Modifier.weight(1f).padding(top = 12.dp),
                        style = MaterialTheme.typography.titleMedium,
                    )
                    if (playing) {
                        Button(onClick = onStop, modifier = Modifier.heightIn(min = 48.dp)) { Text("Стоп") }
                    } else {
                        OutlinedButton(onClick = onUndo, modifier = Modifier.heightIn(min = 48.dp), enabled = canUndo && !syncing) {
                            Text("Отмена")
                        }
                        OutlinedButton(onClick = onSync, modifier = Modifier.heightIn(min = 48.dp), enabled = !syncing) {
                            Text("Синхр.")
                        }
                        Button(onClick = onPlay, modifier = Modifier.heightIn(min = 48.dp)) { Text("Играть") }
                    }
                }
                Text(
                    cloudStatus + if (pendingCount > 0) " · очередь $pendingCount" else "",
                    style = MaterialTheme.typography.bodySmall,
                )
                if (project.conflict && !playing) {
                    Text("Проект изменён на другом устройстве. Локальная копия сохранена.")
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        OutlinedButton(onClick = onTakeServer, modifier = Modifier.heightIn(min = 48.dp), enabled = !syncing) {
                            Text("Взять серверную")
                        }
                        OutlinedButton(onClick = onRetryBase, modifier = Modifier.heightIn(min = 48.dp), enabled = !syncing) {
                            Text("Повторить с новой базой")
                        }
                    }
                }
            }
        }
    }
}

@Composable
fun NavigatorPanel(
    sceneName: String,
    entities: List<SceneEntity>,
    selectedEntityId: String?,
    syncing: Boolean,
    onAdd: () -> Unit,
    onDelete: () -> Unit,
    onSelectEntity: (SceneEntity) -> Unit,
) {
    MaterialTheme {
        Surface(Modifier.fillMaxSize(), tonalElevation = 4.dp) {
            Column(
                Modifier.fillMaxHeight().verticalScroll(rememberScrollState()).padding(8.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Text("Навигатор", style = MaterialTheme.typography.titleMedium)
                Text(sceneName, style = MaterialTheme.typography.bodySmall)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(onClick = onAdd, modifier = Modifier.weight(1f).heightIn(min = 48.dp), enabled = !syncing) {
                        Text("Добавить")
                    }
                    OutlinedButton(
                        onClick = onDelete,
                        modifier = Modifier.weight(1f).heightIn(min = 48.dp),
                        enabled = selectedEntityId != null && !syncing,
                    ) {
                        Text("Удалить")
                    }
                }
                if (entities.isEmpty()) Text("В сцене нет объектов")
                entities.forEach { entity ->
                    val selected = entity.id == selectedEntityId
                    OutlinedButton(
                        onClick = { onSelectEntity(entity) },
                        modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp).padding(start = (entity.depth * 12).dp),
                    ) {
                        Column(Modifier.fillMaxWidth()) {
                            Text(if (selected) "• ${entity.name}" else entity.name)
                            if (entity.kinds.isNotBlank()) {
                                Text(entity.kinds, style = MaterialTheme.typography.bodySmall)
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
fun InspectorPanel(
    inspector: EntityInspector?,
    syncing: Boolean,
    onApply: (String, Any) -> Unit,
) {
    MaterialTheme {
        Surface(Modifier.fillMaxSize(), tonalElevation = 4.dp) {
            Column(
                Modifier.fillMaxHeight().verticalScroll(rememberScrollState()).padding(8.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Text("Свойства", style = MaterialTheme.typography.titleMedium)
                if (inspector == null) {
                    Text("Выберите объект в навигаторе")
                    return@Column
                }
                Text(inspector.name, style = MaterialTheme.typography.titleSmall)
                if (inspector.kinds.isNotBlank()) {
                    Text(inspector.kinds, style = MaterialTheme.typography.bodySmall)
                }
                inspector.fields.forEach { field ->
                    PropertyEditor(field = field, enabled = !syncing, onApply = onApply)
                }
            }
        }
    }
}

@Composable
fun AiPromptBar(
    prompt: String,
    status: String,
    sending: Boolean,
    onPrompt: (String) -> Unit,
    onGenerate: () -> Unit,
) {
    MaterialTheme {
        Surface(tonalElevation = 6.dp) {
            Column(
                Modifier.fillMaxWidth().padding(8.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                Text("AI · генерация игры", style = MaterialTheme.typography.titleSmall)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(
                        value = prompt,
                        onValueChange = onPrompt,
                        modifier = Modifier.weight(1f).heightIn(min = 56.dp, max = 96.dp),
                        label = { Text("Опишите игру") },
                        placeholder = { Text("Платформер: герой бежит и прыгает по блокам") },
                        enabled = !sending,
                    )
                    Button(
                        onClick = onGenerate,
                        modifier = Modifier.heightIn(min = 48.dp),
                        enabled = !sending && prompt.isNotBlank(),
                    ) {
                        Text("Создать")
                    }
                }
                Text(status, style = MaterialTheme.typography.bodySmall)
            }
        }
    }
}

@Composable
private fun PropertyEditor(field: InspectorField, enabled: Boolean, onApply: (String, Any) -> Unit) {
    var text by remember(field.property, field.text) { mutableStateOf(field.text) }
    val apply: () -> Unit = {
        val parsed = parseField(field, text)
        if (parsed != null) onApply(field.property, parsed)
    }
    OutlinedTextField(
        value = text,
        onValueChange = { text = it },
        modifier = Modifier.fillMaxWidth(),
        enabled = enabled,
        singleLine = true,
        label = { Text(field.label) },
        keyboardOptions = KeyboardOptions(
            keyboardType = if (field.kind == FieldKind.Color) KeyboardType.Ascii else KeyboardType.Decimal,
            imeAction = ImeAction.Done,
        ),
        keyboardActions = KeyboardActions(onDone = { apply() }),
    )
    OutlinedButton(onClick = apply, modifier = Modifier.fillMaxWidth().heightIn(min = 48.dp), enabled = enabled) {
        Text("Применить")
    }
}

private fun parseField(field: InspectorField, raw: String): Any? {
    val text = raw.trim()
    return when (field.kind) {
        FieldKind.Integer -> text.toIntOrNull()
        FieldKind.Number -> text.toDoubleOrNull()
        FieldKind.Vec2 -> parseNumbers(text, 2)
        FieldKind.Vec3 -> parseNumbers(text, 3)
        FieldKind.Color -> text.takeIf { it.matches(Regex("^#([0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$")) }
    }
}

private fun parseNumbers(text: String, count: Int): List<Double>? {
    val parts = text.split(',', ' ', ';').map { it.trim() }.filter { it.isNotEmpty() }
    if (parts.size != count) return null
    val numbers = parts.map { it.toDoubleOrNull() ?: return null }
    return numbers
}
