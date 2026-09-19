# Android: этап 0–1

Это измеримый каркас, а не готовый конструктор версии 1.0. Приложение создаёт локальные GameProject-документы 2D/3D, показывает их в доверенном Godot, даёт дерево объектов, добавление/удаление примитива, отмену и режим «Играть/Стоп». OpenAI и CPython пока отсутствуют.

## Сборка

Нужны JDK 17, Android SDK 36/Build Tools 36.0.0 и Gradle 9.6.1. Из каталога `android/`:

```sh
ANDROID_HOME="$HOME/Library/Android/sdk" gradle :app:assembleDebug
```

Gradle берёт исходники доверенного проекта из `../engine/godot-host/` и копирует их в APK assets. Там должен лежать `project.godot` в корне. При сборке исключаются `.godot`, `.git`, `*.import` и временные файлы. AAR `org.godotengine:godot:4.6.1.stable` закреплён в `runtime-bridge`.

`SandboxHost` — единственный мост Android → доверенный Godot. Сигналы `load_project(json)`, `select_sample("2d"|"3d")` и `set_playing(Boolean)` управляют сценой и режимом запуска. Документ передаётся как JSON, не как путь к файлу.

В редакторе: слева навигатор объектов, по центру viewport Godot, справа свойства выбранного объекта, снизу поле промпта. «Создать» синхронизирует проект и вызывает `POST /api/v1/projects/{id}/ai/generations`. Ключ OpenAI остаётся на сервере. Если ключа нет, API отвечает `503` и локальная сцена не меняется.

Локальное сохранение пишет `filesDir/projects/{id}.json` через временный файл и rename. Очередь команд лежит рядом в `{id}.outbox.json`. Если в библиотеке заданы URL API и bearer-токен (`go run ./cmd/devseed`), приложение создаёт проект на сервере и отправляет `CreateEntity`/`DeleteEntity` с `base_revision_id`. Для эмулятора URL по умолчанию `http://10.0.2.2:8080`. Ответ `409 REVISION_CONFLICT` не затирает локальную сцену: можно взять серверную копию или повторить очередь на новой базовой ревизии.

`PythonSandboxService` зарегистрирован с `android:isolatedProcess="true"` и `android:exported="false"`. AIDL содержит типизированное сообщение `SandboxTick`, но сервис возвращает `STATUS_INTERPRETER_UNAVAILABLE`; пользовательский код не выполняется. Перед включением CPython нужны проверки AC-07–AC-09 на физическом устройстве, жёсткие лимиты и watchdog. Текущий интерфейс IPC нельзя трактовать как доказанную границу безопасности.

Для Godot Android library [документация Godot](https://docs.godotengine.org/en/stable/tutorials/platform/android/android_library.html) предупреждает об одной инстанции на процесс и риске изменения размеров/orientation. Здесь активность закреплена в landscape и сохраняет один `GodotFragment`; измерения lifecycle, утечек и frame time на arm64 всё ещё нужны.
