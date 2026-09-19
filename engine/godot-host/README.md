# Godot stage-0 host

Доверенный прототип просмотра 2D/3D-сцены. Загружает пакетные JSON-примеры из `demo/` или JSON-документ через singleton `SandboxHost` (`load_project`). Недоверенные скрипты, GLB и файлы пользователя не запускает. Android-хост также может послать `select_sample(mode)` и `set_playing(playing)`.

Запуск локальной проверки:

```sh
/Applications/Godot.app/Contents/MacOS/Godot --headless --path engine/godot-host --quit-after 30
```

Пока это проверка запуска Godot и базового отображения. Она не подтверждает работу Android AAR, Python isolation и безопасность полного формата проекта. Парсер поддерживает только встроенные `Transform`, примитивные `Sprite` и `Mesh`; неизвестные компоненты отвергаются.
